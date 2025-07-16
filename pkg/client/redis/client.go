package redis

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/rueidis"
	"github.com/rs/zerolog"
	"github.com/snowmerak/ticketing/lib/adapter"
)

// Client represents a Redis client wrapper
type Client struct {
	rdb    rueidis.Client
	logger zerolog.Logger
}

// NewClient creates a new Redis client
func NewClient(addr, password string, db int, logger zerolog.Logger) *Client {
	client, err := rueidis.NewClient(rueidis.ClientOption{
		InitAddress: []string{addr},
		Password:    password,
		SelectDB:    db,
	})
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to create Redis client")
	}

	return &Client{
		rdb:    client,
		logger: logger,
	}
}

// Close closes the Redis connection
func (c *Client) Close() error {
	c.rdb.Close()
	return nil
}

// Ping pings the Redis server
func (c *Client) Ping(ctx context.Context) error {
	return c.rdb.Do(ctx, c.rdb.B().Ping().Build()).Error()
}

// GetRedisClient returns the underlying Redis client
func (c *Client) GetRedisClient() rueidis.Client {
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
	var cmd rueidis.Completed

	// Convert value to string
	var valueStr string
	switch v := value.(type) {
	case string:
		valueStr = v
	case []byte:
		valueStr = string(v)
	default:
		jsonBytes, err := json.Marshal(value)
		if err != nil {
			return err
		}
		valueStr = string(jsonBytes)
	}

	if expiration > 0 {
		cmd = c.client.rdb.B().Set().Key(key).Value(valueStr).Ex(expiration).Build()
	} else {
		cmd = c.client.rdb.B().Set().Key(key).Value(valueStr).Build()
	}

	return c.client.rdb.Do(ctx, cmd).Error()
}

// Get retrieves a value by key
func (c *Cache) Get(ctx context.Context, key string) (interface{}, error) {
	cmd := c.client.rdb.B().Get().Key(key).Build()
	result := c.client.rdb.Do(ctx, cmd)
	if result.Error() != nil {
		return nil, result.Error()
	}
	return result.ToString()
}

// Delete removes a key from cache
func (c *Cache) Delete(ctx context.Context, key string) error {
	cmd := c.client.rdb.B().Del().Key(key).Build()
	return c.client.rdb.Do(ctx, cmd).Error()
}

// Exists checks if a key exists in cache
func (c *Cache) Exists(ctx context.Context, key string) (bool, error) {
	cmd := c.client.rdb.B().Exists().Key(key).Build()
	result := c.client.rdb.Do(ctx, cmd)
	if result.Error() != nil {
		return false, result.Error()
	}
	count, err := result.ToInt64()
	return count > 0, err
}

// Expire sets expiration time for a key
func (c *Cache) Expire(ctx context.Context, key string, expiration time.Duration) error {
	cmd := c.client.rdb.B().Expire().Key(key).Seconds(int64(expiration.Seconds())).Build()
	return c.client.rdb.Do(ctx, cmd).Error()
}

// TTL returns the time to live for a key
func (c *Cache) TTL(ctx context.Context, key string) (time.Duration, error) {
	cmd := c.client.rdb.B().Ttl().Key(key).Build()
	result := c.client.rdb.Do(ctx, cmd)
	if result.Error() != nil {
		return 0, result.Error()
	}
	seconds, err := result.ToInt64()
	if err != nil {
		return 0, err
	}
	return time.Duration(seconds) * time.Second, nil
}
