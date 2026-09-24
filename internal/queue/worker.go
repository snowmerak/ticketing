package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/snowmerak/ticketing/internal/config"
)

type DependencyHealth func(context.Context) error

type Worker struct {
	store      *Store
	cfg        config.Config
	owner      string
	dependency DependencyHealth
	logger     *slog.Logger
}

func NewWorker(store *Store, cfg config.Config, owner string, dependency DependencyHealth, logger *slog.Logger) *Worker {
	return &Worker{store: store, cfg: cfg, owner: owner, dependency: dependency, logger: logger}
}

func (w *Worker) Run(ctx context.Context) {
	scheduler := time.NewTicker(w.cfg.SchedulerInterval)
	stats := time.NewTicker(w.cfg.StatsInterval)
	reaper := time.NewTicker(time.Second)
	defer scheduler.Stop()
	defer stats.Stop()
	defer reaper.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-scheduler.C:
			for _, eventID := range w.cfg.EventIDs {
				if err := w.scheduleEvent(ctx, eventID); err != nil && !errors.Is(err, context.Canceled) {
					w.logger.Warn("admission_tick_failed", "event_id", eventID, "error", err)
				}
			}
		case <-stats.C:
			for _, eventID := range w.cfg.EventIDs {
				epoch, err := w.store.ActiveEpoch(ctx, eventID)
				if err == nil && epoch != "" {
					if err := w.store.RefreshStats(ctx, eventID, epoch); err != nil {
						w.logger.Warn("queue_stats_failed", "event_id", eventID, "error", err)
					}
				}
			}
		case <-reaper.C:
			for _, eventID := range w.cfg.EventIDs {
				if err := w.store.Sweep(ctx, eventID, 100); err != nil && !errors.Is(err, context.Canceled) {
					w.logger.Warn("redis_reaper_failed", "event_id", eventID, "error", err)
				}
			}
		}
	}
}

func (w *Worker) scheduleEvent(ctx context.Context, eventID uint64) error {
	if w.dependency != nil {
		healthCtx, cancel := context.WithTimeout(ctx, w.cfg.DependencyTimeout)
		err := w.dependency(healthCtx)
		cancel()
		if err != nil {
			return nil
		}
	}
	epoch, err := w.store.ActiveEpoch(ctx, eventID)
	if err != nil || epoch == "" {
		return err
	}
	fence, acquired, err := w.store.AcquireLease(ctx, eventID, w.owner)
	if err != nil || !acquired {
		return err
	}
	candidates, err := w.store.Candidates(ctx, eventID, epoch, w.cfg.MaxGrantsPerTick)
	if err != nil {
		return err
	}
	for _, seq := range candidates {
		if _, ok, err := w.store.Claim(ctx, eventID, epoch, w.owner, fence, seq); err != nil {
			return err
		} else if !ok {
			continue
		}
	}
	return nil
}

func (s *Store) Sweep(ctx context.Context, eventID uint64, limit int64) error {
	now, err := s.client.Time(ctx).Result()
	if err != nil {
		return err
	}
	grantIDs, err := s.client.ZRangeByScore(ctx, s.grantExpiryKey(eventID), &redis.ZRangeBy{
		Min: "-inf", Max: strconv.FormatInt(now.UnixMilli(), 10), Offset: 0, Count: limit,
	}).Result()
	if err != nil {
		return err
	}
	for _, grantID := range grantIDs {
		raw, err := s.client.HGet(ctx, s.grantsKey(eventID), grantID).Result()
		if errors.Is(err, redis.Nil) {
			_ = s.client.ZRem(ctx, s.grantExpiryKey(eventID), grantID).Err()
			continue
		}
		if err != nil {
			return err
		}
		grant, err := parseGrant(grantID, raw)
		if err != nil {
			return fmt.Errorf("decode grant %s: %w", grantID, err)
		}
		if err := s.expireGrant(ctx, eventID, grant); err != nil {
			return err
		}
	}
	bookingIDs, err := s.client.ZRangeByScore(ctx, s.bookingExpiryKey(eventID), &redis.ZRangeBy{
		Min: "-inf", Max: strconv.FormatInt(now.UnixMilli(), 10), Offset: 0, Count: limit,
	}).Result()
	if err != nil {
		return err
	}
	for _, bookingID := range bookingIDs {
		if _, err := s.bookingExpireScript.Run(ctx, s.client,
			[]string{s.controlKey(eventID), s.bookingsKey(eventID), s.bookingExpiryKey(eventID)}, bookingID).Result(); err != nil {
			return err
		}
	}
	epoch, err := s.ActiveEpoch(ctx, eventID)
	if err != nil || epoch == "" {
		return err
	}
	result, err := s.closeEpochScript.Run(ctx, s.client,
		[]string{s.controlKey(eventID), s.activeEpochKey(eventID), s.metaKey(eventID, epoch), s.grantsKey(eventID)}, epoch).Result()
	if err != nil {
		return err
	}
	values := resultStrings(result)
	if len(values) > 0 && values[0] == "CLOSED" {
		return s.expireEpochKeys(ctx, eventID, epoch, time.Minute)
	}
	return nil
}

// expireEpochKeys cleans closed epoch state using bounded Redis pipeline batches.
// Applying TTL rather than deleting while SCAN is in progress keeps the iteration stable.
func (s *Store) expireEpochKeys(ctx context.Context, eventID uint64, epoch string, ttl time.Duration) error {
	pattern := s.epochPrefix(eventID, epoch) + ":*"
	var cursor uint64
	for {
		keys, next, err := s.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			pipeline := s.client.Pipeline()
			for _, key := range keys {
				pipeline.PExpire(ctx, key, ttl)
			}
			if _, err := pipeline.Exec(ctx); err != nil {
				return err
			}
		}
		cursor = next
		if cursor == 0 {
			return nil
		}
	}
}

func (s *Store) expireGrant(ctx context.Context, eventID uint64, grant Grant) error {
	page, offset := PageOffset(grant.Seq, s.cfg.PageBits)
	now, err := s.client.Time(ctx).Result()
	if err != nil {
		return err
	}
	keys := []string{
		s.controlKey(eventID), s.reservedKey(eventID, grant.Epoch, page), s.grantsKey(eventID), s.grantBySeqKey(eventID), s.grantExpiryKey(eventID),
	}
	for _, slot := range Slots(Slot(now.UnixMilli(), s.cfg.SlotWidth.Milliseconds()), s.cfg.ReadySlots()) {
		keys = append(keys, s.readyKey(eventID, grant.Epoch, slot, page))
	}
	_, err = s.expireGrantScript.Run(ctx, s.client, keys, grant.Epoch, grant.Seq, offset, grant.ID).Result()
	return err
}

func (s *Store) DebugState(ctx context.Context, eventID uint64) (map[string]any, error) {
	control, err := s.client.HGetAll(ctx, s.controlKey(eventID)).Result()
	if err != nil {
		return nil, err
	}
	epoch, _ := s.ActiveEpoch(ctx, eventID)
	result := map[string]any{"control": control, "active_epoch": epoch}
	if epoch != "" {
		meta, _ := s.client.HGetAll(ctx, s.metaKey(eventID, epoch)).Result()
		result["meta"] = meta
	}
	if raw, err := json.Marshal(result); err == nil {
		_ = raw
	}
	return result, nil
}
