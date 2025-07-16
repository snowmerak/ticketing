package domain

import (
	"time"

	"github.com/google/uuid"
)

// Ticket represents a purchased ticket
type Ticket struct {
	ID        uuid.UUID  `json:"id"`
	EventID   uuid.UUID  `json:"event_id"`
	SeatID    *uuid.UUID `json:"seat_id,omitempty"` // nil for standing events
	UserID    uuid.UUID  `json:"user_id"`
	Price     int64      `json:"price"`  // Price in cents
	Status    string     `json:"status"` // "reserved", "confirmed", "cancelled"
	IssuedAt  time.Time  `json:"issued_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"` // For temporary reservations
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// TicketStatus represents the status of a ticket
type TicketStatus string

const (
	TicketStatusReserved  TicketStatus = "reserved"
	TicketStatusConfirmed TicketStatus = "confirmed"
	TicketStatusCancelled TicketStatus = "cancelled"
)

// IsExpired checks if the ticket reservation has expired
func (t *Ticket) IsExpired() bool {
	if t.ExpiresAt == nil {
		return false
	}
	return time.Now().After(*t.ExpiresAt)
}

// IsConfirmed checks if the ticket is confirmed
func (t *Ticket) IsConfirmed() bool {
	return t.Status == string(TicketStatusConfirmed)
}

// IsReserved checks if the ticket is reserved
func (t *Ticket) IsReserved() bool {
	return t.Status == string(TicketStatusReserved)
}

// IsCancelled checks if the ticket is cancelled
func (t *Ticket) IsCancelled() bool {
	return t.Status == string(TicketStatusCancelled)
}
