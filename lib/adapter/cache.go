package adapter

import (
	"context"
	"time"
)

// Cache defines the interface for caching operations
type Cache interface {
	// Set stores a key-value pair with optional expiration
	Set(ctx context.Context, key string, value interface{}, expiration time.Duration) error

	// Get retrieves a value by key
	Get(ctx context.Context, key string) (interface{}, error)

	// Delete removes a key from cache
	Delete(ctx context.Context, key string) error

	// Exists checks if a key exists in cache
	Exists(ctx context.Context, key string) (bool, error)

	// Expire sets expiration time for a key
	Expire(ctx context.Context, key string, expiration time.Duration) error

	// TTL returns the time to live for a key
	TTL(ctx context.Context, key string) (time.Duration, error)
}
