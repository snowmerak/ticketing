package ticket

import (
	"context"
	"crypto/ed25519"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/snowmerak/ticketing/internal/apierr"
)

type memoryRegistry struct {
	mu         sync.Mutex
	keys       map[string]ed25519.PublicKey
	retentions map[string]time.Duration
	fail       bool
	attempts   []string
}

type blockingLookupRegistry struct {
	Registry
	entered chan struct{}
	release chan struct{}
}

func (r *blockingLookupRegistry) Lookup(ctx context.Context, id string) (ed25519.PublicKey, error) {
	close(r.entered)
	<-r.release
	return r.Registry.Lookup(ctx, id)
}

func newMemoryRegistry() *memoryRegistry {
	return &memoryRegistry{keys: make(map[string]ed25519.PublicKey), retentions: make(map[string]time.Duration)}
}

func (r *memoryRegistry) Publish(_ context.Context, id string, key ed25519.PublicKey, retention time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts = append(r.attempts, id)
	if r.fail {
		return errors.New("registry down")
	}
	if old, ok := r.keys[id]; ok && string(old) != string(key) {
		return errors.New("collision")
	}
	r.keys[id] = append(ed25519.PublicKey(nil), key...)
	r.retentions[id] = retention
	return nil
}

func (r *memoryRegistry) Lookup(_ context.Context, id string) (ed25519.PublicKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return nil, errors.New("registry down")
	}
	key, ok := r.keys[id]
	if !ok {
		return nil, ErrKeyNotFound
	}
	return append(ed25519.PublicKey(nil), key...), nil
}

func testSigner(t *testing.T, registry Registry) *Signer {
	t.Helper()
	signer, err := NewSigner(registry, time.Hour, 20*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		signer.Run(ctx, 50*time.Millisecond, nil)
	}()
	t.Cleanup(func() { cancel(); <-done; signer.Close() })
	waitReady(t, signer)
	return signer
}

func waitReady(t *testing.T, signer *Signer) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		err := signer.Ready(ctx)
		cancel()
		if err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("signer did not become ready")
}

func (r *memoryRegistry) setFail(value bool) {
	r.mu.Lock()
	r.fail = value
	r.mu.Unlock()
}

func TestTicketRoundTripAndBindingAcrossInstances(t *testing.T) {
	ctx := context.Background()
	registry := newMemoryRegistry()
	issuer := testSigner(t, registry)
	verifier := testSigner(t, registry)
	now := time.Now()
	token, want, err := issuer.New(ctx, 100, "epoch", 7, "user-1", now, 20*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	got, err := verifier.Verify(ctx, token, "user-1", 100, 100, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if got.TicketID != want.TicketID || got.Seq != 7 || got.KeyID != want.KeyID || got.Version != Version {
		t.Fatalf("claims = %#v, want ticket %s seq 7", got, want.TicketID)
	}
	if _, err := verifier.Verify(ctx, token, "other", 100, 100, now); !errors.Is(err, apierr.ErrTicketInvalid) {
		t.Fatalf("wrong subject error = %v", err)
	}
	if _, err := verifier.Verify(ctx, token, "user-1", 101, 100, now); !errors.Is(err, apierr.ErrTicketInvalid) {
		t.Fatalf("wrong event error = %v", err)
	}
}

func TestTicketRejectsTamperExpiryAndLargeSeq(t *testing.T) {
	ctx := context.Background()
	signer := testSigner(t, newMemoryRegistry())
	now := time.Now()
	token, _, err := signer.New(ctx, 100, "epoch", 101, "user-1", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{token + "x", string(make([]byte, maxTokenBytes+1))} {
		if _, err := signer.Verify(ctx, bad, "user-1", 100, 1000, now); !errors.Is(err, apierr.ErrTicketInvalid) {
			t.Fatalf("tamper error = %v", err)
		}
	}
	if _, err := signer.Verify(ctx, token, "user-1", 100, 100, now); !errors.Is(err, apierr.ErrTicketInvalid) {
		t.Fatalf("large seq error = %v", err)
	}
	if _, err := signer.Verify(ctx, token, "user-1", 100, 1000, now.Add(time.Minute)); !errors.Is(err, apierr.ErrTicketExpired) {
		t.Fatalf("expiry error = %v", err)
	}
}

func TestBackgroundRotationKeepsPreviousPublicKeyForVerification(t *testing.T) {
	ctx := context.Background()
	registry := newMemoryRegistry()
	signer, err := NewSigner(registry, 200*time.Millisecond, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); signer.Run(runCtx, 50*time.Millisecond, nil) }()
	t.Cleanup(func() { cancel(); <-done; signer.Close() })
	waitReady(t, signer)
	oldKey := signer.currentKey()
	now := time.Now()
	oldToken, oldClaims, err := signer.New(ctx, 100, "epoch", 7, "user-1", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var newToken string
	var newClaims Claims
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		newToken, newClaims, err = signer.Renew(ctx, oldClaims, time.Now(), time.Minute)
		if err == nil && newClaims.KeyID != oldClaims.KeyID {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if oldClaims.KeyID == newClaims.KeyID || oldToken == newToken {
		t.Fatal("rotation did not replace the signing key")
	}
	if oldKey.private.IsAlive() {
		t.Fatal("retired private key was not destroyed")
	}
	if _, err := signer.Verify(ctx, oldToken, "user-1", 100, 100, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := signer.Verify(ctx, newToken, "user-1", 100, 100, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if registry.retentions[oldClaims.KeyID] < time.Minute+keyRetentionGrace || registry.retentions[oldClaims.KeyID] > 200*time.Millisecond+time.Minute+keyRetentionGrace {
		t.Fatal("old verification key retention does not cover signing and ticket lifetimes")
	}
}

func TestRegistryOutageFailsClosed(t *testing.T) {
	ctx := context.Background()
	registry := newMemoryRegistry()
	signer := testSigner(t, registry)
	now := time.Now()
	token, _, err := signer.New(ctx, 100, "epoch", 1, "user-1", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	registry.setFail(true)
	if _, err := signer.Verify(ctx, token, "user-1", 100, 100, now); apierr.As(err).Code != "TICKET_KEY_UNAVAILABLE" {
		t.Fatalf("verification outage error = %v", err)
	}
	if _, _, err := signer.New(ctx, 100, "epoch", 2, "user-1", now, time.Minute); apierr.As(err).Code != "TICKET_KEY_UNAVAILABLE" {
		t.Fatalf("issuance outage error = %v", err)
	}
	if err := signer.Ready(ctx); apierr.As(err).Code != "TICKET_KEY_UNAVAILABLE" {
		t.Fatalf("readiness outage error = %v", err)
	}
}

func TestReadinessAndSigningNeverPublish(t *testing.T) {
	ctx := context.Background()
	registry := newMemoryRegistry()
	signer, err := NewSigner(registry, time.Hour, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(signer.Close)
	for range 3 {
		if err := signer.Ready(ctx); apierr.As(err).Code != "TICKET_KEY_UNAVAILABLE" {
			t.Fatalf("unregistered readiness error = %v", err)
		}
		if _, _, err := signer.New(ctx, 100, "epoch", 1, "user-1", time.Now(), time.Minute); apierr.As(err).Code != "TICKET_KEY_UNAVAILABLE" {
			t.Fatalf("unregistered issuance error = %v", err)
		}
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if len(registry.attempts) != 0 {
		t.Fatalf("requests published %d keys", len(registry.attempts))
	}
}

func TestExpiredKeyFailsClosedWithoutRequestTriggeredRotation(t *testing.T) {
	ctx := context.Background()
	registry := newMemoryRegistry()
	signer, err := NewSigner(registry, time.Hour, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(signer.Close)
	key, err := signer.generateKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Publish(ctx, key.id, key.public, time.Hour+time.Minute+keyRetentionGrace); err != nil {
		t.Fatal(err)
	}
	key.signUntil = time.Now().Add(-time.Second)
	signer.current = key
	if err := signer.Ready(ctx); apierr.As(err).Code != "TICKET_KEY_UNAVAILABLE" {
		t.Fatalf("expired readiness error = %v", err)
	}
	if _, _, err := signer.New(ctx, 100, "epoch", 1, "user-1", time.Now(), time.Minute); apierr.As(err).Code != "TICKET_KEY_UNAVAILABLE" {
		t.Fatalf("expired issuance error = %v", err)
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if len(registry.attempts) != 1 {
		t.Fatalf("expired requests triggered %d publications, want only test setup", len(registry.attempts))
	}
}

func TestCloseWipesActivePrivateKey(t *testing.T) {
	signer, err := NewSigner(newMemoryRegistry(), time.Hour, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	key, err := signer.generateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer.current = key
	signer.Close()
	signer.Close()
	if key.private.IsAlive() || signer.currentKey() != nil {
		t.Fatal("Close kept the active private key")
	}
	if _, _, err := signer.New(context.Background(), 100, "epoch", 1, "user-1", time.Now(), time.Minute); apierr.As(err).Code != "TICKET_KEY_UNAVAILABLE" {
		t.Fatalf("issuance after Close = %v", err)
	}
}

func TestCloseWaitsForInFlightSigning(t *testing.T) {
	base := newMemoryRegistry()
	registry := &blockingLookupRegistry{Registry: base, entered: make(chan struct{}), release: make(chan struct{})}
	released := false
	defer func() {
		if !released {
			close(registry.release)
		}
	}()
	signer, err := NewSigner(registry, time.Hour, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	key, err := signer.generateKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := base.Publish(context.Background(), key.id, key.public, time.Hour+time.Minute+keyRetentionGrace); err != nil {
		t.Fatal(err)
	}
	signer.current = key
	signed := make(chan error, 1)
	go func() {
		_, _, err := signer.New(context.Background(), 100, "epoch", 1, "user-1", time.Now(), time.Minute)
		signed <- err
	}()
	<-registry.entered
	closed := make(chan struct{})
	go func() { signer.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("Close destroyed the key while signing was in flight")
	case <-time.After(50 * time.Millisecond):
	}
	close(registry.release)
	released = true
	select {
	case err := <-signed:
		if err != nil {
			t.Fatalf("in-flight signing failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight signing did not finish")
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not finish after signing")
	}
	if key.private.IsAlive() {
		t.Fatal("Close did not destroy the private key")
	}
}

func TestRegistrationRetriesSameKeyAndRecovers(t *testing.T) {
	registry := newMemoryRegistry()
	registry.setFail(true)
	signer, err := NewSigner(registry, time.Hour, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); signer.Run(ctx, 50*time.Millisecond, nil) }()
	t.Cleanup(func() { cancel(); <-done; signer.Close() })
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		registry.mu.Lock()
		attempts := append([]string(nil), registry.attempts...)
		registry.mu.Unlock()
		if len(attempts) >= 2 {
			if attempts[0] != attempts[1] {
				t.Fatalf("registration retry changed key ID: %v", attempts)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !time.Now().Before(deadline) {
		t.Fatal("registration was not retried")
	}
	if _, _, err := signer.New(context.Background(), 100, "epoch", 1, "user-1", time.Now(), time.Minute); apierr.As(err).Code != "TICKET_KEY_UNAVAILABLE" {
		t.Fatalf("issuance during registration failure = %v", err)
	}
	registry.setFail(false)
	waitReady(t, signer)
	if _, _, err := signer.New(context.Background(), 100, "epoch", 1, "user-1", time.Now(), time.Minute); err != nil {
		t.Fatalf("issuance after recovery: %v", err)
	}
}

func TestBackgroundRestoresLostActivePublicKey(t *testing.T) {
	registry := newMemoryRegistry()
	signer := testSigner(t, registry)
	keyID := signer.currentKey().id
	registry.mu.Lock()
	delete(registry.keys, keyID)
	registry.mu.Unlock()
	if err := signer.Ready(context.Background()); apierr.As(err).Code != "TICKET_KEY_UNAVAILABLE" {
		t.Fatalf("readiness after key loss = %v", err)
	}
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if signer.Ready(context.Background()) == nil {
			registry.mu.Lock()
			attempts := append([]string(nil), registry.attempts...)
			registry.mu.Unlock()
			if len(attempts) != 2 || attempts[1] != keyID {
				t.Fatalf("re-registration used a different key: %v", attempts)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("background worker did not restore the active public key")
}

func TestConcurrentIssuanceUsesOnePublishedKey(t *testing.T) {
	ctx := context.Background()
	registry := newMemoryRegistry()
	signer := testSigner(t, registry)
	const issuers = 32
	ids := make(chan string, issuers)
	errs := make(chan error, issuers)
	var workers sync.WaitGroup
	for range issuers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, claims, err := signer.New(ctx, 100, "epoch", 1, "user-1", time.Now(), time.Minute)
			if err != nil {
				errs <- err
				return
			}
			ids <- claims.KeyID
		}()
	}
	workers.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var replacement string
	for id := range ids {
		if replacement == "" {
			replacement = id
		} else if id != replacement {
			t.Fatalf("concurrent issue used multiple replacement keys: %s and %s", replacement, id)
		}
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if len(registry.keys) != 1 {
		t.Fatalf("issuance published %d keys, want only background key", len(registry.keys))
	}
}
