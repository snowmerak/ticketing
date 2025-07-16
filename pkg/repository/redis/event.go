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
	if err := r.client.GetRedisClient().Set(ctx, key, data, 0).Err(); err != nil {
		return fmt.Errorf("failed to create event: %w", err)
	}

	// Add to active events index if active
	if event.Status == string(domain.EventStatusActive) {
		if err := r.client.GetRedisClient().SAdd(ctx, "events:active", event.ID.String()).Err(); err != nil {
			return fmt.Errorf("failed to add to active events: %w", err)
		}
	}

	// Add to all events index
	if err := r.client.GetRedisClient().SAdd(ctx, "events:all", event.ID.String()).Err(); err != nil {
		return fmt.Errorf("failed to add to all events: %w", err)
	}

	return nil
}

// GetByID retrieves an event by its ID
func (r *EventRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Event, error) {
	key := fmt.Sprintf("event:%s", id.String())

	data, err := r.client.GetRedisClient().Get(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get event: %w", err)
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
	if err := r.client.GetRedisClient().Set(ctx, key, data, 0).Err(); err != nil {
		return fmt.Errorf("failed to update event: %w", err)
	}

	// Update active events index
	if event.Status == string(domain.EventStatusActive) {
		if err := r.client.GetRedisClient().SAdd(ctx, "events:active", event.ID.String()).Err(); err != nil {
			return fmt.Errorf("failed to add to active events: %w", err)
		}
	} else {
		if err := r.client.GetRedisClient().SRem(ctx, "events:active", event.ID.String()).Err(); err != nil {
			return fmt.Errorf("failed to remove from active events: %w", err)
		}
	}

	return nil
}

// Delete deletes an event by its ID
func (r *EventRepository) Delete(ctx context.Context, id uuid.UUID) error {
	key := fmt.Sprintf("event:%s", id.String())

	// Remove from Redis
	if err := r.client.GetRedisClient().Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("failed to delete event: %w", err)
	}

	// Remove from indexes
	idStr := id.String()
	if err := r.client.GetRedisClient().SRem(ctx, "events:all", idStr).Err(); err != nil {
		return fmt.Errorf("failed to remove from all events: %w", err)
	}

	if err := r.client.GetRedisClient().SRem(ctx, "events:active", idStr).Err(); err != nil {
		return fmt.Errorf("failed to remove from active events: %w", err)
	}

	return nil
}

// List retrieves all events with pagination
func (r *EventRepository) List(ctx context.Context, offset, limit int) ([]*domain.Event, error) {
	members, err := r.client.GetRedisClient().SMembers(ctx, "events:all").Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get all events: %w", err)
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
	members, err := r.client.GetRedisClient().SMembers(ctx, "events:active").Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get active events: %w", err)
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

	result, err := r.client.GetRedisClient().Eval(ctx, script, []string{key}, count).Result()
	if err != nil {
		return fmt.Errorf("failed to decrement available tickets: %w", err)
	}

	if result == -1 {
		return fmt.Errorf("event not found")
	}

	if result == -2 {
		return fmt.Errorf("insufficient tickets available")
	}

	// Update the event object
	event, err := r.GetByID(ctx, eventID)
	if err != nil {
		return fmt.Errorf("failed to get event: %w", err)
	}

	event.AvailableTickets = int(result.(int64))

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

	result, err := r.client.GetRedisClient().Eval(ctx, script, []string{key}, count).Result()
	if err != nil {
		return fmt.Errorf("failed to increment available tickets: %w", err)
	}

	if result == -1 {
		return fmt.Errorf("event not found")
	}

	// Update the event object
	event, err := r.GetByID(ctx, eventID)
	if err != nil {
		return fmt.Errorf("failed to get event: %w", err)
	}

	event.AvailableTickets = int(result.(int64))

	return r.Update(ctx, event)
}
