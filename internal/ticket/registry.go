package ticket

import (
	"context"
	"crypto/ed25519"
	"errors"
	"time"
)

var ErrKeyNotFound = errors.New("ticket verification key not found")

// Registry publishes immutable verification keys and resolves them by content ID.
// The implementation owns storage, transport, and key expiry details.
type Registry interface {
	Publish(ctx context.Context, keyID string, publicKey ed25519.PublicKey, retention time.Duration) error
	Lookup(ctx context.Context, keyID string) (ed25519.PublicKey, error)
}
