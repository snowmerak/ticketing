package domain

import (
	"time"

	"github.com/google/uuid"
)

// Seat represents a seat in a venue
type Seat struct {
	ID        uuid.UUID `json:"id"`
	EventID   uuid.UUID `json:"event_id"`
	Section   string    `json:"section"`
	Row       string    `json:"row"`
	Number    string    `json:"number"`
	Price     int64     `json:"price"`  // Price in cents
	Status    string    `json:"status"` // "available", "reserved", "sold"
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SeatStatus represents the status of a seat
type SeatStatus string

const (
	SeatStatusAvailable SeatStatus = "available"
	SeatStatusReserved  SeatStatus = "reserved"
	SeatStatusSold      SeatStatus = "sold"
)

// IsAvailable checks if the seat is available
func (s *Seat) IsAvailable() bool {
	return s.Status == string(SeatStatusAvailable)
}

// IsReserved checks if the seat is reserved
func (s *Seat) IsReserved() bool {
	return s.Status == string(SeatStatusReserved)
}

// IsSold checks if the seat is sold
func (s *Seat) IsSold() bool {
	return s.Status == string(SeatStatusSold)
}

// GetDisplayName returns a human-readable seat identifier
func (s *Seat) GetDisplayName() string {
	if s.Row != "" && s.Number != "" {
		return s.Section + "-" + s.Row + "-" + s.Number
	}
	return s.Section
}
