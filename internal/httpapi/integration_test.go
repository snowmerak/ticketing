//go:build integration

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/snowmerak/ticketing/internal/booking"
	"github.com/snowmerak/ticketing/internal/config"
	"github.com/snowmerak/ticketing/internal/database"
	"github.com/snowmerak/ticketing/internal/queue"
)

func TestQ15PurchaseThenIssueMultipleQueueTicketsE2EIntegration(t *testing.T) {
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.EventIDs = []uint64{100}
	cfg.Capacity = 8
	cfg.AdmissionRate = 1000
	cfg.AdmissionBurst = 1000
	db, err := database.Open(cfg.MySQLDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("MySQL integration dependency is required: %v", err)
	}
	redisClient := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword})
	t.Cleanup(func() { _ = redisClient.Close() })
	if err := redisClient.Ping(ctx).Err(); err != nil {
		t.Fatalf("Redis integration dependency is required: %v", err)
	}
	cleanupHTTPEventKeys(t, ctx, redisClient, 100)
	t.Cleanup(func() { cleanupHTTPEventKeys(t, context.Background(), redisClient, 100) })
	bookingStore := booking.NewStore(db, time.Minute, 8)
	if err := bookingStore.ResetEventForTests(ctx, 100); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bookingStore.ResetEventForTests(context.Background(), 100) })
	signer, err := queue.NewTicketSigner(cfg.HMACKeyID, map[string][]byte{cfg.HMACKeyID: cfg.HMACKey})
	if err != nil {
		t.Fatal(err)
	}
	queueStore := queue.NewStore(redisClient, signer, cfg)
	if err := queueStore.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	seatCache := booking.NewSeatCache(bookingStore, cfg.EventIDs, logger)
	server := httptest.NewServer(New(cfg, queueStore, bookingStore, seatCache, db, redisClient, logger).Handler())
	t.Cleanup(server.Close)

	entry := requestJSON(t, http.MethodPost, server.URL+"/events/100/entry", "e2e-buyer", "", map[string]any{})
	bookingValue := entry["booking"].(map[string]any)
	bookingID := bookingValue["booking_id"].(string)
	hold := requestJSON(t, http.MethodPost, server.URL+"/events/100/holds", "e2e-buyer", "e2e-hold", map[string]any{
		"booking_id": bookingID, "mode": "specified", "seat_ids": []string{"1"},
	})
	holdID := hold["hold_id"].(string)
	requestJSON(t, http.MethodPost, server.URL+"/holds/"+holdID+"/confirm", "e2e-buyer", "", map[string]any{
		"booking_id": bookingID, "payment_result_id": "e2e-payment",
	})

	controlKey := "q:{100}:control"
	if err := redisClient.HSet(ctx, controlKey, "pause", 1).Err(); err != nil {
		t.Fatal(err)
	}
	first := requestJSON(t, http.MethodPost, server.URL+"/events/100/entry", "e2e-buyer", "", map[string]any{})
	second := requestJSON(t, http.MethodPost, server.URL+"/events/100/entry", "e2e-buyer", "", map[string]any{})
	if first["kind"] != "QUEUE" || second["kind"] != "QUEUE" || first["ticket"] == second["ticket"] || first["seq"] == second["seq"] {
		t.Fatalf("purchased user must receive independent tickets: first=%v second=%v", first, second)
	}
	var orderCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM orders WHERE event_id = 100 AND subject_id = 'e2e-buyer'`).Scan(&orderCount); err != nil {
		t.Fatal(err)
	}
	if orderCount != 1 {
		t.Fatalf("queue issue changed purchase state: order count=%d", orderCount)
	}
}

func TestF01RedisUnavailableEntryFailsClosedIntegration(t *testing.T) {
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.DependencyTimeout = 100 * time.Millisecond
	db, err := database.Open(cfg.MySQLDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("MySQL integration dependency is required: %v", err)
	}
	unavailableRedis := redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:1", DialTimeout: 100 * time.Millisecond,
		ReadTimeout: 100 * time.Millisecond, WriteTimeout: 100 * time.Millisecond, MaxRetries: -1,
	})
	t.Cleanup(func() { _ = unavailableRedis.Close() })
	signer, err := queue.NewTicketSigner(cfg.HMACKeyID, map[string][]byte{cfg.HMACKeyID: cfg.HMACKey})
	if err != nil {
		t.Fatal(err)
	}
	queueStore := queue.NewStore(unavailableRedis, signer, cfg)
	bookingStore := booking.NewStore(db, time.Minute, 8)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	seatCache := booking.NewSeatCache(bookingStore, cfg.EventIDs, logger)
	server := httptest.NewServer(New(cfg, queueStore, bookingStore, seatCache, db, unavailableRedis, logger).Handler())
	t.Cleanup(server.Close)

	raw, err := json.Marshal(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, server.URL+"/events/100/entry", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Subject-ID", "redis-down-user")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusServiceUnavailable || payload["code"] != "REDIS_UNAVAILABLE" || payload["booking"] != nil {
		t.Fatalf("Redis outage did not fail closed: status=%d payload=%v", response.StatusCode, payload)
	}
}

func TestF02MySQLUnavailableEntryQueuesInsteadOfDirectIntegration(t *testing.T) {
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.EventIDs = []uint64{91006}
	cfg.DependencyTimeout = 100 * time.Millisecond
	redisClient := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword})
	t.Cleanup(func() { _ = redisClient.Close() })
	if err := redisClient.Ping(ctx).Err(); err != nil {
		t.Fatalf("Redis integration dependency is required: %v", err)
	}
	cleanupHTTPEventKeys(t, ctx, redisClient, cfg.EventIDs[0])
	t.Cleanup(func() { cleanupHTTPEventKeys(t, context.Background(), redisClient, cfg.EventIDs[0]) })
	db, err := database.Open(cfg.MySQLDSN)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("MySQL integration dependency is required: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	signer, err := queue.NewTicketSigner(cfg.HMACKeyID, map[string][]byte{cfg.HMACKeyID: cfg.HMACKey})
	if err != nil {
		t.Fatal(err)
	}
	queueStore := queue.NewStore(redisClient, signer, cfg)
	if err := queueStore.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	bookingStore := booking.NewStore(db, time.Minute, 8)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	seatCache := booking.NewSeatCache(bookingStore, cfg.EventIDs, logger)
	server := httptest.NewServer(New(cfg, queueStore, bookingStore, seatCache, db, redisClient, logger).Handler())
	t.Cleanup(server.Close)

	entry := requestJSON(t, http.MethodPost, server.URL+"/events/"+strconv.FormatUint(cfg.EventIDs[0], 10)+"/entry", "mysql-down-user", "", map[string]any{})
	if entry["kind"] != "QUEUE" || entry["ticket"] == nil || entry["booking"] != nil {
		t.Fatalf("MySQL outage minted a direct permit: %v", entry)
	}
}

func requestJSON(t *testing.T, method, target, subject, idempotencyKey string, body any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(method, target, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Subject-ID", subject)
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		t.Fatalf("%s %s returned %d: %s", method, target, response.StatusCode, payload)
	}
	var result map[string]any
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatalf("decode response: %v (%s)", err, payload)
	}
	return result
}

func cleanupHTTPEventKeys(t *testing.T, ctx context.Context, client *redis.Client, eventID uint64) {
	t.Helper()
	pattern := fmt.Sprintf("q:{%s}:*", strconv.FormatUint(eventID, 10))
	var cursor uint64
	for {
		keys, next, err := client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			t.Fatal(err)
		}
		if len(keys) > 0 {
			if err := client.Del(ctx, keys...).Err(); err != nil {
				t.Fatal(err)
			}
		}
		cursor = next
		if cursor == 0 {
			return
		}
	}
}
