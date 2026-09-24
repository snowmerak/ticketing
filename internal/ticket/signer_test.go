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
}

func newMemoryRegistry() *memoryRegistry {
	return &memoryRegistry{keys: make(map[string]ed25519.PublicKey), retentions: make(map[string]time.Duration)}
}

func (r *memoryRegistry) Publish(_ context.Context, id string, key ed25519.PublicKey, retention time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
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
	signer, err := NewSigner(context.Background(), registry, time.Hour, 20*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return signer
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

func TestRotationKeepsPreviousPublicKeyForVerification(t *testing.T) {
	ctx := context.Background()
	registry := newMemoryRegistry()
	signer := testSigner(t, registry)
	now := time.Now()
	oldToken, oldClaims, err := signer.New(ctx, 100, "epoch", 7, "user-1", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	signer.mu.Lock()
	signer.current.signUntil = time.Now().Add(-time.Second)
	signer.mu.Unlock()
	newToken, newClaims, err := signer.Renew(ctx, oldClaims, now.Add(time.Second), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if oldClaims.KeyID == newClaims.KeyID || oldToken == newToken {
		t.Fatal("rotation did not replace the signing key")
	}
	if _, err := signer.Verify(ctx, oldToken, "user-1", 100, 100, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := signer.Verify(ctx, newToken, "user-1", 100, 100, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if registry.retentions[oldClaims.KeyID] != time.Hour+20*time.Minute+keyRetentionGrace {
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
	registry.fail = true
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

func TestConcurrentRotationPublishesOneReplacement(t *testing.T) {
	ctx := context.Background()
	registry := newMemoryRegistry()
	signer := testSigner(t, registry)
	signer.mu.Lock()
	signer.current.signUntil = time.Now().Add(-time.Second)
	signer.mu.Unlock()
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
	if len(registry.keys) != 2 {
		t.Fatalf("rotation published %d keys, want initial and one replacement", len(registry.keys))
	}
}
