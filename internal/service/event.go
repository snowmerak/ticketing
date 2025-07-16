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

// EventService handles event-related business logic
type EventService struct {
	eventRepo repository.EventRepository
	seatRepo  repository.SeatRepository
	cache     adapter.Cache
	lock      adapter.Lock
	logger    adapter.Logger
}

// NewEventService creates a new EventService
func NewEventService(
	eventRepo repository.EventRepository,
	seatRepo repository.SeatRepository,
	cache adapter.Cache,
	lock adapter.Lock,
	logger adapter.Logger,
) *EventService {
	return &EventService{
		eventRepo: eventRepo,
		seatRepo:  seatRepo,
		cache:     cache,
		lock:      lock,
		logger:    logger,
	}
}

// CreateEvent creates a new event
func (s *EventService) CreateEvent(ctx context.Context, event *domain.Event) error {
	s.logger.Info(ctx, "Creating new event", "event_id", event.ID, "name", event.Name)

	// Validate event
	if err := s.validateEvent(event); err != nil {
		s.logger.Error(ctx, "Event validation failed", "error", err)
		return fmt.Errorf("event validation failed: %w", err)
	}

	// Create event
	if err := s.eventRepo.Create(ctx, event); err != nil {
		s.logger.Error(ctx, "Failed to create event", "error", err)
		return fmt.Errorf("failed to create event: %w", err)
	}

	// Cache event
	cacheKey := fmt.Sprintf("event:%s", event.ID.String())
	if err := s.cache.Set(ctx, cacheKey, event, 1*time.Hour); err != nil {
		s.logger.Warn(ctx, "Failed to cache event", "error", err)
	}

	s.logger.Info(ctx, "Event created successfully", "event_id", event.ID)
	return nil
}

// GetEvent retrieves an event by ID
func (s *EventService) GetEvent(ctx context.Context, id uuid.UUID) (*domain.Event, error) {
	// Try cache first
	cacheKey := fmt.Sprintf("event:%s", id.String())
	if cached, err := s.cache.Get(ctx, cacheKey); err == nil {
		if event, ok := cached.(*domain.Event); ok {
			return event, nil
		}
	}

	// Get from repository
	event, err := s.eventRepo.GetByID(ctx, id)
	if err != nil {
		s.logger.Error(ctx, "Failed to get event", "event_id", id, "error", err)
		return nil, fmt.Errorf("failed to get event: %w", err)
	}

	// Cache for future use
	if err := s.cache.Set(ctx, cacheKey, event, 1*time.Hour); err != nil {
		s.logger.Warn(ctx, "Failed to cache event", "error", err)
	}

	return event, nil
}

// GetActiveEvents retrieves all active events
func (s *EventService) GetActiveEvents(ctx context.Context) ([]*domain.Event, error) {
	// Try cache first
	cacheKey := "events:active"
	if cached, err := s.cache.Get(ctx, cacheKey); err == nil {
		if events, ok := cached.([]*domain.Event); ok {
			return events, nil
		}
	}

	events, err := s.eventRepo.GetActiveEvents(ctx)
	if err != nil {
		s.logger.Error(ctx, "Failed to get active events", "error", err)
		return nil, fmt.Errorf("failed to get active events: %w", err)
	}

	// Cache for 5 minutes
	if err := s.cache.Set(ctx, cacheKey, events, 5*time.Minute); err != nil {
		s.logger.Warn(ctx, "Failed to cache active events", "error", err)
	}

	return events, nil
}

// GetAllEvents retrieves all events with pagination
func (s *EventService) GetAllEvents(ctx context.Context) ([]*domain.Event, error) {
	// Try cache first
	cacheKey := "events:all"
	if cached, err := s.cache.Get(ctx, cacheKey); err == nil {
		if events, ok := cached.([]*domain.Event); ok {
			return events, nil
		}
	}

	events, err := s.eventRepo.List(ctx, 0, 100) // Get first 100 events
	if err != nil {
		s.logger.Error(ctx, "Failed to get all events", "error", err)
		return nil, fmt.Errorf("failed to get all events: %w", err)
	}

	// Cache for 2 minutes
	if err := s.cache.Set(ctx, cacheKey, events, 2*time.Minute); err != nil {
		s.logger.Warn(ctx, "Failed to cache all events", "error", err)
	}

	return events, nil
}

// UpdateEvent updates an existing event
func (s *EventService) UpdateEvent(ctx context.Context, event *domain.Event) error {
	s.logger.Info(ctx, "Updating event", "event_id", event.ID)

	// Validate event
	if err := s.validateEvent(event); err != nil {
		s.logger.Error(ctx, "Event validation failed", "error", err)
		return fmt.Errorf("event validation failed: %w", err)
	}

	// Update event
	if err := s.eventRepo.Update(ctx, event); err != nil {
		s.logger.Error(ctx, "Failed to update event", "error", err)
		return fmt.Errorf("failed to update event: %w", err)
	}

	// Invalidate cache
	cacheKey := fmt.Sprintf("event:%s", event.ID.String())
	if err := s.cache.Delete(ctx, cacheKey); err != nil {
		s.logger.Warn(ctx, "Failed to invalidate event cache", "error", err)
	}

	// Invalidate active events cache
	if err := s.cache.Delete(ctx, "events:active"); err != nil {
		s.logger.Warn(ctx, "Failed to invalidate active events cache", "error", err)
	}

	s.logger.Info(ctx, "Event updated successfully", "event_id", event.ID)
	return nil
}

// DeleteEvent deletes an event
func (s *EventService) DeleteEvent(ctx context.Context, id uuid.UUID) error {
	s.logger.Info(ctx, "Deleting event", "event_id", id)

	// Delete all seats for this event
	if err := s.seatRepo.DeleteByEventID(ctx, id); err != nil {
		s.logger.Error(ctx, "Failed to delete event seats", "error", err)
		return fmt.Errorf("failed to delete event seats: %w", err)
	}

	// Delete event
	if err := s.eventRepo.Delete(ctx, id); err != nil {
		s.logger.Error(ctx, "Failed to delete event", "error", err)
		return fmt.Errorf("failed to delete event: %w", err)
	}

	// Invalidate cache
	cacheKey := fmt.Sprintf("event:%s", id.String())
	if err := s.cache.Delete(ctx, cacheKey); err != nil {
		s.logger.Warn(ctx, "Failed to invalidate event cache", "error", err)
	}

	// Invalidate active events cache
	if err := s.cache.Delete(ctx, "events:active"); err != nil {
		s.logger.Warn(ctx, "Failed to invalidate active events cache", "error", err)
	}

	s.logger.Info(ctx, "Event deleted successfully", "event_id", id)
	return nil
}

// CreateSeatsForEvent creates seats for an event
func (s *EventService) CreateSeatsForEvent(ctx context.Context, eventID uuid.UUID, seats []*domain.Seat) error {
	s.logger.Info(ctx, "Creating seats for event", "event_id", eventID, "seat_count", len(seats))

	// Validate that event exists
	event, err := s.GetEvent(ctx, eventID)
	if err != nil {
		return fmt.Errorf("failed to get event: %w", err)
	}

	if !event.IsSeatedEvent {
		return fmt.Errorf("event is not a seated event")
	}

	// Set event ID for all seats
	for _, seat := range seats {
		seat.EventID = eventID
		seat.CreatedAt = time.Now()
		seat.UpdatedAt = time.Now()
	}

	// Create seats in batch
	if err := s.seatRepo.CreateBatch(ctx, seats); err != nil {
		s.logger.Error(ctx, "Failed to create seats", "error", err)
		return fmt.Errorf("failed to create seats: %w", err)
	}

	s.logger.Info(ctx, "Seats created successfully", "event_id", eventID, "seat_count", len(seats))
	return nil
}

// GetAvailableSeats retrieves available seats for an event
func (s *EventService) GetAvailableSeats(ctx context.Context, eventID uuid.UUID) ([]*domain.Seat, error) {
	// Try cache first
	cacheKey := fmt.Sprintf("seats:available:%s", eventID.String())
	if cached, err := s.cache.Get(ctx, cacheKey); err == nil {
		if seats, ok := cached.([]*domain.Seat); ok {
			return seats, nil
		}
	}

	seats, err := s.seatRepo.GetAvailableByEventID(ctx, eventID)
	if err != nil {
		s.logger.Error(ctx, "Failed to get available seats", "event_id", eventID, "error", err)
		return nil, fmt.Errorf("failed to get available seats: %w", err)
	}

	// Cache for 1 minute (frequently changing data)
	if err := s.cache.Set(ctx, cacheKey, seats, 1*time.Minute); err != nil {
		s.logger.Warn(ctx, "Failed to cache available seats", "error", err)
	}

	return seats, nil
}

// validateEvent validates an event
func (s *EventService) validateEvent(event *domain.Event) error {
	if event.Name == "" {
		return fmt.Errorf("event name is required")
	}

	if event.StartTime.IsZero() {
		return fmt.Errorf("event start time is required")
	}

	if event.EndTime.IsZero() {
		return fmt.Errorf("event end time is required")
	}

	if event.StartTime.After(event.EndTime) {
		return fmt.Errorf("event start time must be before end time")
	}

	if event.TotalTickets < 0 {
		return fmt.Errorf("total tickets must be non-negative")
	}

	if event.AvailableTickets < 0 {
		return fmt.Errorf("available tickets must be non-negative")
	}

	if event.AvailableTickets > event.TotalTickets {
		return fmt.Errorf("available tickets cannot exceed total tickets")
	}

	return nil
}
