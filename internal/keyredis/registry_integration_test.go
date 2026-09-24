//go:build integration

package keyredis

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/snowmerak/ticketing/internal/config"
	"github.com/snowmerak/ticketing/internal/ticket"
)

func TestPublicKeyRegistryIntegration(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(&redis.Options{Addr: cfg.TicketKeyRedisAddr, Password: cfg.TicketKeyRedisPassword})
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("ticket key Redis integration dependency is required: %v", err)
	}
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := ticket.KeyID(publicKey)
	key := prefix + id
	t.Cleanup(func() { _ = client.Del(context.Background(), key).Err() })
	registry := New(client)
	if err := registry.Publish(ctx, id, publicKey, time.Minute); err != nil {
		t.Fatal(err)
	}
	stored, err := registry.Lookup(ctx, id)
	if err != nil || !bytes.Equal(stored, publicKey) {
		t.Fatalf("published key lookup = %x, %v", stored, err)
	}
	ttl, err := client.PTTL(ctx, key).Result()
	if err != nil || ttl <= 0 || ttl > time.Minute {
		t.Fatalf("published key TTL = %v, %v", ttl, err)
	}
	if err := registry.Publish(ctx, id, publicKey, time.Hour); err != nil {
		t.Fatal(err)
	}
	unchangedTTL, err := client.PTTL(ctx, key).Result()
	if err != nil || unchangedTTL > ttl {
		t.Fatalf("duplicate publication extended expiry: %v -> %v, %v", ttl, unchangedTTL, err)
	}
	if _, err := registry.Lookup(ctx, ticket.KeyID(make([]byte, ed25519.PublicKeySize))); !errors.Is(err, ticket.ErrKeyNotFound) {
		t.Fatalf("missing key error = %v", err)
	}
	if err := client.Set(ctx, key, "corrupt", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Lookup(ctx, id); err == nil {
		t.Fatal("corrupt stored public key was accepted")
	}
}
