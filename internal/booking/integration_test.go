//go:build integration

package booking

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/snowmerak/ticketing/internal/apierr"
	"github.com/snowmerak/ticketing/internal/config"
	"github.com/snowmerak/ticketing/internal/database"
)

func TestS18OneSeatPurchaseLimitUnderConcurrentConfirmationIntegration(t *testing.T) {
	ctx := context.Background()
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
		t.Fatalf("MySQL integration dependency is required: %v", err)
	}
	if err := database.Migrate(ctx, db, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db, time.Minute, 8)
	if err := store.ResetEventForTests(ctx, 200); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.ResetEventForTests(context.Background(), 200) })

	first, err := store.CreateHold(ctx, CreateHoldRequest{
		EventID: 200, SubjectID: "single-buyer", BookingID: "booking-a", IdempotencyKey: "hold-a",
		Mode: "specified", SeatIDs: []uint64{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateHold(ctx, CreateHoldRequest{
		EventID: 200, SubjectID: "single-buyer", BookingID: "booking-b", IdempotencyKey: "hold-b",
		Mode: "specified", SeatIDs: []uint64{2},
	})
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for index, hold := range []Hold{first, second} {
		wg.Add(1)
		go func(index int, hold Hold) {
			defer wg.Done()
			<-start
			_, err := store.ConfirmHold(ctx, hold.ID, "single-buyer", "payment-"+string(rune('a'+index)))
			results <- err
		}(index, hold)
	}
	close(start)
	wg.Wait()
	close(results)

	successes := 0
	limitFailures := 0
	for err := range results {
		if err == nil {
			successes++
		} else if apierr.As(err).Code == "SEAT_PURCHASE_LIMIT_EXCEEDED" {
			limitFailures++
		} else {
			t.Fatalf("unexpected confirmation error: %v", err)
		}
	}
	if successes != 1 || limitFailures != 1 {
		t.Fatalf("expected one order and one purchase-limit rejection, got success=%d limit=%d", successes, limitFailures)
	}
	var orderCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM orders WHERE event_id = 200 AND subject_id = 'single-buyer'`).Scan(&orderCount); err != nil {
		t.Fatal(err)
	}
	if orderCount != 1 {
		t.Fatalf("database invariant broken: got %d orders", orderCount)
	}
	_, err = store.CreateHold(ctx, CreateHoldRequest{
		EventID: 200, SubjectID: "single-buyer", BookingID: "booking-c", IdempotencyKey: "hold-c",
		Mode: "auto", SectionID: 10, Quantity: 1,
	})
	if err == nil || apierr.As(err).Code != "SEAT_PURCHASE_LIMIT_EXCEEDED" {
		t.Fatalf("post-purchase hold must be rejected by the strong guard, got %v", err)
	}
}

func TestS01SpecifiedSeatHasSingleConcurrentWinnerIntegration(t *testing.T) {
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(cfg.MySQLDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := NewStore(db, time.Minute, 8)
	if err := store.ResetEventForTests(ctx, 200); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.ResetEventForTests(context.Background(), 200) })

	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for index := 0; index < 2; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			_, err := store.CreateHold(ctx, CreateHoldRequest{
				EventID: 200, SubjectID: "buyer-" + string(rune('a'+index)), BookingID: "booking",
				IdempotencyKey: "seat-race", Mode: "specified", SeatIDs: []uint64{1},
			})
			results <- err
		}(index)
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	failures := 0
	for err := range results {
		if err == nil {
			successes++
		} else if code := apierr.As(err).Code; code == "SEAT_BUSY" || code == "SEAT_UNAVAILABLE" {
			failures++
		} else {
			t.Fatalf("unexpected seat race result: %v", err)
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("expected one seat winner and one rejection, got success=%d failures=%d", successes, failures)
	}
}

func TestS07MultiSeatRequestHasNoSideEffectsIntegration(t *testing.T) {
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(cfg.MySQLDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := NewStore(db, time.Minute, 8)
	if err := store.ResetEventForTests(ctx, 200); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.ResetEventForTests(context.Background(), 200) })

	requests := []CreateHoldRequest{
		{EventID: 200, SubjectID: "multi-buyer", BookingID: "booking-a", IdempotencyKey: "multi-a", Mode: "specified", SeatIDs: []uint64{1, 2}},
		{EventID: 200, SubjectID: "multi-buyer", BookingID: "booking-b", IdempotencyKey: "multi-b", Mode: "auto", SectionID: 10, Quantity: 2},
	}
	for _, request := range requests {
		if _, err := store.CreateHold(ctx, request); err == nil || apierr.As(err).Code != "SEAT_PURCHASE_LIMIT_EXCEEDED" {
			t.Fatalf("multi-seat request must fail with purchase limit, got %v", err)
		}
	}
	var holds, guards, changedInventory int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM holds WHERE event_id = 200`).Scan(&holds); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_purchase_guards WHERE event_id = 200`).Scan(&guards); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM seat_inventory WHERE event_id = 200 AND state <> 'AVAILABLE'`).Scan(&changedInventory); err != nil {
		t.Fatal(err)
	}
	if holds != 0 || guards != 0 || changedInventory != 0 {
		t.Fatalf("invalid multi-seat request changed state: holds=%d guards=%d inventory=%d", holds, guards, changedInventory)
	}
}

func TestHoldIdempotencyIncludesBookingPermitIntegration(t *testing.T) {
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(cfg.MySQLDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := NewStore(db, time.Minute, 8)
	if err := store.ResetEventForTests(ctx, 200); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.ResetEventForTests(context.Background(), 200) })
	request := CreateHoldRequest{
		EventID: 200, SubjectID: "idempotent-buyer", BookingID: "booking-a",
		IdempotencyKey: "same-key", Mode: "specified", SeatIDs: []uint64{1},
	}
	first, err := store.CreateHold(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateHold(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("same request returned a new hold: first=%s second=%s", first.ID, second.ID)
	}
	request.BookingID = "booking-b"
	if _, err := store.CreateHold(ctx, request); err == nil || apierr.As(err).Code != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("booking change must conflict for the same idempotency key, got %v", err)
	}
}

func TestS19PurchaseLimitIsScopedPerEventIntegration(t *testing.T) {
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(cfg.MySQLDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := NewStore(db, time.Minute, 8)
	events := []uint64{92001, 92002}
	for _, eventID := range events {
		if _, err := db.ExecContext(ctx, `
INSERT INTO seat_definitions (event_id, seat_id, section_id, display_alias, row_label, seat_number, allocation_order)
VALUES (?, 1, 10, 'R1', 'R', 1, 1)
ON DUPLICATE KEY UPDATE display_alias = VALUES(display_alias)`, eventID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `
INSERT INTO seat_inventory (event_id, seat_id, section_id, allocation_order)
VALUES (?, 1, 10, 1)
ON DUPLICATE KEY UPDATE state = 'AVAILABLE', hold_id = NULL, order_id = NULL`, eventID); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, eventID := range events {
			_ = store.ResetEventForTests(context.Background(), eventID)
			_, _ = db.ExecContext(context.Background(), `DELETE FROM seat_inventory WHERE event_id = ?`, eventID)
			_, _ = db.ExecContext(context.Background(), `DELETE FROM seat_definitions WHERE event_id = ?`, eventID)
		}
	})

	for index, eventID := range events {
		hold, err := store.CreateHold(ctx, CreateHoldRequest{
			EventID: eventID, SubjectID: "cross-event-buyer", BookingID: "booking",
			IdempotencyKey: "cross-event", Mode: "specified", SeatIDs: []uint64{1},
		})
		if err != nil {
			t.Fatalf("event %d hold: %v", eventID, err)
		}
		if _, err := store.ConfirmHold(ctx, hold.ID, "cross-event-buyer", "payment-"+string(rune('a'+index))); err != nil {
			t.Fatalf("event %d confirm: %v", eventID, err)
		}
	}
	var orderCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM orders WHERE subject_id = 'cross-event-buyer' AND event_id IN (92001, 92002)`).Scan(&orderCount); err != nil {
		t.Fatal(err)
	}
	if orderCount != 2 {
		t.Fatalf("expected one order per event, got %d total", orderCount)
	}
}
