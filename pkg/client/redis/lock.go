package redis

import (
	"context"
	"time"

	"github.com/snowmerak/ticketing/lib/adapter"
)

// Lock implementation using Redis
type Lock struct {
	client *Client
}

// NewLock creates a new Lock implementation
func NewLock(client *Client) *Lock {
	return &Lock{
		client: client,
	}
}

// Compile-time check to ensure Lock implements adapter.Lock
var _ adapter.Lock = (*Lock)(nil)

// Acquire attempts to acquire a lock with a timeout
func (l *Lock) Acquire(ctx context.Context, key string, expiration time.Duration) (bool, error) {
	lockKey := "lock:" + key

	// Try to set the lock with NX (only if not exists) and EX (expiration)
	result, err := l.client.rdb.SetNX(ctx, lockKey, "1", expiration).Result()
	if err != nil {
		return false, err
	}

	return result, nil
}

// Release releases a lock
func (l *Lock) Release(ctx context.Context, key string) error {
	lockKey := "lock:" + key

	// Use Lua script to atomically check and delete the lock
	script := `
		if redis.call("GET", KEYS[1]) then
			return redis.call("DEL", KEYS[1])
		else
			return 0
		end
	`

	return l.client.rdb.Eval(ctx, script, []string{lockKey}).Err()
}

// Extend extends the expiration time of a lock
func (l *Lock) Extend(ctx context.Context, key string, expiration time.Duration) error {
	lockKey := "lock:" + key

	// Use Lua script to atomically check existence and extend expiration
	script := `
		if redis.call("GET", KEYS[1]) then
			return redis.call("EXPIRE", KEYS[1], ARGV[1])
		else
			return 0
		end
	`

	return l.client.rdb.Eval(ctx, script, []string{lockKey}, int(expiration.Seconds())).Err()
}

// IsLocked checks if a key is locked
func (l *Lock) IsLocked(ctx context.Context, key string) (bool, error) {
	lockKey := "lock:" + key

	result, err := l.client.rdb.Exists(ctx, lockKey).Result()
	if err != nil {
		return false, err
	}

	return result > 0, nil
}
