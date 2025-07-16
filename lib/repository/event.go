package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/snowmerak/ticketing/lib/domain"
)

// EventRepository defines the interface for event data operations
type EventRepository interface {
	// Create creates a new event
	Create(ctx context.Context, event *domain.Event) error

	// GetByID retrieves an event by its ID
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Event, error)

	// Update updates an existing event
	Update(ctx context.Context, event *domain.Event) error

	// Delete deletes an event by its ID
	Delete(ctx context.Context, id uuid.UUID) error

	// List retrieves all events with pagination
	List(ctx context.Context, offset, limit int) ([]*domain.Event, error)

	// GetActiveEvents retrieves all active events
	GetActiveEvents(ctx context.Context) ([]*domain.Event, error)

	// UpdateAvailableTickets updates the available ticket count
	UpdateAvailableTickets(ctx context.Context, eventID uuid.UUID, count int) error

	// DecrementAvailableTickets decrements available tickets atomically
	DecrementAvailableTickets(ctx context.Context, eventID uuid.UUID, count int) error

	// IncrementAvailableTickets increments available tickets atomically
	IncrementAvailableTickets(ctx context.Context, eventID uuid.UUID, count int) error
}
