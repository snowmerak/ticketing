//go:build integration

package queue

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/snowmerak/ticketing/internal/apierr"
	"github.com/snowmerak/ticketing/internal/config"
	"github.com/snowmerak/ticketing/internal/keyredis"
	"github.com/snowmerak/ticketing/internal/ticket"
)

func TestEntryWithoutRegisteredKeyPreservesDirectAdmissionAndQueueSequenceIntegration(t *testing.T) {
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.EventIDs = []uint64{91011}
	client := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword})
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("Redis integration dependency is required: %v", err)
	}
	cleanupEventKeys(t, ctx, client, cfg.EventIDs[0])
	t.Cleanup(func() { cleanupEventKeys(t, context.Background(), client, cfg.EventIDs[0]) })
	keyClient := redis.NewClient(&redis.Options{Addr: cfg.TicketKeyRedisAddr, Password: cfg.TicketKeyRedisPassword})
	t.Cleanup(func() { _ = keyClient.Close() })
	signer, err := ticket.NewSigner(keyredis.New(keyClient), cfg.TicketKeyLifetime, cfg.TicketTTL)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(client, signer, cfg)
	if err := store.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	direct, err := store.Entry(ctx, cfg.EventIDs[0], "direct-user", true)
	if err != nil || direct.Kind != "DIRECT" {
		t.Fatalf("direct entry without key: kind=%q err=%v", direct.Kind, err)
	}
	if err := client.HSet(ctx, store.controlKey(cfg.EventIDs[0]), "pause", 1).Err(); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		_, err := store.Entry(ctx, cfg.EventIDs[0], "queued-user", true)
		if apierr.As(err).Code != "TICKET_KEY_UNAVAILABLE" {
			t.Fatalf("queued entry without key = %v", err)
		}
	}
	active, err := store.ActiveEpoch(ctx, cfg.EventIDs[0])
	if err != nil || active != "" {
		t.Fatalf("failed issuance created queue epoch: epoch=%q err=%v", active, err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); signer.Run(runCtx, cfg.DependencyTimeout, nil) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if signer.Ready(ctx) == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := signer.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	queued, err := store.Entry(ctx, cfg.EventIDs[0], "queued-user", true)
	if err != nil || queued.Kind != "QUEUE" || queued.Claims.Seq != 0 {
		t.Fatalf("first queued entry after recovery: result=%+v err=%v", queued, err)
	}
}

func TestQ14MultipleTicketsAndRedeemIntegration(t *testing.T) {
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.EventIDs = []uint64{91001}
	cfg.PageBits = 64
	cfg.SlotWidth = 200 * time.Millisecond
	cfg.ReadyWindow = time.Second
	cfg.PollInterval = 100 * time.Millisecond
	cfg.GrantTTL = 3 * time.Second
	cfg.SchedulerLeaseTTL = time.Second
	cfg.Capacity = 1
	cfg.AdmissionRate = 1000
	cfg.AdmissionBurst = 1000

	client := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword})
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("Redis integration dependency is required: %v", err)
	}
	cleanupEventKeys(t, ctx, client, cfg.EventIDs[0])
	t.Cleanup(func() { cleanupEventKeys(t, context.Background(), client, cfg.EventIDs[0]) })

	signer := integrationSigner(t, cfg)
	store := NewStore(client, signer, cfg)
	if err := store.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.HSet(ctx, store.controlKey(cfg.EventIDs[0]), "pause", 1).Err(); err != nil {
		t.Fatal(err)
	}

	first, err := store.Entry(ctx, cfg.EventIDs[0], "same-user", true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Entry(ctx, cfg.EventIDs[0], "same-user", true)
	if err != nil {
		t.Fatal(err)
	}
	if first.Kind != "QUEUE" || second.Kind != "QUEUE" || first.Ticket == second.Ticket || first.Claims.Seq == second.Claims.Seq {
		t.Fatalf("same user must receive distinct queue tickets: first=%+v second=%+v", first.Claims, second.Claims)
	}
	const concurrentTickets = 64
	tickets := make(chan EntryResult, concurrentTickets)
	errors := make(chan error, concurrentTickets)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range concurrentTickets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			entry, err := store.Entry(ctx, cfg.EventIDs[0], "same-user", true)
			if err != nil {
				errors <- err
				return
			}
			tickets <- entry
		}()
	}
	close(start)
	wg.Wait()
	close(errors)
	close(tickets)
	for err := range errors {
		t.Fatalf("concurrent ticket issue failed: %v", err)
	}
	seenSeq := map[uint64]struct{}{first.Claims.Seq: {}, second.Claims.Seq: {}}
	seenTicketID := map[string]struct{}{first.Claims.TicketID: {}, second.Claims.TicketID: {}}
	for entry := range tickets {
		if entry.Kind != "QUEUE" {
			t.Fatalf("expected queued entry, got %+v", entry)
		}
		if _, duplicate := seenSeq[entry.Claims.Seq]; duplicate {
			t.Fatalf("duplicate sequence %d", entry.Claims.Seq)
		}
		if _, duplicate := seenTicketID[entry.Claims.TicketID]; duplicate {
			t.Fatalf("duplicate ticket id %s", entry.Claims.TicketID)
		}
		seenSeq[entry.Claims.Seq] = struct{}{}
		seenTicketID[entry.Claims.TicketID] = struct{}{}
	}
	if len(seenSeq) != concurrentTickets+2 || len(seenTicketID) != concurrentTickets+2 {
		t.Fatalf("expected %d unique tickets, got seq=%d ticket_id=%d", concurrentTickets+2, len(seenSeq), len(seenTicketID))
	}

	if err := client.HSet(ctx, store.controlKey(cfg.EventIDs[0]), "pause", 0).Err(); err != nil {
		t.Fatal(err)
	}
	heartbeat, err := store.Heartbeat(ctx, cfg.EventIDs[0], "same-user", first.Ticket)
	if err != nil {
		t.Fatal(err)
	}
	worker := NewWorker(store, cfg, "integration-worker", nil, slog.Default())
	if err := worker.scheduleEvent(ctx, cfg.EventIDs[0]); err != nil {
		t.Fatal(err)
	}
	granted, err := store.Heartbeat(ctx, cfg.EventIDs[0], "same-user", heartbeat.Ticket)
	if err != nil {
		t.Fatal(err)
	}
	if granted.Status != "GRANTED" || granted.GrantID == "" {
		t.Fatalf("expected grant, got %+v", granted)
	}
	booking, err := store.Redeem(ctx, cfg.EventIDs[0], "same-user", granted.Ticket, granted.GrantID)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := store.Redeem(ctx, cfg.EventIDs[0], "same-user", granted.Ticket, granted.GrantID)
	if err != nil {
		t.Fatal(err)
	}
	if booking.ID == "" || replayed.ID != booking.ID {
		t.Fatalf("redeem must be idempotent: first=%+v replay=%+v", booking, replayed)
	}

	third, err := store.Entry(ctx, cfg.EventIDs[0], "same-user", true)
	if err != nil {
		t.Fatal(err)
	}
	if third.Kind != "QUEUE" || third.Claims.Seq == first.Claims.Seq || third.Claims.Seq == second.Claims.Seq {
		t.Fatalf("an admitted user may still obtain another queue ticket: %+v", third)
	}
}

func TestA06AdmissionCapacityInvariantIntegration(t *testing.T) {
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.EventIDs = []uint64{91002}
	cfg.PageBits = 64
	cfg.SlotWidth = 200 * time.Millisecond
	cfg.ReadyWindow = time.Second
	cfg.PollInterval = 100 * time.Millisecond
	cfg.GrantTTL = 3 * time.Second
	cfg.SchedulerLeaseTTL = time.Second
	cfg.Capacity = 8
	cfg.AdmissionRate = 1000
	cfg.AdmissionBurst = 1000
	cfg.MaxGrantsPerTick = 100

	client := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword})
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("Redis integration dependency is required: %v", err)
	}
	cleanupEventKeys(t, ctx, client, cfg.EventIDs[0])
	t.Cleanup(func() { cleanupEventKeys(t, context.Background(), client, cfg.EventIDs[0]) })
	signer := integrationSigner(t, cfg)
	store := NewStore(client, signer, cfg)
	if err := store.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.HSet(ctx, store.controlKey(cfg.EventIDs[0]), "pause", 1).Err(); err != nil {
		t.Fatal(err)
	}

	const waiters = 32
	tokens := make(map[uint64]string, waiters)
	var epoch string
	for index := 0; index < waiters; index++ {
		entry, err := store.Entry(ctx, cfg.EventIDs[0], "capacity-user-"+strconv.Itoa(index), true)
		if err != nil {
			t.Fatal(err)
		}
		tokens[entry.Claims.Seq] = entry.Ticket
		epoch = entry.Claims.Epoch
		if _, err := store.Heartbeat(ctx, cfg.EventIDs[0], "capacity-user-"+strconv.Itoa(index), entry.Ticket); err != nil {
			t.Fatal(err)
		}
	}
	if err := client.HSet(ctx, store.controlKey(cfg.EventIDs[0]), "pause", 0).Err(); err != nil {
		t.Fatal(err)
	}
	fence, acquired, err := store.AcquireLease(ctx, cfg.EventIDs[0], "capacity-worker")
	if err != nil || !acquired {
		t.Fatalf("acquire lease: acquired=%v err=%v", acquired, err)
	}
	candidates, err := store.Candidates(ctx, cfg.EventIDs[0], epoch, waiters)
	if err != nil {
		t.Fatal(err)
	}
	grants := make(chan Grant, len(candidates))
	var wg sync.WaitGroup
	for _, seq := range candidates {
		wg.Add(1)
		go func(seq uint64) {
			defer wg.Done()
			grant, ok, err := store.Claim(ctx, cfg.EventIDs[0], epoch, "capacity-worker", fence, seq)
			if err != nil {
				t.Errorf("claim %d: %v", seq, err)
				return
			}
			if ok {
				grants <- grant
			}
		}(seq)
	}
	wg.Wait()
	close(grants)
	claimed := make([]Grant, 0, cfg.Capacity)
	for grant := range grants {
		claimed = append(claimed, grant)
	}
	if int64(len(claimed)) != cfg.Capacity {
		t.Fatalf("capacity must cap reservations at %d, got %d", cfg.Capacity, len(claimed))
	}
	assertCapacity(t, ctx, client, store.controlKey(cfg.EventIDs[0]), cfg.Capacity, 0, cfg.Capacity)

	bookings := make([]Booking, 0, len(claimed))
	for _, grant := range claimed {
		subject := "capacity-user-" + strconv.FormatUint(grant.Seq, 10)
		booking, err := store.Redeem(ctx, cfg.EventIDs[0], subject, tokens[grant.Seq], grant.ID)
		if err != nil {
			t.Fatal(err)
		}
		bookings = append(bookings, booking)
	}
	assertCapacity(t, ctx, client, store.controlKey(cfg.EventIDs[0]), cfg.Capacity, cfg.Capacity, 0)
	for index, grant := range claimed {
		subject := "capacity-user-" + strconv.FormatUint(grant.Seq, 10)
		replayed, err := store.Redeem(ctx, cfg.EventIDs[0], subject, tokens[grant.Seq], grant.ID)
		if err != nil || replayed.ID != bookings[index].ID {
			t.Fatalf("redeem replay changed booking: got=%+v err=%v", replayed, err)
		}
	}
	assertCapacity(t, ctx, client, store.controlKey(cfg.EventIDs[0]), cfg.Capacity, cfg.Capacity, 0)
	for index, booking := range bookings {
		if err := store.LeaveBooking(ctx, cfg.EventIDs[0], "capacity-user-"+strconv.FormatUint(claimed[index].Seq, 10), booking.ID); err != nil {
			t.Fatal(err)
		}
	}
	assertCapacity(t, ctx, client, store.controlKey(cfg.EventIDs[0]), cfg.Capacity, 0, 0)
}

func TestQ13ClosedEpochStateGetsBoundedCleanupTTLIntegration(t *testing.T) {
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.EventIDs = []uint64{91003}
	client := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword})
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("Redis integration dependency is required: %v", err)
	}
	cleanupEventKeys(t, ctx, client, cfg.EventIDs[0])
	t.Cleanup(func() { cleanupEventKeys(t, context.Background(), client, cfg.EventIDs[0]) })
	signer := integrationSigner(t, cfg)
	store := NewStore(client, signer, cfg)
	if err := store.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.HSet(ctx, store.controlKey(cfg.EventIDs[0]), "pause", 1).Err(); err != nil {
		t.Fatal(err)
	}
	entry, err := store.Entry(ctx, cfg.EventIDs[0], "closing-user", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.HSet(ctx, store.metaKey(cfg.EventIDs[0], entry.Claims.Epoch), "max_ticket_expiry", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.Sweep(ctx, cfg.EventIDs[0], 100); err != nil {
		t.Fatal(err)
	}
	active, err := store.ActiveEpoch(ctx, cfg.EventIDs[0])
	if err != nil || active != "" {
		t.Fatalf("closed epoch remained active: epoch=%q err=%v", active, err)
	}
	ttl, err := client.PTTL(ctx, store.metaKey(cfg.EventIDs[0], entry.Claims.Epoch)).Result()
	if err != nil || ttl <= 0 || ttl > time.Minute {
		t.Fatalf("closed epoch meta has no cleanup TTL: ttl=%v err=%v", ttl, err)
	}
}

func TestA12StaleSchedulerFenceCannotClaimIntegration(t *testing.T) {
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.EventIDs = []uint64{91004}
	cfg.PageBits = 64
	cfg.SlotWidth = 200 * time.Millisecond
	cfg.ReadyWindow = time.Second
	cfg.Capacity = 1
	cfg.AdmissionRate = 1000
	cfg.AdmissionBurst = 1000
	client := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword})
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("Redis integration dependency is required: %v", err)
	}
	cleanupEventKeys(t, ctx, client, cfg.EventIDs[0])
	t.Cleanup(func() { cleanupEventKeys(t, context.Background(), client, cfg.EventIDs[0]) })
	signer := integrationSigner(t, cfg)
	store := NewStore(client, signer, cfg)
	if err := store.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.HSet(ctx, store.controlKey(cfg.EventIDs[0]), "pause", 1).Err(); err != nil {
		t.Fatal(err)
	}
	entry, err := store.Entry(ctx, cfg.EventIDs[0], "fenced-user", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Heartbeat(ctx, cfg.EventIDs[0], "fenced-user", entry.Ticket); err != nil {
		t.Fatal(err)
	}
	if err := client.HSet(ctx, store.controlKey(cfg.EventIDs[0]), "pause", 0).Err(); err != nil {
		t.Fatal(err)
	}
	oldFence, acquired, err := store.AcquireLease(ctx, cfg.EventIDs[0], "old-worker")
	if err != nil || !acquired {
		t.Fatalf("old lease: acquired=%v err=%v", acquired, err)
	}
	if err := client.HSet(ctx, store.controlKey(cfg.EventIDs[0]), "writer_until_ms", 0).Err(); err != nil {
		t.Fatal(err)
	}
	newFence, acquired, err := store.AcquireLease(ctx, cfg.EventIDs[0], "new-worker")
	if err != nil || !acquired || newFence <= oldFence {
		t.Fatalf("new lease did not advance fence: old=%d new=%d acquired=%v err=%v", oldFence, newFence, acquired, err)
	}
	if _, ok, err := store.Claim(ctx, cfg.EventIDs[0], entry.Claims.Epoch, "old-worker", oldFence, entry.Claims.Seq); err != nil || ok {
		t.Fatalf("stale writer claimed: ok=%v err=%v", ok, err)
	}
	if _, ok, err := store.Claim(ctx, cfg.EventIDs[0], entry.Claims.Epoch, "new-worker", newFence, entry.Claims.Seq); err != nil || !ok {
		t.Fatalf("current writer could not claim: ok=%v err=%v", ok, err)
	}
}

func TestF03MissingActiveEpochFailsRecoveringIntegration(t *testing.T) {
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.EventIDs = []uint64{91005}
	client := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword})
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("Redis integration dependency is required: %v", err)
	}
	cleanupEventKeys(t, ctx, client, cfg.EventIDs[0])
	t.Cleanup(func() { cleanupEventKeys(t, context.Background(), client, cfg.EventIDs[0]) })
	signer := integrationSigner(t, cfg)
	store := NewStore(client, signer, cfg)
	if err := store.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.HSet(ctx, store.controlKey(cfg.EventIDs[0]), "mode", "QUEUE", "expected_epoch", "lost-epoch").Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Entry(ctx, cfg.EventIDs[0], "recovery-user", true); err == nil || apierr.As(err).Code != "QUEUE_RECOVERING" {
		t.Fatalf("missing active epoch must fail closed, got %v", err)
	}
	mode, err := client.HGet(ctx, store.controlKey(cfg.EventIDs[0]), "mode").Result()
	if err != nil || mode != "RECOVERING" {
		t.Fatalf("event did not latch recovering: mode=%q err=%v", mode, err)
	}
}

func assertCapacity(t *testing.T, ctx context.Context, client *redis.Client, controlKey string, capacity, active, reserved int64) {
	t.Helper()
	values, err := client.HMGet(ctx, controlKey, "active_count", "reserved_count").Result()
	if err != nil {
		t.Fatal(err)
	}
	actualActive, _ := strconv.ParseInt(values[0].(string), 10, 64)
	actualReserved, _ := strconv.ParseInt(values[1].(string), 10, 64)
	if actualActive != active || actualReserved != reserved || actualActive+actualReserved > capacity {
		t.Fatalf("capacity invariant: active=%d reserved=%d capacity=%d", actualActive, actualReserved, capacity)
	}
}

func cleanupEventKeys(t *testing.T, ctx context.Context, client *redis.Client, eventID uint64) {
	t.Helper()
	pattern := "q:{" + strconv.FormatUint(eventID, 10) + "}:*"
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
