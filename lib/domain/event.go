package domain

import (
	"time"

	"github.com/google/uuid"
)

// Event represents a ticketing event
type Event struct {
	ID               uuid.UUID `json:"id"`
	Name             string    `json:"name"`
	Description      string    `json:"description"`
	StartTime        time.Time `json:"start_time"`
	EndTime          time.Time `json:"end_time"`
	Venue            string    `json:"venue"`
	Status           string    `json:"status"` // "active", "inactive", "sold_out"
	TotalTickets     int       `json:"total_tickets"`
	AvailableTickets int       `json:"available_tickets"`
	IsSeatedEvent    bool      `json:"is_seated_event"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// EventStatus represents the status of an event
type EventStatus string

const (
	EventStatusActive   EventStatus = "active"
	EventStatusInactive EventStatus = "inactive"
	EventStatusSoldOut  EventStatus = "sold_out"
)

// IsActive checks if the event is active
func (e *Event) IsActive() bool {
	return e.Status == string(EventStatusActive)
}

// IsSoldOut checks if the event is sold out
func (e *Event) IsSoldOut() bool {
	return e.Status == string(EventStatusSoldOut) || e.AvailableTickets <= 0
}

// CanPurchase checks if tickets can be purchased for this event
func (e *Event) CanPurchase() bool {
	now := time.Now()
	return e.IsActive() && !e.IsSoldOut() && now.Before(e.EndTime)
}
