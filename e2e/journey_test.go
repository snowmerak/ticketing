//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/snowmerak/ticketing/internal/config"
	"github.com/snowmerak/ticketing/internal/database"
)

// TestQueueToSingleSeatPurchase starts the built service, uses its public HTTP API,
// and verifies the resulting MySQL and Redis state. It owns one synthetic event.
func TestQueueToSingleSeatPurchase(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(cfg.MySQLDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("MySQL dependency is required: %v", err)
	}
	redisClient := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword})
	t.Cleanup(func() { _ = redisClient.Close() })
	if err := redisClient.Ping(ctx).Err(); err != nil {
		t.Fatalf("Redis dependency is required: %v", err)
	}

	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "ticketing")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/ticketing")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build service: %v\n%s", err, output)
	}
	runOneShot(t, ctx, binary, root, os.Environ(), "migrate")

	eventID := isolatedEventID(t, ctx, db, redisClient)
	event := strconv.FormatUint(eventID, 10)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if err := cleanupEvent(cleanupCtx, db, redisClient, eventID); err != nil {
			t.Errorf("clean up synthetic event %s: %v", event, err)
		}
	})
	seedEvent(t, ctx, db, eventID)

	baseEnv := append(os.Environ(),
		"EVENT_IDS="+event,
		"CAPACITY=1",
		"ADMISSION_RATE=100",
		"ADMISSION_BURST=100",
		"POLL_INTERVAL=100ms",
		"GRANT_TTL=3s",
		"SCHEDULER_INTERVAL=100ms",
		"TICKETING_LOG_LEVEL=warn",
	)
	runOneShot(t, ctx, binary, root, baseEnv, "init-state")
	client := &http.Client{Timeout: 2 * time.Second}
	baseURL, stop := startServer(t, ctx, binary, root, baseEnv, client)
	defer stop()

	index, err := client.Get(baseURL + "/")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(io.LimitReader(index.Body, 4096))
	_ = index.Body.Close()
	if index.StatusCode != http.StatusOK || !bytes.Contains(page, []byte("Ticketing V1")) {
		t.Fatalf("development client is unavailable: status=%d", index.StatusCode)
	}

	entryPath := "/events/" + event + "/entry"
	occupier := request(t, client, baseURL, http.MethodPost, entryPath, "e2e-occupier", nil, map[string]any{}, http.StatusOK)
	if occupier["kind"] != "DIRECT" {
		t.Fatalf("initial admission was not DIRECT: %v", occupier)
	}
	occupierBooking := field(t, object(t, occupier, "booking"), "booking_id")

	first := request(t, client, baseURL, http.MethodPost, entryPath, "e2e-buyer", nil, map[string]any{}, http.StatusOK)
	second := request(t, client, baseURL, http.MethodPost, entryPath, "e2e-buyer", nil, map[string]any{}, http.StatusOK)
	if first["kind"] != "QUEUE" || second["kind"] != "QUEUE" || field(t, first, "seq") == field(t, second, "seq") {
		t.Fatalf("same buyer did not get two independent tickets: first=%v second=%v", first, second)
	}
	ticket := field(t, first, "ticket")
	queueRequest := map[string]any{"event_id": event, "ticket": ticket}
	waiting := request(t, client, baseURL, http.MethodPost, "/queue/heartbeat", "e2e-buyer", nil, queueRequest, http.StatusOK)
	if waiting["status"] != "WAITING" {
		t.Fatalf("occupied capacity should keep buyer waiting: %v", waiting)
	}
	ticket = field(t, waiting, "ticket")

	// Restart the actual process while the queue ticket and occupier permit live in Redis.
	stop()
	baseURL, stop = startServer(t, ctx, binary, root, baseEnv, client)
	defer stop()
	request(t, client, baseURL, http.MethodPost, "/booking/leave", "e2e-occupier", nil,
		map[string]any{"event_id": event, "booking_id": occupierBooking}, http.StatusOK)

	var grant map[string]any
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		grant = request(t, client, baseURL, http.MethodPost, "/queue/heartbeat", "e2e-buyer", nil,
			map[string]any{"event_id": event, "ticket": ticket}, http.StatusOK)
		ticket = field(t, grant, "ticket")
		if grant["status"] == "GRANTED" {
			break
		}
		if grant["status"] != "WAITING" {
			t.Fatalf("unexpected queue status: %v", grant)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	if grant["status"] != "GRANTED" {
		t.Fatal("scheduler did not grant the original ticket after restart")
	}
	redeemBody := map[string]any{"event_id": event, "ticket": ticket, "grant_id": field(t, grant, "grant_id")}
	booking := request(t, client, baseURL, http.MethodPost, "/queue/redeem", "e2e-buyer", nil, redeemBody, http.StatusOK)
	replay := request(t, client, baseURL, http.MethodPost, "/queue/redeem", "e2e-buyer", nil, redeemBody, http.StatusOK)
	bookingID := field(t, booking, "booking_id")
	if field(t, replay, "booking_id") != bookingID {
		t.Fatalf("grant replay changed booking permit: first=%v replay=%v", booking, replay)
	}
	request(t, client, baseURL, http.MethodPost, "/booking/heartbeat", "e2e-buyer", nil,
		map[string]any{"event_id": event, "booking_id": bookingID}, http.StatusOK)

	seatMap := request(t, client, baseURL, http.MethodGet, "/events/"+event+"/seat-map", "e2e-buyer",
		map[string]string{"X-Booking-ID": bookingID}, nil, http.StatusOK)
	seats, ok := seatMap["seats"].([]any)
	if !ok || len(seats) != 2 {
		t.Fatalf("expected two seeded seats: %v", seatMap)
	}
	specified := map[string]any{"booking_id": bookingID, "mode": "specified", "seat_ids": []string{"1"}, "quantity": 1}
	hold := request(t, client, baseURL, http.MethodPost, "/events/"+event+"/holds", "e2e-buyer",
		map[string]string{"Idempotency-Key": "e2e-specified"}, specified, http.StatusCreated)
	holdID := field(t, hold, "hold_id")
	holdReplay := request(t, client, baseURL, http.MethodPost, "/events/"+event+"/holds", "e2e-buyer",
		map[string]string{"Idempotency-Key": "e2e-specified"}, specified, http.StatusCreated)
	if field(t, holdReplay, "hold_id") != holdID {
		t.Fatalf("hold retry created a different hold: %v", holdReplay)
	}
	cancelled := request(t, client, baseURL, http.MethodPost, "/holds/"+holdID+"/cancel", "e2e-buyer", nil, map[string]any{}, http.StatusOK)
	if cancelled["state"] != "CANCELLED" {
		t.Fatalf("hold did not cancel: %v", cancelled)
	}

	auto := request(t, client, baseURL, http.MethodPost, "/events/"+event+"/holds", "e2e-buyer",
		map[string]string{"Idempotency-Key": "e2e-auto"},
		map[string]any{"booking_id": bookingID, "mode": "auto", "section_id": "10", "quantity": 1}, http.StatusCreated)
	autoID := field(t, auto, "hold_id")
	confirmBody := map[string]any{"booking_id": bookingID, "payment_result_id": "e2e-payment"}
	order := request(t, client, baseURL, http.MethodPost, "/holds/"+autoID+"/confirm", "e2e-buyer", nil, confirmBody, http.StatusOK)
	orderReplay := request(t, client, baseURL, http.MethodPost, "/holds/"+autoID+"/confirm", "e2e-buyer", nil, confirmBody, http.StatusOK)
	if field(t, orderReplay, "order_id") != field(t, order, "order_id") {
		t.Fatalf("confirm retry created a second order: %v", orderReplay)
	}
	limit := request(t, client, baseURL, http.MethodPost, "/events/"+event+"/holds", "e2e-buyer",
		map[string]string{"Idempotency-Key": "e2e-second-purchase"},
		map[string]any{"booking_id": bookingID, "mode": "specified", "seat_ids": []string{"2"}, "quantity": 1}, http.StatusConflict)
	if limit["code"] != "SEAT_PURCHASE_LIMIT_EXCEEDED" {
		t.Fatalf("second purchase returned wrong error: %v", limit)
	}
	postPurchaseA := request(t, client, baseURL, http.MethodPost, entryPath, "e2e-buyer", nil, map[string]any{}, http.StatusOK)
	postPurchaseB := request(t, client, baseURL, http.MethodPost, entryPath, "e2e-buyer", nil, map[string]any{}, http.StatusOK)
	if postPurchaseA["kind"] != "QUEUE" || postPurchaseB["kind"] != "QUEUE" ||
		field(t, postPurchaseA, "seq") == field(t, postPurchaseB, "seq") {
		t.Fatalf("purchased buyer could not obtain multiple new tickets: %v %v", postPurchaseA, postPurchaseB)
	}
	postPurchaseTicket := field(t, postPurchaseA, "ticket")
	postPurchaseHeartbeat := request(t, client, baseURL, http.MethodPost, "/queue/heartbeat", "e2e-buyer", nil,
		map[string]any{"event_id": event, "ticket": postPurchaseTicket}, http.StatusOK)
	if postPurchaseHeartbeat["status"] != "WAITING" {
		t.Fatalf("buyer still occupies capacity, so the new ticket must wait: %v", postPurchaseHeartbeat)
	}
	postPurchaseTicket = field(t, postPurchaseHeartbeat, "ticket")
	request(t, client, baseURL, http.MethodPost, "/booking/leave", "e2e-buyer", nil,
		map[string]any{"event_id": event, "booking_id": bookingID}, http.StatusOK)
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		grant = request(t, client, baseURL, http.MethodPost, "/queue/heartbeat", "e2e-buyer", nil,
			map[string]any{"event_id": event, "ticket": postPurchaseTicket}, http.StatusOK)
		postPurchaseTicket = field(t, grant, "ticket")
		if grant["status"] == "GRANTED" {
			break
		}
		if grant["status"] != "WAITING" {
			t.Fatalf("unexpected post-purchase queue status: %v", grant)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	if grant["status"] != "GRANTED" {
		t.Fatal("purchased buyer's new ticket did not receive a grant")
	}
	newBooking := request(t, client, baseURL, http.MethodPost, "/queue/redeem", "e2e-buyer", nil,
		map[string]any{"event_id": event, "ticket": postPurchaseTicket, "grant_id": field(t, grant, "grant_id")}, http.StatusOK)
	newBookingID := field(t, newBooking, "booking_id")
	limit = request(t, client, baseURL, http.MethodPost, "/events/"+event+"/holds", "e2e-buyer",
		map[string]string{"Idempotency-Key": "e2e-other-permit"},
		map[string]any{"booking_id": newBookingID, "mode": "specified", "seat_ids": []string{"2"}, "quantity": 1}, http.StatusConflict)
	if limit["code"] != "SEAT_PURCHASE_LIMIT_EXCEEDED" {
		t.Fatalf("new booking permit bypassed purchase limit: %v", limit)
	}
	request(t, client, baseURL, http.MethodPost, "/booking/leave", "e2e-buyer", nil,
		map[string]any{"event_id": event, "booking_id": newBookingID}, http.StatusOK)

	var orderCount, soldCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM orders WHERE event_id = ? AND subject_id = 'e2e-buyer'`, eventID).Scan(&orderCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM seat_inventory WHERE event_id = ? AND state = 'SOLD'`, eventID).Scan(&soldCount); err != nil {
		t.Fatal(err)
	}
	if orderCount != 1 || soldCount != 1 {
		t.Fatalf("purchase invariant failed: orders=%d sold=%d", orderCount, soldCount)
	}
	control, err := redisClient.HMGet(ctx, "q:{"+event+"}:control", "active_count", "reserved_count").Result()
	if err != nil || fmt.Sprint(control) != "[0 0]" {
		t.Fatalf("capacity did not return after leave: values=%v err=%v", control, err)
	}
	t.Logf("PASS: direct -> queue -> restart -> purchase -> new ticket/permit -> purchase limit; event=%s orders=1 sold=1", event)
}

func isolatedEventID(t *testing.T, ctx context.Context, db *sql.DB, client *redis.Client) uint64 {
	t.Helper()
	for range 10 {
		random, err := rand.Int(rand.Reader, big.NewInt(500_000_000))
		if err != nil {
			t.Fatal(err)
		}
		id := uint64(3_000_000_000 + random.Int64())
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM seat_definitions WHERE event_id = ?`, id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		present, err := client.Exists(ctx, fmt.Sprintf("q:{%d}:control", id)).Result()
		if err != nil {
			t.Fatal(err)
		}
		if count == 0 && present == 0 {
			return id
		}
	}
	t.Fatal("could not allocate an isolated event ID")
	return 0
}

func seedEvent(t *testing.T, ctx context.Context, db *sql.DB, eventID uint64) {
	t.Helper()
	for seatID := 1; seatID <= 2; seatID++ {
		if _, err := db.ExecContext(ctx, `
INSERT INTO seat_definitions (event_id, seat_id, section_id, display_alias, row_label, seat_number, allocation_order)
VALUES (?, ?, 10, ?, 'R', ?, ?)`, eventID, seatID, fmt.Sprintf("R%d", seatID), seatID, seatID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `
INSERT INTO seat_inventory (event_id, seat_id, section_id, allocation_order) VALUES (?, ?, 10, ?)`, eventID, seatID, seatID); err != nil {
			t.Fatal(err)
		}
	}
}

func cleanupEvent(ctx context.Context, db *sql.DB, client *redis.Client, eventID uint64) error {
	statements := []string{
		`DELETE FROM event_purchase_guards WHERE event_id = ?`,
		`DELETE oi FROM order_items oi JOIN orders o ON o.order_id = oi.order_id WHERE o.event_id = ?`,
		`UPDATE seat_inventory SET state = 'AVAILABLE', hold_id = NULL, order_id = NULL WHERE event_id = ?`,
		`DELETE FROM orders WHERE event_id = ?`,
		`DELETE hi FROM hold_items hi JOIN holds h ON h.hold_id = hi.hold_id WHERE h.event_id = ?`,
		`DELETE FROM holds WHERE event_id = ?`,
		`DELETE FROM seat_inventory WHERE event_id = ?`,
		`DELETE FROM seat_definitions WHERE event_id = ?`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement, eventID); err != nil {
			return err
		}
	}
	pattern := fmt.Sprintf("q:{%d}:*", eventID)
	var cursor uint64
	for {
		keys, next, err := client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			if err := client.Del(ctx, keys...).Err(); err != nil {
				return err
			}
		}
		cursor = next
		if cursor == 0 {
			return nil
		}
	}
}

func runOneShot(t *testing.T, ctx context.Context, binary, root string, env []string, action string) {
	t.Helper()
	command := exec.CommandContext(ctx, binary, action)
	command.Dir = root
	command.Env = env
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s failed: %v\n%s", action, err, output)
	}
}

func startServer(t *testing.T, ctx context.Context, binary, root string, baseEnv []string, client *http.Client) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.Create(filepath.Join(t.TempDir(), "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "serve")
	command.Dir = root
	command.Env = append(append([]string(nil), baseEnv...), "HTTP_ADDR="+address)
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = command.Process.Kill()
		<-done
		_ = logFile.Close()
	}
	baseURL := "http://" + address
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			stopped = true
			_ = logFile.Close()
			logs, _ := os.ReadFile(logFile.Name())
			t.Fatalf("service exited before readiness: %v\n%s", err, logs)
		default:
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/readyz", nil)
		if err != nil {
			stop()
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return baseURL, stop
			}
		}
		select {
		case <-ctx.Done():
			break
		case <-time.After(100 * time.Millisecond):
		}
	}
	stop()
	logs, _ := os.ReadFile(logFile.Name())
	t.Fatalf("service did not become ready at %s\n%s", baseURL, logs)
	return "", nil
}

func request(t *testing.T, client *http.Client, baseURL, method, path, subject string, headers map[string]string, body any, wantStatus int) map[string]any {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, baseURL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Subject-ID", subject)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	response, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("%s %s: status %d, want %d, response %s", method, path, response.StatusCode, wantStatus, payload)
	}
	var result map[string]any
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatalf("%s %s: decode response: %v", method, path, err)
	}
	return result
}

func object(t *testing.T, source map[string]any, name string) map[string]any {
	t.Helper()
	value, ok := source[name].(map[string]any)
	if !ok {
		t.Fatalf("missing object %q in %v", name, source)
	}
	return value
}

func field(t *testing.T, source map[string]any, name string) string {
	t.Helper()
	value, ok := source[name].(string)
	if !ok || strings.TrimSpace(value) == "" {
		t.Fatalf("missing string %q in %v", name, source)
	}
	return value
}
