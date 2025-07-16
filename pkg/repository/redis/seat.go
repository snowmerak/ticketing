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
	cmd := r.client.GetRedisClient().B().Set().Key(key).Value(string(data)).Build()
	if err := r.client.GetRedisClient().Do(ctx, cmd).Error(); err != nil {
		return fmt.Errorf("failed to create seat: %w", err)
	}

	// Add to event seats index
	eventSeatsKey := fmt.Sprintf("event_seats:%s", seat.EventID.String())
	saddCmd := r.client.GetRedisClient().B().Sadd().Key(eventSeatsKey).Member(seat.ID.String()).Build()
	if err := r.client.GetRedisClient().Do(ctx, saddCmd).Error(); err != nil {
		return fmt.Errorf("failed to add to event seats: %w", err)
	}

	// Add to section index
	sectionKey := fmt.Sprintf("section:%s:%s", seat.EventID.String(), seat.Section)
	sectionCmd := r.client.GetRedisClient().B().Sadd().Key(sectionKey).Member(seat.ID.String()).Build()
	if err := r.client.GetRedisClient().Do(ctx, sectionCmd).Error(); err != nil {
		return fmt.Errorf("failed to add to section: %w", err)
	}

	// Add to available seats if available
	if seat.Status == string(domain.SeatStatusAvailable) {
		availableKey := fmt.Sprintf("available_seats:%s", seat.EventID.String())
		availableCmd := r.client.GetRedisClient().B().Sadd().Key(availableKey).Member(seat.ID.String()).Build()
		if err := r.client.GetRedisClient().Do(ctx, availableCmd).Error(); err != nil {
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

	// Create all seats individually for simplicity
	for _, seat := range seats {
		if err := r.Create(ctx, seat); err != nil {
			return fmt.Errorf("failed to create seat %s: %w", seat.ID.String(), err)
		}
	}

	return nil
}

// GetByID retrieves a seat by its ID
func (r *SeatRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Seat, error) {
	key := fmt.Sprintf("seat:%s", id.String())

	const clientSideCacheTTL = 30 * time.Minute // moderate TTL for seat data
	cmd := r.client.GetRedisClient().B().Get().Key(key).Cache()
	result := r.client.GetRedisClient().DoCache(ctx, cmd, clientSideCacheTTL)
	if result.Error() != nil {
		return nil, fmt.Errorf("failed to get seat: %w", result.Error())
	}

	data, err := result.ToString()
	if err != nil {
		return nil, fmt.Errorf("failed to get seat data: %w", err)
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

	const clientSideCacheTTL = 10 * time.Minute // shorter TTL for seat lists
	cmd := r.client.GetRedisClient().B().Smembers().Key(eventSeatsKey).Cache()
	result := r.client.GetRedisClient().DoCache(ctx, cmd, clientSideCacheTTL)
	if result.Error() != nil {
		return nil, fmt.Errorf("failed to get event seats: %w", result.Error())
	}

	members, err := result.AsStrSlice()
	if err != nil {
		return nil, fmt.Errorf("failed to parse members: %w", err)
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
	availableKey := fmt.Sprintf("available_seats:%s", eventID.String())

	cmd := r.client.GetRedisClient().B().Smembers().Key(availableKey).Build()
	result := r.client.GetRedisClient().Do(ctx, cmd)
	if result.Error() != nil {
		return nil, fmt.Errorf("failed to get available seats: %w", result.Error())
	}

	members, err := result.AsStrSlice()
	if err != nil {
		return nil, fmt.Errorf("failed to parse members: %w", err)
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
	sectionKey := fmt.Sprintf("section:%s:%s", eventID.String(), section)

	cmd := r.client.GetRedisClient().B().Smembers().Key(sectionKey).Build()
	result := r.client.GetRedisClient().Do(ctx, cmd)
	if result.Error() != nil {
		return nil, fmt.Errorf("failed to get section seats: %w", result.Error())
	}

	members, err := result.AsStrSlice()
	if err != nil {
		return nil, fmt.Errorf("failed to parse members: %w", err)
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

	// Update the seat data
	cmd := r.client.GetRedisClient().B().Set().Key(key).Value(string(data)).Build()
	if err := r.client.GetRedisClient().Do(ctx, cmd).Error(); err != nil {
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

	// Update available seats index
	availableKey := fmt.Sprintf("available_seats:%s", seat.EventID.String())

	if oldStatus == string(domain.SeatStatusAvailable) && status != string(domain.SeatStatusAvailable) {
		// Remove from available seats
		remCmd := r.client.GetRedisClient().B().Srem().Key(availableKey).Member(seatID.String()).Build()
		if err := r.client.GetRedisClient().Do(ctx, remCmd).Error(); err != nil {
			return fmt.Errorf("failed to remove from available seats: %w", err)
		}
	} else if oldStatus != string(domain.SeatStatusAvailable) && status == string(domain.SeatStatusAvailable) {
		// Add to available seats
		addCmd := r.client.GetRedisClient().B().Sadd().Key(availableKey).Member(seatID.String()).Build()
		if err := r.client.GetRedisClient().Do(ctx, addCmd).Error(); err != nil {
			return fmt.Errorf("failed to add to available seats: %w", err)
		}
	}

	return r.Update(ctx, seat)
}

// ReserveSeats reserves multiple seats atomically
func (r *SeatRepository) ReserveSeats(ctx context.Context, seatIDs []uuid.UUID) error {
	// Use Lua script for atomic operation
	script := `
		local seats = {}
		for i, seatKey in ipairs(KEYS) do
			local seatData = redis.call('GET', seatKey)
			if seatData == false then
				return 'seat_not_found'
			end
			
			local seat = cjson.decode(seatData)
			if seat.status ~= 'available' then
				return 'seat_not_available'
			end
			
			seat.status = 'reserved'
			seat.updated_at = ARGV[1]
			seats[i] = {key = seatKey, data = cjson.encode(seat), id = seat.id, event_id = seat.event_id}
		end
		
		for i, seat in ipairs(seats) do
			redis.call('SET', seat.key, seat.data)
			redis.call('SREM', 'available_seats:' .. seat.event_id, seat.id)
		end
		
		return 'success'
	`

	var keys []string
	for _, seatID := range seatIDs {
		keys = append(keys, fmt.Sprintf("seat:%s", seatID.String()))
	}

	now := time.Now().Format(time.RFC3339)
	cmd := r.client.GetRedisClient().B().Eval().Script(script).Numkeys(int64(len(keys))).Key(keys...).Arg(now).Build()
	result := r.client.GetRedisClient().Do(ctx, cmd)
	if result.Error() != nil {
		return fmt.Errorf("failed to reserve seats: %w", result.Error())
	}

	resultStr, err := result.ToString()
	if err != nil {
		return fmt.Errorf("failed to get result: %w", err)
	}

	if resultStr == "seat_not_found" {
		return fmt.Errorf("one or more seats not found")
	}
	if resultStr == "seat_not_available" {
		return fmt.Errorf("one or more seats not available")
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
				return 'seat_not_found'
			end
			
			local seat = cjson.decode(seatData)
			if seat.status ~= 'reserved' then
				return 'seat_not_reserved'
			end
			
			seat.status = 'available'
			seat.updated_at = ARGV[1]
			seats[i] = {key = seatKey, data = cjson.encode(seat), id = seat.id, event_id = seat.event_id}
		end
		
		for i, seat in ipairs(seats) do
			redis.call('SET', seat.key, seat.data)
			redis.call('SADD', 'available_seats:' .. seat.event_id, seat.id)
		end
		
		return 'success'
	`

	var keys []string
	for _, seatID := range seatIDs {
		keys = append(keys, fmt.Sprintf("seat:%s", seatID.String()))
	}

	now := time.Now().Format(time.RFC3339)
	cmd := r.client.GetRedisClient().B().Eval().Script(script).Numkeys(int64(len(keys))).Key(keys...).Arg(now).Build()
	result := r.client.GetRedisClient().Do(ctx, cmd)
	if result.Error() != nil {
		return fmt.Errorf("failed to release seats: %w", result.Error())
	}

	resultStr, err := result.ToString()
	if err != nil {
		return fmt.Errorf("failed to get result: %w", err)
	}

	if resultStr == "seat_not_found" {
		return fmt.Errorf("one or more seats not found")
	}
	if resultStr == "seat_not_reserved" {
		return fmt.Errorf("one or more seats not reserved")
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
	delCmd := r.client.GetRedisClient().B().Del().Key(key).Build()
	if err := r.client.GetRedisClient().Do(ctx, delCmd).Error(); err != nil {
		return fmt.Errorf("failed to delete seat: %w", err)
	}

	// Remove from indexes
	idStr := id.String()
	eventSeatsKey := fmt.Sprintf("event_seats:%s", seat.EventID.String())
	eventRemCmd := r.client.GetRedisClient().B().Srem().Key(eventSeatsKey).Member(idStr).Build()
	if err := r.client.GetRedisClient().Do(ctx, eventRemCmd).Error(); err != nil {
		return fmt.Errorf("failed to remove from event seats: %w", err)
	}

	sectionKey := fmt.Sprintf("section:%s:%s", seat.EventID.String(), seat.Section)
	sectionRemCmd := r.client.GetRedisClient().B().Srem().Key(sectionKey).Member(idStr).Build()
	if err := r.client.GetRedisClient().Do(ctx, sectionRemCmd).Error(); err != nil {
		return fmt.Errorf("failed to remove from section: %w", err)
	}

	availableKey := fmt.Sprintf("available_seats:%s", seat.EventID.String())
	availableRemCmd := r.client.GetRedisClient().B().Srem().Key(availableKey).Member(idStr).Build()
	if err := r.client.GetRedisClient().Do(ctx, availableRemCmd).Error(); err != nil {
		return fmt.Errorf("failed to remove from available seats: %w", err)
	}

	return nil
}

// DeleteByEventID deletes all seats for an event
func (r *SeatRepository) DeleteByEventID(ctx context.Context, eventID uuid.UUID) error {
	seats, err := r.GetByEventID(ctx, eventID)
	if err != nil {
		return fmt.Errorf("failed to get event seats: %w", err)
	}

	for _, seat := range seats {
		if err := r.Delete(ctx, seat.ID); err != nil {
			return fmt.Errorf("failed to delete seat %s: %w", seat.ID.String(), err)
		}
	}

	return nil
}
