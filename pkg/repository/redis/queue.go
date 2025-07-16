package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/snowmerak/ticketing/lib/domain"
	"github.com/snowmerak/ticketing/lib/repository"
	"github.com/snowmerak/ticketing/pkg/client/redis"
)

// QueueRepository implements repository.QueueRepository using Redis
type QueueRepository struct {
	client *redis.Client
}

// NewQueueRepository creates a new QueueRepository
func NewQueueRepository(client *redis.Client) *QueueRepository {
	return &QueueRepository{
		client: client,
	}
}

// Compile-time check to ensure QueueRepository implements repository.QueueRepository
var _ repository.QueueRepository = (*QueueRepository)(nil)

// Join adds a user to the queue for an event
func (r *QueueRepository) Join(ctx context.Context, eventID, userID uuid.UUID, sessionID string) (*domain.QueueEntry, error) {
	// Check if user is already in queue
	existing, err := r.GetPosition(ctx, eventID, userID)
	if err == nil && existing != nil {
		return existing, nil
	}

	queueKey := fmt.Sprintf("queue:%s", eventID.String())
	entryKey := fmt.Sprintf("queue_entry:%s:%s", eventID.String(), userID.String())

	// Get current queue length to determine position
	length, err := r.client.GetRedisClient().LLen(ctx, queueKey).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get queue length: %w", err)
	}

	entry := &domain.QueueEntry{
		ID:        uuid.New(),
		EventID:   eventID,
		UserID:    userID,
		Position:  int(length + 1),
		Status:    string(domain.QueueStatusWaiting),
		SessionID: sessionID,
		EnteredAt: time.Now(),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	// If this is the first person in queue, activate them immediately
	if length == 0 {
		entry.Status = string(domain.QueueStatusActive)
		// Set expiration for active session (15 minutes)
		expiry := time.Now().Add(15 * time.Minute)
		entry.ExpiresAt = &expiry
	}

	// Serialize entry
	data, err := json.Marshal(entry)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal queue entry: %w", err)
	}

	// Add to queue and store entry data
	pipe := r.client.GetRedisClient().Pipeline()
	pipe.RPush(ctx, queueKey, userID.String())
	pipe.Set(ctx, entryKey, data, 0)
	pipe.HSet(ctx, fmt.Sprintf("session:%s", sessionID), "queue_entry", entryKey)

	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("failed to join queue: %w", err)
	}

	return entry, nil
}

// GetPosition retrieves a user's position in the queue
func (r *QueueRepository) GetPosition(ctx context.Context, eventID, userID uuid.UUID) (*domain.QueueEntry, error) {
	entryKey := fmt.Sprintf("queue_entry:%s:%s", eventID.String(), userID.String())

	data, err := r.client.GetRedisClient().Get(ctx, entryKey).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get queue entry: %w", err)
	}

	var entry domain.QueueEntry
	if err := json.Unmarshal([]byte(data), &entry); err != nil {
		return nil, fmt.Errorf("failed to unmarshal queue entry: %w", err)
	}

	return &entry, nil
}

// GetBySessionID retrieves queue entry by session ID
func (r *QueueRepository) GetBySessionID(ctx context.Context, sessionID string) (*domain.QueueEntry, error) {
	entryKey, err := r.client.GetRedisClient().HGet(ctx, fmt.Sprintf("session:%s", sessionID), "queue_entry").Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get queue entry key: %w", err)
	}

	data, err := r.client.GetRedisClient().Get(ctx, entryKey).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get queue entry: %w", err)
	}

	var entry domain.QueueEntry
	if err := json.Unmarshal([]byte(data), &entry); err != nil {
		return nil, fmt.Errorf("failed to unmarshal queue entry: %w", err)
	}

	return &entry, nil
}

// GetNextInQueue retrieves the next user in queue for an event
func (r *QueueRepository) GetNextInQueue(ctx context.Context, eventID uuid.UUID) (*domain.QueueEntry, error) {
	queueKey := fmt.Sprintf("queue:%s", eventID.String())

	// Get the first user in queue
	userID, err := r.client.GetRedisClient().LIndex(ctx, queueKey, 0).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get next in queue: %w", err)
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse user ID: %w", err)
	}

	return r.GetPosition(ctx, eventID, userUUID)
}

// GetQueueLength retrieves the current queue length for an event
func (r *QueueRepository) GetQueueLength(ctx context.Context, eventID uuid.UUID) (int, error) {
	queueKey := fmt.Sprintf("queue:%s", eventID.String())

	length, err := r.client.GetRedisClient().LLen(ctx, queueKey).Result()
	if err != nil {
		return 0, fmt.Errorf("failed to get queue length: %w", err)
	}

	return int(length), nil
}

// UpdateStatus updates the status of a queue entry
func (r *QueueRepository) UpdateStatus(ctx context.Context, entryID uuid.UUID, status string) error {
	// We need to find the entry first
	// This is a simplified approach - in a real implementation, you might want to maintain an index
	return fmt.Errorf("not implemented - use specific methods like ActivateNext")
}

// ActivateNext activates the next user in queue
func (r *QueueRepository) ActivateNext(ctx context.Context, eventID uuid.UUID) (*domain.QueueEntry, error) {
	queueKey := fmt.Sprintf("queue:%s", eventID.String())

	// Remove the current first user and get the next one
	_, err := r.client.GetRedisClient().LPop(ctx, queueKey).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to remove current user from queue: %w", err)
	}

	// Get the new first user
	userID, err := r.client.GetRedisClient().LIndex(ctx, queueKey, 0).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get next user: %w", err)
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse user ID: %w", err)
	}

	// Get the entry and update it
	entry, err := r.GetPosition(ctx, eventID, userUUID)
	if err != nil {
		return nil, fmt.Errorf("failed to get queue entry: %w", err)
	}

	// Update status to active
	entry.Status = string(domain.QueueStatusActive)
	expiry := time.Now().Add(15 * time.Minute)
	entry.ExpiresAt = &expiry
	entry.UpdatedAt = time.Now()

	// Save the updated entry
	entryKey := fmt.Sprintf("queue_entry:%s:%s", eventID.String(), userUUID.String())
	data, err := json.Marshal(entry)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal queue entry: %w", err)
	}

	if err := r.client.GetRedisClient().Set(ctx, entryKey, data, 0).Err(); err != nil {
		return nil, fmt.Errorf("failed to update queue entry: %w", err)
	}

	return entry, nil
}

// RemoveFromQueue removes a user from the queue
func (r *QueueRepository) RemoveFromQueue(ctx context.Context, entryID uuid.UUID) error {
	// This is a simplified implementation
	// In a real scenario, you'd need to maintain better indexing
	return fmt.Errorf("not implemented - use session-based removal")
}

// GetActiveEntries retrieves all active queue entries for an event
func (r *QueueRepository) GetActiveEntries(ctx context.Context, eventID uuid.UUID) ([]*domain.QueueEntry, error) {
	// This would require scanning all entries - simplified implementation
	return nil, fmt.Errorf("not implemented")
}

// GetExpiredEntries retrieves all expired queue entries
func (r *QueueRepository) GetExpiredEntries(ctx context.Context) ([]*domain.QueueEntry, error) {
	// This would require scanning all entries - simplified implementation
	return nil, fmt.Errorf("not implemented")
}

// CleanupExpiredEntries removes expired entries from the queue
func (r *QueueRepository) CleanupExpiredEntries(ctx context.Context) error {
	// This would require scanning all entries - simplified implementation
	return fmt.Errorf("not implemented")
}
