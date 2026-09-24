package booking

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

type SeatCache struct {
	store  *Store
	events []uint64
	logger *slog.Logger

	mu        sync.RWMutex
	snapshots map[uint64]SeatMap
}

func NewSeatCache(store *Store, events []uint64, logger *slog.Logger) *SeatCache {
	return &SeatCache{store: store, events: append([]uint64(nil), events...), logger: logger, snapshots: make(map[uint64]SeatMap)}
}

func (c *SeatCache) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	c.refresh(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.refresh(ctx)
		}
	}
}

func (c *SeatCache) Get(eventID uint64) (SeatMap, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	snapshot, ok := c.snapshots[eventID]
	return snapshot, ok
}

func (c *SeatCache) refresh(ctx context.Context) {
	for _, eventID := range c.events {
		snapshot, err := c.store.LoadSeatMap(ctx, eventID)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				c.logger.Warn("seat_snapshot_failed", "event_id", eventID, "error", err)
			}
			continue
		}
		c.mu.Lock()
		c.snapshots[eventID] = snapshot
		c.mu.Unlock()
	}
}
