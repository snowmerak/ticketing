//go:build integration

package queue

import (
	"context"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/snowmerak/ticketing/internal/config"
	"github.com/snowmerak/ticketing/internal/keyredis"
	"github.com/snowmerak/ticketing/internal/ticket"
)

func integrationSigner(t *testing.T, cfg config.Config) *ticket.Signer {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: cfg.TicketKeyRedisAddr, Password: cfg.TicketKeyRedisPassword})
	t.Cleanup(func() { _ = client.Close() })
	signer, err := ticket.NewSigner(context.Background(), keyredis.New(client), cfg.TicketKeyLifetime, cfg.TicketTTL)
	if err != nil {
		t.Fatalf("ticket key Redis integration dependency is required: %v", err)
	}
	return signer
}
