package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/snowmerak/ticketing/lib/domain"
)

// SeatRepository defines the interface for seat data operations
type SeatRepository interface {
	// Create creates a new seat
	Create(ctx context.Context, seat *domain.Seat) error

	// CreateBatch creates multiple seats in a single transaction
	CreateBatch(ctx context.Context, seats []*domain.Seat) error

	// GetByID retrieves a seat by its ID
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Seat, error)

	// GetByEventID retrieves all seats for an event
	GetByEventID(ctx context.Context, eventID uuid.UUID) ([]*domain.Seat, error)

	// GetAvailableByEventID retrieves available seats for an event
	GetAvailableByEventID(ctx context.Context, eventID uuid.UUID) ([]*domain.Seat, error)

	// GetBySection retrieves seats by section
	GetBySection(ctx context.Context, eventID uuid.UUID, section string) ([]*domain.Seat, error)

	// Update updates an existing seat
	Update(ctx context.Context, seat *domain.Seat) error

	// UpdateStatus updates seat status
	UpdateStatus(ctx context.Context, seatID uuid.UUID, status string) error

	// ReserveSeats reserves multiple seats atomically
	ReserveSeats(ctx context.Context, seatIDs []uuid.UUID) error

	// ReleaseSeats releases reserved seats atomically
	ReleaseSeats(ctx context.Context, seatIDs []uuid.UUID) error

	// Delete deletes a seat by its ID
	Delete(ctx context.Context, id uuid.UUID) error

	// DeleteByEventID deletes all seats for an event
	DeleteByEventID(ctx context.Context, eventID uuid.UUID) error
}
