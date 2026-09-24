//go:build integration

package queue

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/snowmerak/ticketing/internal/config"
	"github.com/snowmerak/ticketing/internal/keyredis"
	"github.com/snowmerak/ticketing/internal/ticket"
)

func integrationSigner(t *testing.T, cfg config.Config) *ticket.Signer {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: cfg.TicketKeyRedisAddr, Password: cfg.TicketKeyRedisPassword})
	t.Cleanup(func() { _ = client.Close() })
	signer, err := ticket.NewSigner(keyredis.New(client), cfg.TicketKeyLifetime, cfg.TicketTTL)
	if err != nil {
		t.Fatalf("ticket key Redis integration dependency is required: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); signer.Run(ctx, cfg.DependencyTimeout, nil) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		checkCtx, checkCancel := context.WithTimeout(context.Background(), cfg.DependencyTimeout)
		err := signer.Ready(checkCtx)
		checkCancel()
		if err == nil {
			return signer
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("ticket signer did not become ready")
	return signer
}
