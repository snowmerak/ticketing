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
	cmd := r.client.GetRedisClient().B().Set().Key(key).Value(string(data)).Build()
	if err := r.client.GetRedisClient().Do(ctx, cmd).Error(); err != nil {
		return fmt.Errorf("failed to create ticket: %w", err)
	}

	// Add to user tickets index
	userTicketsKey := fmt.Sprintf("user_tickets:%s", ticket.UserID.String())
	userCmd := r.client.GetRedisClient().B().Sadd().Key(userTicketsKey).Member(ticket.ID.String()).Build()
	if err := r.client.GetRedisClient().Do(ctx, userCmd).Error(); err != nil {
		return fmt.Errorf("failed to add to user tickets: %w", err)
	}

	// Add to event tickets index
	eventTicketsKey := fmt.Sprintf("event_tickets:%s", ticket.EventID.String())
	eventCmd := r.client.GetRedisClient().B().Sadd().Key(eventTicketsKey).Member(ticket.ID.String()).Build()
	if err := r.client.GetRedisClient().Do(ctx, eventCmd).Error(); err != nil {
		return fmt.Errorf("failed to add to event tickets: %w", err)
	}

	// Add to seat ticket index if seat exists
	if ticket.SeatID != nil {
		seatTicketKey := fmt.Sprintf("seat_ticket:%s", ticket.SeatID.String())
		seatCmd := r.client.GetRedisClient().B().Set().Key(seatTicketKey).Value(ticket.ID.String()).Build()
		if err := r.client.GetRedisClient().Do(ctx, seatCmd).Error(); err != nil {
			return fmt.Errorf("failed to add seat ticket mapping: %w", err)
		}
	}

	// Add to reserved tickets index if reserved
	if ticket.Status == string(domain.TicketStatusReserved) && ticket.ExpiresAt != nil {
		reservedKey := fmt.Sprintf("reserved_tickets:%d", ticket.ExpiresAt.Unix())
		reservedCmd := r.client.GetRedisClient().B().Sadd().Key(reservedKey).Member(ticket.ID.String()).Build()
		if err := r.client.GetRedisClient().Do(ctx, reservedCmd).Error(); err != nil {
			return fmt.Errorf("failed to add to reserved tickets: %w", err)
		}
	}

	return nil
}

// GetByID retrieves a ticket by its ID
func (r *TicketRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Ticket, error) {
	key := fmt.Sprintf("ticket:%s", id.String())

	cmd := r.client.GetRedisClient().B().Get().Key(key).Build()
	result := r.client.GetRedisClient().Do(ctx, cmd)
	if result.Error() != nil {
		return nil, fmt.Errorf("failed to get ticket: %w", result.Error())
	}

	data, err := result.ToString()
	if err != nil {
		return nil, fmt.Errorf("failed to get ticket data: %w", err)
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

	cmd := r.client.GetRedisClient().B().Smembers().Key(userTicketsKey).Build()
	result := r.client.GetRedisClient().Do(ctx, cmd)
	if result.Error() != nil {
		return nil, fmt.Errorf("failed to get user tickets: %w", result.Error())
	}

	members, err := result.AsStrSlice()
	if err != nil {
		return nil, fmt.Errorf("failed to parse members: %w", err)
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

	cmd := r.client.GetRedisClient().B().Smembers().Key(eventTicketsKey).Build()
	result := r.client.GetRedisClient().Do(ctx, cmd)
	if result.Error() != nil {
		return nil, fmt.Errorf("failed to get event tickets: %w", result.Error())
	}

	members, err := result.AsStrSlice()
	if err != nil {
		return nil, fmt.Errorf("failed to parse members: %w", err)
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

	cmd := r.client.GetRedisClient().B().Get().Key(seatTicketKey).Build()
	result := r.client.GetRedisClient().Do(ctx, cmd)
	if result.Error() != nil {
		return nil, fmt.Errorf("failed to get seat ticket: %w", result.Error())
	}

	ticketID, err := result.ToString()
	if err != nil {
		return nil, fmt.Errorf("failed to get ticket ID: %w", err)
	}

	ticketUUID, err := uuid.Parse(ticketID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse ticket ID: %w", err)
	}

	return r.GetByID(ctx, ticketUUID)
}

// Update updates an existing ticket
func (r *TicketRepository) Update(ctx context.Context, ticket *domain.Ticket) error {
	ticket.UpdatedAt = time.Now()

	data, err := json.Marshal(ticket)
	if err != nil {
		return fmt.Errorf("failed to marshal ticket: %w", err)
	}

	key := fmt.Sprintf("ticket:%s", ticket.ID.String())

	// Update the ticket data
	cmd := r.client.GetRedisClient().B().Set().Key(key).Value(string(data)).Build()
	if err := r.client.GetRedisClient().Do(ctx, cmd).Error(); err != nil {
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

	ticket.Status = status
	return r.Update(ctx, ticket)
}

// GetExpiredReservations retrieves all expired reservations
func (r *TicketRepository) GetExpiredReservations(ctx context.Context) ([]*domain.Ticket, error) {
	now := time.Now().Unix()

	// Get all reservation keys up to current time
	var expiredTickets []*domain.Ticket

	// This is a simplified implementation - in production, you'd use a better approach
	// to track expiration times, possibly with sorted sets
	for i := now - 3600; i <= now; i++ { // Check last hour
		reservedKey := fmt.Sprintf("reserved_tickets:%d", i)

		cmd := r.client.GetRedisClient().B().Smembers().Key(reservedKey).Build()
		result := r.client.GetRedisClient().Do(ctx, cmd)
		if result.Error() != nil {
			continue
		}

		members, err := result.AsStrSlice()
		if err != nil {
			continue
		}

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
	}

	return expiredTickets, nil
}

// ConfirmTicket confirms a reserved ticket
func (r *TicketRepository) ConfirmTicket(ctx context.Context, ticketID uuid.UUID) error {
	return r.UpdateStatus(ctx, ticketID, string(domain.TicketStatusConfirmed))
}

// CancelTicket cancels a ticket and updates its status
func (r *TicketRepository) CancelTicket(ctx context.Context, ticketID uuid.UUID) error {
	return r.UpdateStatus(ctx, ticketID, string(domain.TicketStatusCancelled))
}

// Delete deletes a ticket by its ID
func (r *TicketRepository) Delete(ctx context.Context, id uuid.UUID) error {
	ticket, err := r.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to get ticket: %w", err)
	}

	key := fmt.Sprintf("ticket:%s", id.String())

	// Remove from Redis
	delCmd := r.client.GetRedisClient().B().Del().Key(key).Build()
	if err := r.client.GetRedisClient().Do(ctx, delCmd).Error(); err != nil {
		return fmt.Errorf("failed to delete ticket: %w", err)
	}

	// Remove from indexes
	idStr := id.String()

	// Remove from user tickets
	userTicketsKey := fmt.Sprintf("user_tickets:%s", ticket.UserID.String())
	userRemCmd := r.client.GetRedisClient().B().Srem().Key(userTicketsKey).Member(idStr).Build()
	if err := r.client.GetRedisClient().Do(ctx, userRemCmd).Error(); err != nil {
		return fmt.Errorf("failed to remove from user tickets: %w", err)
	}

	// Remove from event tickets
	eventTicketsKey := fmt.Sprintf("event_tickets:%s", ticket.EventID.String())
	eventRemCmd := r.client.GetRedisClient().B().Srem().Key(eventTicketsKey).Member(idStr).Build()
	if err := r.client.GetRedisClient().Do(ctx, eventRemCmd).Error(); err != nil {
		return fmt.Errorf("failed to remove from event tickets: %w", err)
	}

	// Remove seat ticket mapping if exists
	if ticket.SeatID != nil {
		seatTicketKey := fmt.Sprintf("seat_ticket:%s", ticket.SeatID.String())
		seatDelCmd := r.client.GetRedisClient().B().Del().Key(seatTicketKey).Build()
		if err := r.client.GetRedisClient().Do(ctx, seatDelCmd).Error(); err != nil {
			return fmt.Errorf("failed to remove seat ticket mapping: %w", err)
		}
	}

	// Remove from reserved tickets if applicable
	if ticket.Status == string(domain.TicketStatusReserved) && ticket.ExpiresAt != nil {
		reservedKey := fmt.Sprintf("reserved_tickets:%d", ticket.ExpiresAt.Unix())
		reservedRemCmd := r.client.GetRedisClient().B().Srem().Key(reservedKey).Member(idStr).Build()
		if err := r.client.GetRedisClient().Do(ctx, reservedRemCmd).Error(); err != nil {
			return fmt.Errorf("failed to remove from reserved tickets: %w", err)
		}
	}

	return nil
}
