package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/snowmerak/ticketing/lib/domain"
)

// QueueRepository defines the interface for queue data operations
type QueueRepository interface {
	// Join adds a user to the queue for an event
	Join(ctx context.Context, eventID, userID uuid.UUID, sessionID string) (*domain.QueueEntry, error)

	// GetPosition retrieves a user's position in the queue
	GetPosition(ctx context.Context, eventID, userID uuid.UUID) (*domain.QueueEntry, error)

	// GetBySessionID retrieves queue entry by session ID
	GetBySessionID(ctx context.Context, sessionID string) (*domain.QueueEntry, error)

	// GetNextInQueue retrieves the next user in queue for an event
	GetNextInQueue(ctx context.Context, eventID uuid.UUID) (*domain.QueueEntry, error)

	// GetQueueLength retrieves the current queue length for an event
	GetQueueLength(ctx context.Context, eventID uuid.UUID) (int, error)

	// UpdateStatus updates the status of a queue entry
	UpdateStatus(ctx context.Context, entryID uuid.UUID, status string) error

	// ActivateNext activates the next user in queue
	ActivateNext(ctx context.Context, eventID uuid.UUID) (*domain.QueueEntry, error)

	// RemoveFromQueue removes a user from the queue
	RemoveFromQueue(ctx context.Context, entryID uuid.UUID) error

	// GetActiveEntries retrieves all active queue entries for an event
	GetActiveEntries(ctx context.Context, eventID uuid.UUID) ([]*domain.QueueEntry, error)

	// GetExpiredEntries retrieves all expired queue entries
	GetExpiredEntries(ctx context.Context) ([]*domain.QueueEntry, error)

	// CleanupExpiredEntries removes expired entries from the queue
	CleanupExpiredEntries(ctx context.Context) error
}
