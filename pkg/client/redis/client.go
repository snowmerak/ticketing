package redis

import (
	"context"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/rs/zerolog"
	"github.com/snowmerak/ticketing/lib/adapter"
)

// Client represents a Redis client wrapper
type Client struct {
	rdb    *redis.Client
	logger zerolog.Logger
}

// NewClient creates a new Redis client
func NewClient(addr, password string, db int, logger zerolog.Logger) *Client {
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})

	return &Client{
		rdb:    rdb,
		logger: logger,
	}
}

// Close closes the Redis connection
func (c *Client) Close() error {
	return c.rdb.Close()
}

// Ping pings the Redis server
func (c *Client) Ping(ctx context.Context) error {
	return c.rdb.Ping(ctx).Err()
}

// GetRedisClient returns the underlying Redis client
func (c *Client) GetRedisClient() *redis.Client {
	return c.rdb
}

// Cache implementation
type Cache struct {
	client *Client
}

// NewCache creates a new Cache implementation
func NewCache(client *Client) *Cache {
	return &Cache{
		client: client,
	}
}

// Compile-time check to ensure Cache implements adapter.Cache
var _ adapter.Cache = (*Cache)(nil)

// Set stores a key-value pair with optional expiration
func (c *Cache) Set(ctx context.Context, key string, value interface{}, expiration time.Duration) error {
	return c.client.rdb.Set(ctx, key, value, expiration).Err()
}

// Get retrieves a value by key
func (c *Cache) Get(ctx context.Context, key string) (interface{}, error) {
	return c.client.rdb.Get(ctx, key).Result()
}

// Delete removes a key from cache
func (c *Cache) Delete(ctx context.Context, key string) error {
	return c.client.rdb.Del(ctx, key).Err()
}

// Exists checks if a key exists in cache
func (c *Cache) Exists(ctx context.Context, key string) (bool, error) {
	count, err := c.client.rdb.Exists(ctx, key).Result()
	return count > 0, err
}

// Expire sets expiration time for a key
func (c *Cache) Expire(ctx context.Context, key string, expiration time.Duration) error {
	return c.client.rdb.Expire(ctx, key, expiration).Err()
}

// TTL returns the time to live for a key
func (c *Cache) TTL(ctx context.Context, key string) (time.Duration, error) {
	return c.client.rdb.TTL(ctx, key).Result()
}
