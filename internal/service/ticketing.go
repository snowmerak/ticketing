package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/snowmerak/ticketing/lib/adapter"
	"github.com/snowmerak/ticketing/lib/domain"
	"github.com/snowmerak/ticketing/lib/repository"
)

// TicketingService handles ticket purchasing logic
type TicketingService struct {
	ticketRepo repository.TicketRepository
	eventRepo  repository.EventRepository
	seatRepo   repository.SeatRepository
	queueRepo  repository.QueueRepository
	cache      adapter.Cache
	lock       adapter.Lock
	logger     adapter.Logger
}

// NewTicketingService creates a new TicketingService
func NewTicketingService(
	ticketRepo repository.TicketRepository,
	eventRepo repository.EventRepository,
	seatRepo repository.SeatRepository,
	queueRepo repository.QueueRepository,
	cache adapter.Cache,
	lock adapter.Lock,
	logger adapter.Logger,
) *TicketingService {
	return &TicketingService{
		ticketRepo: ticketRepo,
		eventRepo:  eventRepo,
		seatRepo:   seatRepo,
		queueRepo:  queueRepo,
		cache:      cache,
		lock:       lock,
		logger:     logger,
	}
}

// PurchaseTicket purchases a ticket for an event
func (s *TicketingService) PurchaseTicket(ctx context.Context, eventID, userID uuid.UUID, seatID *uuid.UUID, sessionID string) (*domain.Ticket, error) {
	s.logger.Info(ctx, "Starting ticket purchase",
		"event_id", eventID,
		"user_id", userID,
		"seat_id", seatID,
		"session_id", sessionID)

	// Verify user is active in queue
	queueEntry, err := s.queueRepo.GetBySessionID(ctx, sessionID)
	if err != nil {
		s.logger.Error(ctx, "Failed to get queue entry", "session_id", sessionID, "error", err)
		return nil, fmt.Errorf("invalid session: %w", err)
	}

	if !queueEntry.IsActive() || queueEntry.IsExpired() {
		s.logger.Warn(ctx, "Queue session not active or expired",
			"session_id", sessionID,
			"status", queueEntry.Status,
			"expired", queueEntry.IsExpired())
		return nil, fmt.Errorf("queue session is not active or has expired")
	}

	if queueEntry.EventID != eventID || queueEntry.UserID != userID {
		s.logger.Warn(ctx, "Queue entry mismatch",
			"queue_event_id", queueEntry.EventID,
			"queue_user_id", queueEntry.UserID,
			"request_event_id", eventID,
			"request_user_id", userID)
		return nil, fmt.Errorf("queue entry does not match request")
	}

	// Get event details
	event, err := s.eventRepo.GetByID(ctx, eventID)
	if err != nil {
		s.logger.Error(ctx, "Failed to get event", "event_id", eventID, "error", err)
		return nil, fmt.Errorf("failed to get event: %w", err)
	}

	if !event.CanPurchase() {
		s.logger.Warn(ctx, "Event not available for purchase", "event_id", eventID, "status", event.Status)
		return nil, fmt.Errorf("event is not available for purchase")
	}

	// Use distributed lock for atomic ticket purchase
	lockKey := fmt.Sprintf("ticket_purchase:%s", eventID.String())
	if seatID != nil {
		lockKey = fmt.Sprintf("ticket_purchase:%s:%s", eventID.String(), seatID.String())
	}

	acquired, err := s.lock.Acquire(ctx, lockKey, 10*time.Second)
	if err != nil {
		s.logger.Error(ctx, "Failed to acquire lock", "error", err)
		return nil, fmt.Errorf("failed to acquire lock: %w", err)
	}

	if !acquired {
		s.logger.Warn(ctx, "Failed to acquire lock - purchase busy", "event_id", eventID)
		return nil, fmt.Errorf("ticket purchase is busy, please try again")
	}

	defer func() {
		if err := s.lock.Release(ctx, lockKey); err != nil {
			s.logger.Error(ctx, "Failed to release lock", "error", err)
		}
	}()

	var ticket *domain.Ticket
	var price int64

	if event.IsSeatedEvent {
		// Handle seated event
		if seatID == nil {
			return nil, fmt.Errorf("seat ID is required for seated events")
		}

		ticket, err = s.purchaseSeatedTicket(ctx, event, userID, *seatID)
		if err != nil {
			return nil, fmt.Errorf("failed to purchase seated ticket: %w", err)
		}
		price = ticket.Price
	} else {
		// Handle standing event
		ticket, err = s.purchaseStandingTicket(ctx, event, userID)
		if err != nil {
			return nil, fmt.Errorf("failed to purchase standing ticket: %w", err)
		}
		price = ticket.Price
	}

	s.logger.Info(ctx, "Ticket purchased successfully",
		"ticket_id", ticket.ID,
		"event_id", eventID,
		"user_id", userID,
		"price", price)

	return ticket, nil
}

// purchaseSeatedTicket handles the purchase of a seated ticket
func (s *TicketingService) purchaseSeatedTicket(ctx context.Context, event *domain.Event, userID, seatID uuid.UUID) (*domain.Ticket, error) {
	// Get seat details
	seat, err := s.seatRepo.GetByID(ctx, seatID)
	if err != nil {
		s.logger.Error(ctx, "Failed to get seat", "seat_id", seatID, "error", err)
		return nil, fmt.Errorf("failed to get seat: %w", err)
	}

	if seat.EventID != event.ID {
		return nil, fmt.Errorf("seat does not belong to this event")
	}

	if !seat.IsAvailable() {
		s.logger.Warn(ctx, "Seat not available", "seat_id", seatID, "status", seat.Status)
		return nil, fmt.Errorf("seat is not available")
	}

	// Reserve the seat
	if err := s.seatRepo.ReserveSeats(ctx, []uuid.UUID{seatID}); err != nil {
		s.logger.Error(ctx, "Failed to reserve seat", "seat_id", seatID, "error", err)
		return nil, fmt.Errorf("failed to reserve seat: %w", err)
	}

	// Create ticket
	ticket := &domain.Ticket{
		ID:        uuid.New(),
		EventID:   event.ID,
		SeatID:    &seatID,
		UserID:    userID,
		Price:     seat.Price,
		Status:    string(domain.TicketStatusReserved),
		IssuedAt:  time.Now(),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	// Set expiration (15 minutes to confirm)
	expiry := time.Now().Add(15 * time.Minute)
	ticket.ExpiresAt = &expiry

	if err := s.ticketRepo.Create(ctx, ticket); err != nil {
		s.logger.Error(ctx, "Failed to create ticket", "error", err)

		// Release the seat if ticket creation fails
		if err := s.seatRepo.ReleaseSeats(ctx, []uuid.UUID{seatID}); err != nil {
			s.logger.Error(ctx, "Failed to release seat after ticket creation failure", "seat_id", seatID, "error", err)
		}

		return nil, fmt.Errorf("failed to create ticket: %w", err)
	}

	// Decrement available tickets
	if err := s.eventRepo.DecrementAvailableTickets(ctx, event.ID, 1); err != nil {
		s.logger.Error(ctx, "Failed to decrement available tickets", "error", err)
		// Note: In a real system, you might want to rollback the ticket creation here
	}

	return ticket, nil
}

// purchaseStandingTicket handles the purchase of a standing ticket
func (s *TicketingService) purchaseStandingTicket(ctx context.Context, event *domain.Event, userID uuid.UUID) (*domain.Ticket, error) {
	// Check if tickets are available
	if event.AvailableTickets <= 0 {
		s.logger.Warn(ctx, "No tickets available", "event_id", event.ID)
		return nil, fmt.Errorf("no tickets available")
	}

	// Decrement available tickets first
	if err := s.eventRepo.DecrementAvailableTickets(ctx, event.ID, 1); err != nil {
		s.logger.Error(ctx, "Failed to decrement available tickets", "error", err)
		return nil, fmt.Errorf("failed to reserve ticket: %w", err)
	}

	// Create ticket (assuming a base price for standing tickets)
	ticket := &domain.Ticket{
		ID:        uuid.New(),
		EventID:   event.ID,
		SeatID:    nil, // No seat for standing events
		UserID:    userID,
		Price:     5000, // $50.00 in cents (this could be configurable)
		Status:    string(domain.TicketStatusReserved),
		IssuedAt:  time.Now(),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	// Set expiration (15 minutes to confirm)
	expiry := time.Now().Add(15 * time.Minute)
	ticket.ExpiresAt = &expiry

	if err := s.ticketRepo.Create(ctx, ticket); err != nil {
		s.logger.Error(ctx, "Failed to create ticket", "error", err)

		// Increment back the available tickets if ticket creation fails
		if err := s.eventRepo.IncrementAvailableTickets(ctx, event.ID, 1); err != nil {
			s.logger.Error(ctx, "Failed to increment available tickets after ticket creation failure", "error", err)
		}

		return nil, fmt.Errorf("failed to create ticket: %w", err)
	}

	return ticket, nil
}

// ConfirmTicket confirms a reserved ticket
func (s *TicketingService) ConfirmTicket(ctx context.Context, ticketID uuid.UUID) error {
	s.logger.Info(ctx, "Confirming ticket", "ticket_id", ticketID)

	ticket, err := s.ticketRepo.GetByID(ctx, ticketID)
	if err != nil {
		s.logger.Error(ctx, "Failed to get ticket", "ticket_id", ticketID, "error", err)
		return fmt.Errorf("failed to get ticket: %w", err)
	}

	if !ticket.IsReserved() {
		s.logger.Warn(ctx, "Ticket is not reserved", "ticket_id", ticketID, "status", ticket.Status)
		return fmt.Errorf("ticket is not reserved")
	}

	if ticket.IsExpired() {
		s.logger.Warn(ctx, "Ticket reservation has expired", "ticket_id", ticketID)
		return fmt.Errorf("ticket reservation has expired")
	}

	// Confirm the ticket
	if err := s.ticketRepo.ConfirmTicket(ctx, ticketID); err != nil {
		s.logger.Error(ctx, "Failed to confirm ticket", "ticket_id", ticketID, "error", err)
		return fmt.Errorf("failed to confirm ticket: %w", err)
	}

	// If it's a seated event, mark the seat as sold
	if ticket.SeatID != nil {
		if err := s.seatRepo.UpdateStatus(ctx, *ticket.SeatID, string(domain.SeatStatusSold)); err != nil {
			s.logger.Error(ctx, "Failed to update seat status", "seat_id", *ticket.SeatID, "error", err)
			// Note: In a real system, you might want to rollback the ticket confirmation here
		}
	}

	s.logger.Info(ctx, "Ticket confirmed successfully", "ticket_id", ticketID)
	return nil
}

// CancelTicket cancels a ticket and releases the seat/inventory
func (s *TicketingService) CancelTicket(ctx context.Context, ticketID uuid.UUID) error {
	s.logger.Info(ctx, "Cancelling ticket", "ticket_id", ticketID)

	ticket, err := s.ticketRepo.GetByID(ctx, ticketID)
	if err != nil {
		s.logger.Error(ctx, "Failed to get ticket", "ticket_id", ticketID, "error", err)
		return fmt.Errorf("failed to get ticket: %w", err)
	}

	if ticket.IsCancelled() {
		s.logger.Warn(ctx, "Ticket is already cancelled", "ticket_id", ticketID)
		return fmt.Errorf("ticket is already cancelled")
	}

	// Cancel the ticket
	if err := s.ticketRepo.CancelTicket(ctx, ticketID); err != nil {
		s.logger.Error(ctx, "Failed to cancel ticket", "ticket_id", ticketID, "error", err)
		return fmt.Errorf("failed to cancel ticket: %w", err)
	}

	// Release the seat if it's a seated event
	if ticket.SeatID != nil {
		if err := s.seatRepo.ReleaseSeats(ctx, []uuid.UUID{*ticket.SeatID}); err != nil {
			s.logger.Error(ctx, "Failed to release seat", "seat_id", *ticket.SeatID, "error", err)
		}
	}

	// Increment available tickets
	if err := s.eventRepo.IncrementAvailableTickets(ctx, ticket.EventID, 1); err != nil {
		s.logger.Error(ctx, "Failed to increment available tickets", "error", err)
	}

	s.logger.Info(ctx, "Ticket cancelled successfully", "ticket_id", ticketID)
	return nil
}

// GetUserTickets retrieves all tickets for a user
func (s *TicketingService) GetUserTickets(ctx context.Context, userID uuid.UUID) ([]*domain.Ticket, error) {
	tickets, err := s.ticketRepo.GetByUserID(ctx, userID)
	if err != nil {
		s.logger.Error(ctx, "Failed to get user tickets", "user_id", userID, "error", err)
		return nil, fmt.Errorf("failed to get user tickets: %w", err)
	}

	return tickets, nil
}

// GetTicket retrieves a ticket by ID
func (s *TicketingService) GetTicket(ctx context.Context, ticketID uuid.UUID) (*domain.Ticket, error) {
	ticket, err := s.ticketRepo.GetByID(ctx, ticketID)
	if err != nil {
		s.logger.Error(ctx, "Failed to get ticket", "ticket_id", ticketID, "error", err)
		return nil, fmt.Errorf("failed to get ticket: %w", err)
	}

	return ticket, nil
}
