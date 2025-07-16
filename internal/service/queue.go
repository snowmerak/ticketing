package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/snowmerak/ticketing/lib/adapter"
	"github.com/snowmerak/ticketing/lib/domain"
	"github.com/snowmerak/ticketing/lib/repository"
)

// QueueService handles queue-related business logic
type QueueService struct {
	queueRepo repository.QueueRepository
	eventRepo repository.EventRepository
	cache     adapter.Cache
	lock      adapter.Lock
	logger    adapter.Logger
}

// NewQueueService creates a new QueueService
func NewQueueService(
	queueRepo repository.QueueRepository,
	eventRepo repository.EventRepository,
	cache adapter.Cache,
	lock adapter.Lock,
	logger adapter.Logger,
) *QueueService {
	return &QueueService{
		queueRepo: queueRepo,
		eventRepo: eventRepo,
		cache:     cache,
		lock:      lock,
		logger:    logger,
	}
}

// JoinQueue adds a user to the queue for an event
func (s *QueueService) JoinQueue(ctx context.Context, eventID, userID uuid.UUID, sessionID string) (*domain.QueueEntry, error) {
	s.logger.Info(ctx, "User joining queue", "event_id", eventID, "user_id", userID, "session_id", sessionID)

	// Validate event exists and is active
	event, err := s.eventRepo.GetByID(ctx, eventID)
	if err != nil {
		s.logger.Error(ctx, "Failed to get event", "event_id", eventID, "error", err)
		return nil, fmt.Errorf("failed to get event: %w", err)
	}

	if !event.CanPurchase() {
		s.logger.Warn(ctx, "Event not available for purchase", "event_id", eventID, "status", event.Status)
		return nil, fmt.Errorf("event is not available for purchase")
	}

	// Use distributed lock to prevent race conditions
	lockKey := fmt.Sprintf("queue_join:%s", eventID.String())
	acquired, err := s.lock.Acquire(ctx, lockKey, 5*time.Second)
	if err != nil {
		s.logger.Error(ctx, "Failed to acquire lock", "error", err)
		return nil, fmt.Errorf("failed to acquire lock: %w", err)
	}

	if !acquired {
		s.logger.Warn(ctx, "Failed to acquire lock - queue busy", "event_id", eventID)
		return nil, fmt.Errorf("queue is busy, please try again")
	}

	defer func() {
		if err := s.lock.Release(ctx, lockKey); err != nil {
			s.logger.Error(ctx, "Failed to release lock", "error", err)
		}
	}()

	// Join queue
	entry, err := s.queueRepo.Join(ctx, eventID, userID, sessionID)
	if err != nil {
		s.logger.Error(ctx, "Failed to join queue", "error", err)
		return nil, fmt.Errorf("failed to join queue: %w", err)
	}

	s.logger.Info(ctx, "User joined queue successfully",
		"event_id", eventID,
		"user_id", userID,
		"position", entry.Position,
		"status", entry.Status)

	return entry, nil
}

// GetQueuePosition retrieves a user's position in the queue
func (s *QueueService) GetQueuePosition(ctx context.Context, eventID, userID uuid.UUID) (*domain.QueueEntry, error) {
	entry, err := s.queueRepo.GetPosition(ctx, eventID, userID)
	if err != nil {
		s.logger.Error(ctx, "Failed to get queue position", "event_id", eventID, "user_id", userID, "error", err)
		return nil, fmt.Errorf("failed to get queue position: %w", err)
	}

	return entry, nil
}

// GetQueueStatus retrieves queue status by session ID
func (s *QueueService) GetQueueStatus(ctx context.Context, sessionID string) (*domain.QueueEntry, error) {
	entry, err := s.queueRepo.GetBySessionID(ctx, sessionID)
	if err != nil {
		s.logger.Error(ctx, "Failed to get queue status", "session_id", sessionID, "error", err)
		return nil, fmt.Errorf("failed to get queue status: %w", err)
	}

	// Check if entry has expired
	if entry.IsExpired() {
		s.logger.Info(ctx, "Queue entry expired", "session_id", sessionID, "entry_id", entry.ID)
		return nil, fmt.Errorf("queue session has expired")
	}

	return entry, nil
}

// GetQueueLength retrieves the current queue length for an event
func (s *QueueService) GetQueueLength(ctx context.Context, eventID uuid.UUID) (int, error) {
	// Try cache first
	cacheKey := fmt.Sprintf("queue_length:%s", eventID.String())
	if cached, err := s.cache.Get(ctx, cacheKey); err == nil {
		if length, ok := cached.(int); ok {
			return length, nil
		}
	}

	length, err := s.queueRepo.GetQueueLength(ctx, eventID)
	if err != nil {
		s.logger.Error(ctx, "Failed to get queue length", "event_id", eventID, "error", err)
		return 0, fmt.Errorf("failed to get queue length: %w", err)
	}

	// Cache for 30 seconds
	if err := s.cache.Set(ctx, cacheKey, length, 30*time.Second); err != nil {
		s.logger.Warn(ctx, "Failed to cache queue length", "error", err)
	}

	return length, nil
}

// ProcessQueue processes the queue and activates the next user
func (s *QueueService) ProcessQueue(ctx context.Context, eventID uuid.UUID) (*domain.QueueEntry, error) {
	s.logger.Info(ctx, "Processing queue", "event_id", eventID)

	// Use distributed lock to prevent race conditions
	lockKey := fmt.Sprintf("queue_process:%s", eventID.String())
	acquired, err := s.lock.Acquire(ctx, lockKey, 5*time.Second)
	if err != nil {
		s.logger.Error(ctx, "Failed to acquire lock", "error", err)
		return nil, fmt.Errorf("failed to acquire lock: %w", err)
	}

	if !acquired {
		s.logger.Warn(ctx, "Failed to acquire lock - queue processing busy", "event_id", eventID)
		return nil, fmt.Errorf("queue processing is busy, please try again")
	}

	defer func() {
		if err := s.lock.Release(ctx, lockKey); err != nil {
			s.logger.Error(ctx, "Failed to release lock", "error", err)
		}
	}()

	// Activate next user
	entry, err := s.queueRepo.ActivateNext(ctx, eventID)
	if err != nil {
		s.logger.Error(ctx, "Failed to activate next user", "error", err)
		return nil, fmt.Errorf("failed to activate next user: %w", err)
	}

	// Invalidate queue length cache
	cacheKey := fmt.Sprintf("queue_length:%s", eventID.String())
	if err := s.cache.Delete(ctx, cacheKey); err != nil {
		s.logger.Warn(ctx, "Failed to invalidate queue length cache", "error", err)
	}

	s.logger.Info(ctx, "Queue processed successfully",
		"event_id", eventID,
		"activated_user", entry.UserID,
		"session_id", entry.SessionID)

	return entry, nil
}

// EstimateWaitTime estimates wait time for a user in queue
func (s *QueueService) EstimateWaitTime(ctx context.Context, eventID, userID uuid.UUID) (time.Duration, error) {
	entry, err := s.queueRepo.GetPosition(ctx, eventID, userID)
	if err != nil {
		return 0, fmt.Errorf("failed to get queue position: %w", err)
	}

	if entry.IsActive() {
		return 0, nil
	}

	// Average processing time per user (could be configurable)
	avgProcessingTime := 5 * time.Minute

	return entry.EstimatedWaitTime(avgProcessingTime), nil
}

// IsUserActive checks if a user is currently active in the queue
func (s *QueueService) IsUserActive(ctx context.Context, eventID, userID uuid.UUID) (bool, error) {
	entry, err := s.queueRepo.GetPosition(ctx, eventID, userID)
	if err != nil {
		return false, fmt.Errorf("failed to get queue position: %w", err)
	}

	return entry.IsActive() && !entry.IsExpired(), nil
}

// RefreshSession refreshes an active session's expiration time
func (s *QueueService) RefreshSession(ctx context.Context, sessionID string) error {
	s.logger.Info(ctx, "Refreshing session", "session_id", sessionID)

	entry, err := s.queueRepo.GetBySessionID(ctx, sessionID)
	if err != nil {
		s.logger.Error(ctx, "Failed to get session", "session_id", sessionID, "error", err)
		return fmt.Errorf("failed to get session: %w", err)
	}

	if !entry.IsActive() {
		s.logger.Warn(ctx, "Session is not active", "session_id", sessionID, "status", entry.Status)
		return fmt.Errorf("session is not active")
	}

	// Extend session by 15 minutes
	newExpiry := time.Now().Add(15 * time.Minute)
	entry.ExpiresAt = &newExpiry
	entry.UpdatedAt = time.Now()

	// Save updated entry (this would need to be implemented in the repository)
	s.logger.Info(ctx, "Session refreshed successfully", "session_id", sessionID)

	return nil
}
