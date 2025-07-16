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

// SeatRepository implements repository.SeatRepository using Redis
type SeatRepository struct {
	client *redis.Client
}

// NewSeatRepository creates a new SeatRepository
func NewSeatRepository(client *redis.Client) *SeatRepository {
	return &SeatRepository{
		client: client,
	}
}

// Compile-time check to ensure SeatRepository implements repository.SeatRepository
var _ repository.SeatRepository = (*SeatRepository)(nil)

// Create creates a new seat
func (r *SeatRepository) Create(ctx context.Context, seat *domain.Seat) error {
	seat.CreatedAt = time.Now()
	seat.UpdatedAt = time.Now()

	data, err := json.Marshal(seat)
	if err != nil {
		return fmt.Errorf("failed to marshal seat: %w", err)
	}

	key := fmt.Sprintf("seat:%s", seat.ID.String())

	// Set the seat data
	if err := r.client.GetRedisClient().Set(ctx, key, data, 0).Err(); err != nil {
		return fmt.Errorf("failed to create seat: %w", err)
	}

	// Add to event seats index
	eventSeatsKey := fmt.Sprintf("event_seats:%s", seat.EventID.String())
	if err := r.client.GetRedisClient().SAdd(ctx, eventSeatsKey, seat.ID.String()).Err(); err != nil {
		return fmt.Errorf("failed to add to event seats: %w", err)
	}

	// Add to section index
	sectionKey := fmt.Sprintf("event_seats:%s:section:%s", seat.EventID.String(), seat.Section)
	if err := r.client.GetRedisClient().SAdd(ctx, sectionKey, seat.ID.String()).Err(); err != nil {
		return fmt.Errorf("failed to add to section: %w", err)
	}

	// Add to available seats if available
	if seat.IsAvailable() {
		availableKey := fmt.Sprintf("event_seats:%s:available", seat.EventID.String())
		if err := r.client.GetRedisClient().SAdd(ctx, availableKey, seat.ID.String()).Err(); err != nil {
			return fmt.Errorf("failed to add to available seats: %w", err)
		}
	}

	return nil
}

// CreateBatch creates multiple seats in a single transaction
func (r *SeatRepository) CreateBatch(ctx context.Context, seats []*domain.Seat) error {
	if len(seats) == 0 {
		return nil
	}

	pipe := r.client.GetRedisClient().Pipeline()

	for _, seat := range seats {
		seat.CreatedAt = time.Now()
		seat.UpdatedAt = time.Now()

		data, err := json.Marshal(seat)
		if err != nil {
			return fmt.Errorf("failed to marshal seat: %w", err)
		}

		key := fmt.Sprintf("seat:%s", seat.ID.String())
		pipe.Set(ctx, key, data, 0)

		// Add to event seats index
		eventSeatsKey := fmt.Sprintf("event_seats:%s", seat.EventID.String())
		pipe.SAdd(ctx, eventSeatsKey, seat.ID.String())

		// Add to section index
		sectionKey := fmt.Sprintf("event_seats:%s:section:%s", seat.EventID.String(), seat.Section)
		pipe.SAdd(ctx, sectionKey, seat.ID.String())

		// Add to available seats if available
		if seat.IsAvailable() {
			availableKey := fmt.Sprintf("event_seats:%s:available", seat.EventID.String())
			pipe.SAdd(ctx, availableKey, seat.ID.String())
		}
	}

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("failed to create seats batch: %w", err)
	}

	return nil
}

// GetByID retrieves a seat by its ID
func (r *SeatRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Seat, error) {
	key := fmt.Sprintf("seat:%s", id.String())

	data, err := r.client.GetRedisClient().Get(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get seat: %w", err)
	}

	var seat domain.Seat
	if err := json.Unmarshal([]byte(data), &seat); err != nil {
		return nil, fmt.Errorf("failed to unmarshal seat: %w", err)
	}

	return &seat, nil
}

// GetByEventID retrieves all seats for an event
func (r *SeatRepository) GetByEventID(ctx context.Context, eventID uuid.UUID) ([]*domain.Seat, error) {
	eventSeatsKey := fmt.Sprintf("event_seats:%s", eventID.String())

	members, err := r.client.GetRedisClient().SMembers(ctx, eventSeatsKey).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get event seats: %w", err)
	}

	var seats []*domain.Seat
	for _, member := range members {
		seatID, err := uuid.Parse(member)
		if err != nil {
			continue
		}

		seat, err := r.GetByID(ctx, seatID)
		if err != nil {
			continue
		}

		seats = append(seats, seat)
	}

	return seats, nil
}

// GetAvailableByEventID retrieves available seats for an event
func (r *SeatRepository) GetAvailableByEventID(ctx context.Context, eventID uuid.UUID) ([]*domain.Seat, error) {
	availableKey := fmt.Sprintf("event_seats:%s:available", eventID.String())

	members, err := r.client.GetRedisClient().SMembers(ctx, availableKey).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get available seats: %w", err)
	}

	var seats []*domain.Seat
	for _, member := range members {
		seatID, err := uuid.Parse(member)
		if err != nil {
			continue
		}

		seat, err := r.GetByID(ctx, seatID)
		if err != nil {
			continue
		}

		seats = append(seats, seat)
	}

	return seats, nil
}

// GetBySection retrieves seats by section
func (r *SeatRepository) GetBySection(ctx context.Context, eventID uuid.UUID, section string) ([]*domain.Seat, error) {
	sectionKey := fmt.Sprintf("event_seats:%s:section:%s", eventID.String(), section)

	members, err := r.client.GetRedisClient().SMembers(ctx, sectionKey).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get section seats: %w", err)
	}

	var seats []*domain.Seat
	for _, member := range members {
		seatID, err := uuid.Parse(member)
		if err != nil {
			continue
		}

		seat, err := r.GetByID(ctx, seatID)
		if err != nil {
			continue
		}

		seats = append(seats, seat)
	}

	return seats, nil
}

// Update updates an existing seat
func (r *SeatRepository) Update(ctx context.Context, seat *domain.Seat) error {
	seat.UpdatedAt = time.Now()

	data, err := json.Marshal(seat)
	if err != nil {
		return fmt.Errorf("failed to marshal seat: %w", err)
	}

	key := fmt.Sprintf("seat:%s", seat.ID.String())

	if err := r.client.GetRedisClient().Set(ctx, key, data, 0).Err(); err != nil {
		return fmt.Errorf("failed to update seat: %w", err)
	}

	return nil
}

// UpdateStatus updates seat status
func (r *SeatRepository) UpdateStatus(ctx context.Context, seatID uuid.UUID, status string) error {
	seat, err := r.GetByID(ctx, seatID)
	if err != nil {
		return fmt.Errorf("failed to get seat: %w", err)
	}

	oldStatus := seat.Status
	seat.Status = status

	// Update the seat
	if err := r.Update(ctx, seat); err != nil {
		return fmt.Errorf("failed to update seat: %w", err)
	}

	// Update availability index
	availableKey := fmt.Sprintf("event_seats:%s:available", seat.EventID.String())

	if oldStatus == string(domain.SeatStatusAvailable) && status != string(domain.SeatStatusAvailable) {
		// Remove from available
		if err := r.client.GetRedisClient().SRem(ctx, availableKey, seatID.String()).Err(); err != nil {
			return fmt.Errorf("failed to remove from available seats: %w", err)
		}
	} else if oldStatus != string(domain.SeatStatusAvailable) && status == string(domain.SeatStatusAvailable) {
		// Add to available
		if err := r.client.GetRedisClient().SAdd(ctx, availableKey, seatID.String()).Err(); err != nil {
			return fmt.Errorf("failed to add to available seats: %w", err)
		}
	}

	return nil
}

// ReserveSeats reserves multiple seats atomically
func (r *SeatRepository) ReserveSeats(ctx context.Context, seatIDs []uuid.UUID) error {
	// Use Lua script for atomic operation
	script := `
		local seats = {}
		for i, seatKey in ipairs(KEYS) do
			local seatData = redis.call('GET', seatKey)
			if seatData == false then
				return {err = "seat not found: " .. seatKey}
			end
			
			local seat = cjson.decode(seatData)
			if seat.status ~= "available" then
				return {err = "seat not available: " .. seatKey}
			end
			
			seat.status = "reserved"
			seat.updated_at = ARGV[1]
			
			redis.call('SET', seatKey, cjson.encode(seat))
			redis.call('SREM', 'event_seats:' .. seat.event_id .. ':available', seat.id)
			
			table.insert(seats, seat)
		end
		
		return {ok = "reserved " .. #seats .. " seats"}
	`

	keys := make([]string, len(seatIDs))
	for i, id := range seatIDs {
		keys[i] = fmt.Sprintf("seat:%s", id.String())
	}

	args := []interface{}{time.Now().Format(time.RFC3339)}

	result, err := r.client.GetRedisClient().Eval(ctx, script, keys, args...).Result()
	if err != nil {
		return fmt.Errorf("failed to reserve seats: %w", err)
	}

	if resultMap, ok := result.(map[string]interface{}); ok {
		if errMsg, exists := resultMap["err"]; exists {
			return fmt.Errorf("reservation failed: %v", errMsg)
		}
	}

	return nil
}

// ReleaseSeats releases reserved seats atomically
func (r *SeatRepository) ReleaseSeats(ctx context.Context, seatIDs []uuid.UUID) error {
	// Use Lua script for atomic operation
	script := `
		local seats = {}
		for i, seatKey in ipairs(KEYS) do
			local seatData = redis.call('GET', seatKey)
			if seatData == false then
				return {err = "seat not found: " .. seatKey}
			end
			
			local seat = cjson.decode(seatData)
			if seat.status ~= "reserved" then
				return {err = "seat not reserved: " .. seatKey}
			end
			
			seat.status = "available"
			seat.updated_at = ARGV[1]
			
			redis.call('SET', seatKey, cjson.encode(seat))
			redis.call('SADD', 'event_seats:' .. seat.event_id .. ':available', seat.id)
			
			table.insert(seats, seat)
		end
		
		return {ok = "released " .. #seats .. " seats"}
	`

	keys := make([]string, len(seatIDs))
	for i, id := range seatIDs {
		keys[i] = fmt.Sprintf("seat:%s", id.String())
	}

	args := []interface{}{time.Now().Format(time.RFC3339)}

	result, err := r.client.GetRedisClient().Eval(ctx, script, keys, args...).Result()
	if err != nil {
		return fmt.Errorf("failed to release seats: %w", err)
	}

	if resultMap, ok := result.(map[string]interface{}); ok {
		if errMsg, exists := resultMap["err"]; exists {
			return fmt.Errorf("release failed: %v", errMsg)
		}
	}

	return nil
}

// Delete deletes a seat by its ID
func (r *SeatRepository) Delete(ctx context.Context, id uuid.UUID) error {
	seat, err := r.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to get seat: %w", err)
	}

	key := fmt.Sprintf("seat:%s", id.String())

	// Remove from Redis
	if err := r.client.GetRedisClient().Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("failed to delete seat: %w", err)
	}

	// Remove from indexes
	idStr := id.String()
	eventSeatsKey := fmt.Sprintf("event_seats:%s", seat.EventID.String())
	sectionKey := fmt.Sprintf("event_seats:%s:section:%s", seat.EventID.String(), seat.Section)
	availableKey := fmt.Sprintf("event_seats:%s:available", seat.EventID.String())

	pipe := r.client.GetRedisClient().Pipeline()
	pipe.SRem(ctx, eventSeatsKey, idStr)
	pipe.SRem(ctx, sectionKey, idStr)
	pipe.SRem(ctx, availableKey, idStr)

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("failed to remove from indexes: %w", err)
	}

	return nil
}

// DeleteByEventID deletes all seats for an event
func (r *SeatRepository) DeleteByEventID(ctx context.Context, eventID uuid.UUID) error {
	seats, err := r.GetByEventID(ctx, eventID)
	if err != nil {
		return fmt.Errorf("failed to get event seats: %w", err)
	}

	if len(seats) == 0 {
		return nil
	}

	pipe := r.client.GetRedisClient().Pipeline()

	for _, seat := range seats {
		key := fmt.Sprintf("seat:%s", seat.ID.String())
		pipe.Del(ctx, key)
	}

	// Remove indexes
	eventSeatsKey := fmt.Sprintf("event_seats:%s", eventID.String())
	availableKey := fmt.Sprintf("event_seats:%s:available", eventID.String())

	pipe.Del(ctx, eventSeatsKey)
	pipe.Del(ctx, availableKey)

	// Remove section indexes (this is a simplified approach)
	sections := make(map[string]bool)
	for _, seat := range seats {
		sections[seat.Section] = true
	}

	for section := range sections {
		sectionKey := fmt.Sprintf("event_seats:%s:section:%s", eventID.String(), section)
		pipe.Del(ctx, sectionKey)
	}

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("failed to delete event seats: %w", err)
	}

	return nil
}
