package domain

import (
	"time"

	"github.com/google/uuid"
)

// QueueEntry represents a user's position in the ticketing queue
type QueueEntry struct {
	ID        uuid.UUID  `json:"id"`
	EventID   uuid.UUID  `json:"event_id"`
	UserID    uuid.UUID  `json:"user_id"`
	Position  int        `json:"position"`
	Status    string     `json:"status"` // "waiting", "active", "expired", "completed"
	SessionID string     `json:"session_id"`
	EnteredAt time.Time  `json:"entered_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// QueueStatus represents the status of a queue entry
type QueueStatus string

const (
	QueueStatusWaiting   QueueStatus = "waiting"
	QueueStatusActive    QueueStatus = "active"
	QueueStatusExpired   QueueStatus = "expired"
	QueueStatusCompleted QueueStatus = "completed"
)

// IsWaiting checks if the queue entry is waiting
func (q *QueueEntry) IsWaiting() bool {
	return q.Status == string(QueueStatusWaiting)
}

// IsActive checks if the queue entry is active
func (q *QueueEntry) IsActive() bool {
	return q.Status == string(QueueStatusActive)
}

// IsExpired checks if the queue entry has expired
func (q *QueueEntry) IsExpired() bool {
	if q.ExpiresAt == nil {
		return false
	}
	return time.Now().After(*q.ExpiresAt)
}

// IsCompleted checks if the queue entry is completed
func (q *QueueEntry) IsCompleted() bool {
	return q.Status == string(QueueStatusCompleted)
}

// EstimatedWaitTime calculates estimated wait time based on position
func (q *QueueEntry) EstimatedWaitTime(avgProcessingTime time.Duration) time.Duration {
	if q.Position <= 0 {
		return 0
	}
	return time.Duration(q.Position) * avgProcessingTime
}
