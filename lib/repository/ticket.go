package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/snowmerak/ticketing/lib/domain"
)

// TicketRepository defines the interface for ticket data operations
type TicketRepository interface {
	// Create creates a new ticket
	Create(ctx context.Context, ticket *domain.Ticket) error

	// GetByID retrieves a ticket by its ID
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Ticket, error)

	// GetByUserID retrieves all tickets for a user
	GetByUserID(ctx context.Context, userID uuid.UUID) ([]*domain.Ticket, error)

	// GetByEventID retrieves all tickets for an event
	GetByEventID(ctx context.Context, eventID uuid.UUID) ([]*domain.Ticket, error)

	// GetBySeatID retrieves a ticket by seat ID
	GetBySeatID(ctx context.Context, seatID uuid.UUID) (*domain.Ticket, error)

	// Update updates an existing ticket
	Update(ctx context.Context, ticket *domain.Ticket) error

	// UpdateStatus updates ticket status
	UpdateStatus(ctx context.Context, ticketID uuid.UUID, status string) error

	// GetExpiredReservations retrieves all expired reservations
	GetExpiredReservations(ctx context.Context) ([]*domain.Ticket, error)

	// ConfirmTicket confirms a reserved ticket
	ConfirmTicket(ctx context.Context, ticketID uuid.UUID) error

	// CancelTicket cancels a ticket and updates its status
	CancelTicket(ctx context.Context, ticketID uuid.UUID) error

	// Delete deletes a ticket by its ID
	Delete(ctx context.Context, id uuid.UUID) error
}
