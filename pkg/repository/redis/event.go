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

// EventRepository implements repository.EventRepository using Redis
type EventRepository struct {
	client *redis.Client
}

// NewEventRepository creates a new EventRepository
func NewEventRepository(client *redis.Client) *EventRepository {
	return &EventRepository{
		client: client,
	}
}

// Compile-time check to ensure EventRepository implements repository.EventRepository
var _ repository.EventRepository = (*EventRepository)(nil)

// Create creates a new event
func (r *EventRepository) Create(ctx context.Context, event *domain.Event) error {
	event.CreatedAt = time.Now()
	event.UpdatedAt = time.Now()

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	key := fmt.Sprintf("event:%s", event.ID.String())

	// Set the event data
	cmd := r.client.GetRedisClient().B().Set().Key(key).Value(string(data)).Build()
	if err := r.client.GetRedisClient().Do(ctx, cmd).Error(); err != nil {
		return fmt.Errorf("failed to create event: %w", err)
	}

	// Add to active events index if active
	if event.Status == string(domain.EventStatusActive) {
		addCmd := r.client.GetRedisClient().B().Sadd().Key("events:active").Member(event.ID.String()).Build()
		if err := r.client.GetRedisClient().Do(ctx, addCmd).Error(); err != nil {
			return fmt.Errorf("failed to add to active events: %w", err)
		}
	}

	// Add to all events index
	allCmd := r.client.GetRedisClient().B().Sadd().Key("events:all").Member(event.ID.String()).Build()
	if err := r.client.GetRedisClient().Do(ctx, allCmd).Error(); err != nil {
		return fmt.Errorf("failed to add to all events: %w", err)
	}

	return nil
}

// GetByID retrieves an event by its ID
func (r *EventRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Event, error) {
	key := fmt.Sprintf("event:%s", id.String())

	const clientSideCacheTTL = 1 * time.Hour
	cmd := r.client.GetRedisClient().B().Get().Key(key).Cache()
	result := r.client.GetRedisClient().DoCache(ctx, cmd, clientSideCacheTTL)
	if result.Error() != nil {
		return nil, fmt.Errorf("failed to get event: %w", result.Error())
	}

	data, err := result.ToString()
	if err != nil {
		return nil, fmt.Errorf("failed to get event data: %w", err)
	}

	var event domain.Event
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return nil, fmt.Errorf("failed to unmarshal event: %w", err)
	}

	return &event, nil
}

// Update updates an existing event
func (r *EventRepository) Update(ctx context.Context, event *domain.Event) error {
	event.UpdatedAt = time.Now()

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}

	key := fmt.Sprintf("event:%s", event.ID.String())

	// Update the event data
	cmd := r.client.GetRedisClient().B().Set().Key(key).Value(string(data)).Build()
	if err := r.client.GetRedisClient().Do(ctx, cmd).Error(); err != nil {
		return fmt.Errorf("failed to update event: %w", err)
	}

	// Update active events index
	if event.Status == string(domain.EventStatusActive) {
		addCmd := r.client.GetRedisClient().B().Sadd().Key("events:active").Member(event.ID.String()).Build()
		if err := r.client.GetRedisClient().Do(ctx, addCmd).Error(); err != nil {
			return fmt.Errorf("failed to add to active events: %w", err)
		}
	} else {
		remCmd := r.client.GetRedisClient().B().Srem().Key("events:active").Member(event.ID.String()).Build()
		if err := r.client.GetRedisClient().Do(ctx, remCmd).Error(); err != nil {
			return fmt.Errorf("failed to remove from active events: %w", err)
		}
	}

	return nil
}

// Delete deletes an event by its ID
func (r *EventRepository) Delete(ctx context.Context, id uuid.UUID) error {
	key := fmt.Sprintf("event:%s", id.String())

	// Remove from Redis
	delCmd := r.client.GetRedisClient().B().Del().Key(key).Build()
	if err := r.client.GetRedisClient().Do(ctx, delCmd).Error(); err != nil {
		return fmt.Errorf("failed to delete event: %w", err)
	}

	// Remove from indexes
	idStr := id.String()
	allRemCmd := r.client.GetRedisClient().B().Srem().Key("events:all").Member(idStr).Build()
	if err := r.client.GetRedisClient().Do(ctx, allRemCmd).Error(); err != nil {
		return fmt.Errorf("failed to remove from all events: %w", err)
	}

	activeRemCmd := r.client.GetRedisClient().B().Srem().Key("events:active").Member(idStr).Build()
	if err := r.client.GetRedisClient().Do(ctx, activeRemCmd).Error(); err != nil {
		return fmt.Errorf("failed to remove from active events: %w", err)
	}

	return nil
}

// List retrieves all events with pagination
func (r *EventRepository) List(ctx context.Context, offset, limit int) ([]*domain.Event, error) {
	const clientSideCacheTTL = 2 * time.Minute // shorter TTL for events list
	cmd := r.client.GetRedisClient().B().Smembers().Key("events:all").Cache()
	result := r.client.GetRedisClient().DoCache(ctx, cmd, clientSideCacheTTL)
	if result.Error() != nil {
		return nil, fmt.Errorf("failed to get all events: %w", result.Error())
	}

	members, err := result.AsStrSlice()
	if err != nil {
		return nil, fmt.Errorf("failed to parse members: %w", err)
	}

	var events []*domain.Event
	start := offset
	end := offset + limit

	if start >= len(members) {
		return events, nil
	}

	if end > len(members) {
		end = len(members)
	}

	for i := start; i < end; i++ {
		eventID, err := uuid.Parse(members[i])
		if err != nil {
			continue
		}

		event, err := r.GetByID(ctx, eventID)
		if err != nil {
			continue
		}

		events = append(events, event)
	}

	return events, nil
}

// GetActiveEvents retrieves all active events
func (r *EventRepository) GetActiveEvents(ctx context.Context) ([]*domain.Event, error) {
	const clientSideCacheTTL = 5 * time.Minute // shorter TTL for active events list
	cmd := r.client.GetRedisClient().B().Smembers().Key("events:active").Cache()
	result := r.client.GetRedisClient().DoCache(ctx, cmd, clientSideCacheTTL)
	if result.Error() != nil {
		return nil, fmt.Errorf("failed to get active events: %w", result.Error())
	}

	members, err := result.AsStrSlice()
	if err != nil {
		return nil, fmt.Errorf("failed to parse members: %w", err)
	}

	var events []*domain.Event
	for _, member := range members {
		eventID, err := uuid.Parse(member)
		if err != nil {
			continue
		}

		event, err := r.GetByID(ctx, eventID)
		if err != nil {
			continue
		}

		events = append(events, event)
	}

	return events, nil
}

// UpdateAvailableTickets updates the available ticket count
func (r *EventRepository) UpdateAvailableTickets(ctx context.Context, eventID uuid.UUID, count int) error {
	event, err := r.GetByID(ctx, eventID)
	if err != nil {
		return fmt.Errorf("failed to get event: %w", err)
	}

	event.AvailableTickets = count

	return r.Update(ctx, event)
}

// DecrementAvailableTickets decrements available tickets atomically
func (r *EventRepository) DecrementAvailableTickets(ctx context.Context, eventID uuid.UUID, count int) error {
	key := fmt.Sprintf("event:%s:available_tickets", eventID.String())

	// Use Lua script for atomic decrement
	script := `
		local current = redis.call('GET', KEYS[1])
		if current == false then
			return -1
		end
		
		local currentVal = tonumber(current)
		local decrementBy = tonumber(ARGV[1])
		
		if currentVal < decrementBy then
			return -2
		end
		
		local newVal = currentVal - decrementBy
		redis.call('SET', KEYS[1], newVal)
		return newVal
	`

	cmd := r.client.GetRedisClient().B().Eval().Script(script).Numkeys(1).Key(key).Arg(fmt.Sprintf("%d", count)).Build()
	result := r.client.GetRedisClient().Do(ctx, cmd)
	if result.Error() != nil {
		return fmt.Errorf("failed to decrement available tickets: %w", result.Error())
	}

	resultVal, err := result.ToInt64()
	if err != nil {
		return fmt.Errorf("failed to parse result: %w", err)
	}

	if resultVal == -1 {
		return fmt.Errorf("event not found")
	}

	if resultVal == -2 {
		return fmt.Errorf("insufficient tickets available")
	}

	// Update the event object
	event, err := r.GetByID(ctx, eventID)
	if err != nil {
		return fmt.Errorf("failed to get event: %w", err)
	}

	event.AvailableTickets = int(resultVal)

	return r.Update(ctx, event)
}

// IncrementAvailableTickets increments available tickets atomically
func (r *EventRepository) IncrementAvailableTickets(ctx context.Context, eventID uuid.UUID, count int) error {
	key := fmt.Sprintf("event:%s:available_tickets", eventID.String())

	// Use Lua script for atomic increment
	script := `
		local current = redis.call('GET', KEYS[1])
		if current == false then
			return -1
		end
		
		local currentVal = tonumber(current)
		local incrementBy = tonumber(ARGV[1])
		
		local newVal = currentVal + incrementBy
		redis.call('SET', KEYS[1], newVal)
		return newVal
	`

	cmd := r.client.GetRedisClient().B().Eval().Script(script).Numkeys(1).Key(key).Arg(fmt.Sprintf("%d", count)).Build()
	result := r.client.GetRedisClient().Do(ctx, cmd)
	if result.Error() != nil {
		return fmt.Errorf("failed to increment available tickets: %w", result.Error())
	}

	resultVal, err := result.ToInt64()
	if err != nil {
		return fmt.Errorf("failed to parse result: %w", err)
	}

	if resultVal == -1 {
		return fmt.Errorf("event not found")
	}

	// Update the event object
	event, err := r.GetByID(ctx, eventID)
	if err != nil {
		return fmt.Errorf("failed to get event: %w", err)
	}

	event.AvailableTickets = int(resultVal)

	return r.Update(ctx, event)
}
