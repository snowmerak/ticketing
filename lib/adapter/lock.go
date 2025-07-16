package adapter

import (
	"context"
	"time"
)

// Lock defines the interface for distributed locking operations
type Lock interface {
	// Acquire attempts to acquire a lock with a timeout
	Acquire(ctx context.Context, key string, expiration time.Duration) (bool, error)

	// Release releases a lock
	Release(ctx context.Context, key string) error

	// Extend extends the expiration time of a lock
	Extend(ctx context.Context, key string, expiration time.Duration) error

	// IsLocked checks if a key is locked
	IsLocked(ctx context.Context, key string) (bool, error)
}
