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

// TicketRepository implements repository.TicketRepository using Redis
type TicketRepository struct {
	client *redis.Client
}

// NewTicketRepository creates a new TicketRepository
func NewTicketRepository(client *redis.Client) *TicketRepository {
	return &TicketRepository{
		client: client,
	}
}

// Compile-time check to ensure TicketRepository implements repository.TicketRepository
var _ repository.TicketRepository = (*TicketRepository)(nil)

// Create creates a new ticket
func (r *TicketRepository) Create(ctx context.Context, ticket *domain.Ticket) error {
	ticket.CreatedAt = time.Now()
	ticket.UpdatedAt = time.Now()

	data, err := json.Marshal(ticket)
	if err != nil {
		return fmt.Errorf("failed to marshal ticket: %w", err)
	}

	key := fmt.Sprintf("ticket:%s", ticket.ID.String())

	// Set the ticket data
	if err := r.client.GetRedisClient().Set(ctx, key, data, 0).Err(); err != nil {
		return fmt.Errorf("failed to create ticket: %w", err)
	}

	// Add to user tickets index
	userTicketsKey := fmt.Sprintf("user_tickets:%s", ticket.UserID.String())
	if err := r.client.GetRedisClient().SAdd(ctx, userTicketsKey, ticket.ID.String()).Err(); err != nil {
		return fmt.Errorf("failed to add to user tickets: %w", err)
	}

	// Add to event tickets index
	eventTicketsKey := fmt.Sprintf("event_tickets:%s", ticket.EventID.String())
	if err := r.client.GetRedisClient().SAdd(ctx, eventTicketsKey, ticket.ID.String()).Err(); err != nil {
		return fmt.Errorf("failed to add to event tickets: %w", err)
	}

	// Add to seat ticket index if seat is specified
	if ticket.SeatID != nil {
		seatTicketKey := fmt.Sprintf("seat_ticket:%s", ticket.SeatID.String())
		if err := r.client.GetRedisClient().Set(ctx, seatTicketKey, ticket.ID.String(), 0).Err(); err != nil {
			return fmt.Errorf("failed to add to seat ticket: %w", err)
		}
	}

	// Add to status index
	statusKey := fmt.Sprintf("tickets:%s", ticket.Status)
	if err := r.client.GetRedisClient().SAdd(ctx, statusKey, ticket.ID.String()).Err(); err != nil {
		return fmt.Errorf("failed to add to status index: %w", err)
	}

	return nil
}

// GetByID retrieves a ticket by its ID
func (r *TicketRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Ticket, error) {
	key := fmt.Sprintf("ticket:%s", id.String())

	data, err := r.client.GetRedisClient().Get(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get ticket: %w", err)
	}

	var ticket domain.Ticket
	if err := json.Unmarshal([]byte(data), &ticket); err != nil {
		return nil, fmt.Errorf("failed to unmarshal ticket: %w", err)
	}

	return &ticket, nil
}

// GetByUserID retrieves all tickets for a user
func (r *TicketRepository) GetByUserID(ctx context.Context, userID uuid.UUID) ([]*domain.Ticket, error) {
	userTicketsKey := fmt.Sprintf("user_tickets:%s", userID.String())

	members, err := r.client.GetRedisClient().SMembers(ctx, userTicketsKey).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get user tickets: %w", err)
	}

	var tickets []*domain.Ticket
	for _, member := range members {
		ticketID, err := uuid.Parse(member)
		if err != nil {
			continue
		}

		ticket, err := r.GetByID(ctx, ticketID)
		if err != nil {
			continue
		}

		tickets = append(tickets, ticket)
	}

	return tickets, nil
}

// GetByEventID retrieves all tickets for an event
func (r *TicketRepository) GetByEventID(ctx context.Context, eventID uuid.UUID) ([]*domain.Ticket, error) {
	eventTicketsKey := fmt.Sprintf("event_tickets:%s", eventID.String())

	members, err := r.client.GetRedisClient().SMembers(ctx, eventTicketsKey).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get event tickets: %w", err)
	}

	var tickets []*domain.Ticket
	for _, member := range members {
		ticketID, err := uuid.Parse(member)
		if err != nil {
			continue
		}

		ticket, err := r.GetByID(ctx, ticketID)
		if err != nil {
			continue
		}

		tickets = append(tickets, ticket)
	}

	return tickets, nil
}

// GetBySeatID retrieves a ticket by seat ID
func (r *TicketRepository) GetBySeatID(ctx context.Context, seatID uuid.UUID) (*domain.Ticket, error) {
	seatTicketKey := fmt.Sprintf("seat_ticket:%s", seatID.String())

	ticketIDStr, err := r.client.GetRedisClient().Get(ctx, seatTicketKey).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get seat ticket: %w", err)
	}

	ticketID, err := uuid.Parse(ticketIDStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse ticket ID: %w", err)
	}

	return r.GetByID(ctx, ticketID)
}

// Update updates an existing ticket
func (r *TicketRepository) Update(ctx context.Context, ticket *domain.Ticket) error {
	ticket.UpdatedAt = time.Now()

	data, err := json.Marshal(ticket)
	if err != nil {
		return fmt.Errorf("failed to marshal ticket: %w", err)
	}

	key := fmt.Sprintf("ticket:%s", ticket.ID.String())

	if err := r.client.GetRedisClient().Set(ctx, key, data, 0).Err(); err != nil {
		return fmt.Errorf("failed to update ticket: %w", err)
	}

	return nil
}

// UpdateStatus updates ticket status
func (r *TicketRepository) UpdateStatus(ctx context.Context, ticketID uuid.UUID, status string) error {
	ticket, err := r.GetByID(ctx, ticketID)
	if err != nil {
		return fmt.Errorf("failed to get ticket: %w", err)
	}

	oldStatus := ticket.Status
	ticket.Status = status

	// Update the ticket
	if err := r.Update(ctx, ticket); err != nil {
		return fmt.Errorf("failed to update ticket: %w", err)
	}

	// Update status indexes
	if oldStatus != status {
		// Remove from old status index
		oldStatusKey := fmt.Sprintf("tickets:%s", oldStatus)
		if err := r.client.GetRedisClient().SRem(ctx, oldStatusKey, ticketID.String()).Err(); err != nil {
			return fmt.Errorf("failed to remove from old status index: %w", err)
		}

		// Add to new status index
		newStatusKey := fmt.Sprintf("tickets:%s", status)
		if err := r.client.GetRedisClient().SAdd(ctx, newStatusKey, ticketID.String()).Err(); err != nil {
			return fmt.Errorf("failed to add to new status index: %w", err)
		}
	}

	return nil
}

// GetExpiredReservations retrieves all expired reservations
func (r *TicketRepository) GetExpiredReservations(ctx context.Context) ([]*domain.Ticket, error) {
	reservedKey := fmt.Sprintf("tickets:%s", string(domain.TicketStatusReserved))

	members, err := r.client.GetRedisClient().SMembers(ctx, reservedKey).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get reserved tickets: %w", err)
	}

	var expiredTickets []*domain.Ticket
	for _, member := range members {
		ticketID, err := uuid.Parse(member)
		if err != nil {
			continue
		}

		ticket, err := r.GetByID(ctx, ticketID)
		if err != nil {
			continue
		}

		if ticket.IsExpired() {
			expiredTickets = append(expiredTickets, ticket)
		}
	}

	return expiredTickets, nil
}

// ConfirmTicket confirms a reserved ticket
func (r *TicketRepository) ConfirmTicket(ctx context.Context, ticketID uuid.UUID) error {
	ticket, err := r.GetByID(ctx, ticketID)
	if err != nil {
		return fmt.Errorf("failed to get ticket: %w", err)
	}

	if !ticket.IsReserved() {
		return fmt.Errorf("ticket is not reserved")
	}

	if ticket.IsExpired() {
		return fmt.Errorf("ticket reservation has expired")
	}

	// Update status to confirmed
	ticket.Status = string(domain.TicketStatusConfirmed)
	ticket.ExpiresAt = nil // Remove expiration

	if err := r.Update(ctx, ticket); err != nil {
		return fmt.Errorf("failed to update ticket: %w", err)
	}

	// Update status indexes
	reservedKey := fmt.Sprintf("tickets:%s", string(domain.TicketStatusReserved))
	confirmedKey := fmt.Sprintf("tickets:%s", string(domain.TicketStatusConfirmed))

	pipe := r.client.GetRedisClient().Pipeline()
	pipe.SRem(ctx, reservedKey, ticketID.String())
	pipe.SAdd(ctx, confirmedKey, ticketID.String())

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("failed to update status indexes: %w", err)
	}

	return nil
}

// CancelTicket cancels a ticket and updates its status
func (r *TicketRepository) CancelTicket(ctx context.Context, ticketID uuid.UUID) error {
	ticket, err := r.GetByID(ctx, ticketID)
	if err != nil {
		return fmt.Errorf("failed to get ticket: %w", err)
	}

	if ticket.IsCancelled() {
		return fmt.Errorf("ticket is already cancelled")
	}

	oldStatus := ticket.Status
	ticket.Status = string(domain.TicketStatusCancelled)

	if err := r.Update(ctx, ticket); err != nil {
		return fmt.Errorf("failed to update ticket: %w", err)
	}

	// Update status indexes
	oldStatusKey := fmt.Sprintf("tickets:%s", oldStatus)
	cancelledKey := fmt.Sprintf("tickets:%s", string(domain.TicketStatusCancelled))

	pipe := r.client.GetRedisClient().Pipeline()
	pipe.SRem(ctx, oldStatusKey, ticketID.String())
	pipe.SAdd(ctx, cancelledKey, ticketID.String())

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("failed to update status indexes: %w", err)
	}

	return nil
}

// Delete deletes a ticket by its ID
func (r *TicketRepository) Delete(ctx context.Context, id uuid.UUID) error {
	ticket, err := r.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to get ticket: %w", err)
	}

	key := fmt.Sprintf("ticket:%s", id.String())

	// Remove from Redis
	if err := r.client.GetRedisClient().Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("failed to delete ticket: %w", err)
	}

	// Remove from indexes
	idStr := id.String()
	userTicketsKey := fmt.Sprintf("user_tickets:%s", ticket.UserID.String())
	eventTicketsKey := fmt.Sprintf("event_tickets:%s", ticket.EventID.String())
	statusKey := fmt.Sprintf("tickets:%s", ticket.Status)

	pipe := r.client.GetRedisClient().Pipeline()
	pipe.SRem(ctx, userTicketsKey, idStr)
	pipe.SRem(ctx, eventTicketsKey, idStr)
	pipe.SRem(ctx, statusKey, idStr)

	// Remove from seat ticket index if seat is specified
	if ticket.SeatID != nil {
		seatTicketKey := fmt.Sprintf("seat_ticket:%s", ticket.SeatID.String())
		pipe.Del(ctx, seatTicketKey)
	}

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("failed to remove from indexes: %w", err)
	}

	return nil
}
