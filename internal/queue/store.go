package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/snowmerak/ticketing/internal/apierr"
	"github.com/snowmerak/ticketing/internal/config"
	"github.com/snowmerak/ticketing/internal/id"
	"github.com/snowmerak/ticketing/internal/ticket"
	rediscripts "github.com/snowmerak/ticketing/redis/lua"
)

const installationMarkerKey = "ticketing:installation"

type Store struct {
	client *redis.Client
	signer *ticket.Signer
	cfg    config.Config

	entryScript            *redis.Script
	heartbeatScript        *redis.Script
	acquireLeaseScript     *redis.Script
	claimScript            *redis.Script
	redeemScript           *redis.Script
	expireGrantScript      *redis.Script
	bookingHeartbeatScript *redis.Script
	bookingLeaveScript     *redis.Script
	bookingExpireScript    *redis.Script
	closeEpochScript       *redis.Script
}

type EntryResult struct {
	Kind       string         `json:"kind"`
	Ticket     string         `json:"ticket,omitempty"`
	Claims     *ticket.Claims `json:"-"`
	Booking    *Booking       `json:"booking,omitempty"`
	ServerTime time.Time      `json:"server_time"`
}

type HeartbeatResult struct {
	Status          string  `json:"status"`
	Ticket          string  `json:"ticket"`
	TicketExpiresAt int64   `json:"ticket_expires_at_ms"`
	ServerTimeMS    int64   `json:"server_time_ms"`
	NextPollMS      int64   `json:"next_poll_ms"`
	EstimatedAhead  *int64  `json:"estimated_ahead,omitempty"`
	StatsAsOfMS     *int64  `json:"stats_as_of_ms,omitempty"`
	EstimateStale   bool    `json:"estimate_is_stale"`
	GrantID         string  `json:"grant_id,omitempty"`
	GrantExpiresAt  int64   `json:"grant_expires_at_ms,omitempty"`
	Seq             string  `json:"seq"`
	Epoch           string  `json:"epoch"`
	DebugScore      float64 `json:"-"`
}

type Booking struct {
	ID                  string `json:"booking_id"`
	SubjectID           string `json:"-"`
	IdleExpiresAtMS     int64  `json:"idle_expires_at_ms"`
	AbsoluteExpiresAtMS int64  `json:"absolute_expires_at_ms"`
}

type Grant struct {
	ID          string `json:"grant_id"`
	Epoch       string `json:"epoch"`
	Seq         uint64 `json:"seq,string"`
	ExpiresAtMS int64  `json:"expires_at_ms"`
}

type StatsSnapshot struct {
	Epoch    string  `json:"epoch"`
	Counts   []int64 `json:"counts"`
	Prefixes []int64 `json:"prefixes"`
	NextSeq  uint64  `json:"next_seq,string"`
	AsOfMS   int64   `json:"as_of_ms"`
}

func NewStore(client *redis.Client, signer *ticket.Signer, cfg config.Config) *Store {
	return &Store{
		client: client, signer: signer, cfg: cfg,
		entryScript: redis.NewScript(rediscripts.Entry), heartbeatScript: redis.NewScript(rediscripts.Heartbeat),
		acquireLeaseScript: redis.NewScript(rediscripts.AcquireLease), claimScript: redis.NewScript(rediscripts.Claim),
		redeemScript: redis.NewScript(rediscripts.Redeem), expireGrantScript: redis.NewScript(rediscripts.ExpireGrant),
		bookingHeartbeatScript: redis.NewScript(rediscripts.BookingHeartbeat), bookingLeaveScript: redis.NewScript(rediscripts.BookingLeave),
		bookingExpireScript: redis.NewScript(rediscripts.BookingExpire), closeEpochScript: redis.NewScript(rediscripts.CloseEpoch),
	}
}

func (s *Store) Ping(ctx context.Context) error { return s.client.Ping(ctx).Err() }

func (s *Store) CheckSigning(ctx context.Context) error {
	if s.signer == nil {
		return errors.New("ticket signer is not configured")
	}
	return s.signer.Ready(ctx)
}

func (s *Store) Initialize(ctx context.Context) error {
	existing, err := s.client.Get(ctx, installationMarkerKey).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return err
	}
	if existing != "" && existing != s.cfg.InstallationID {
		return fmt.Errorf("redis installation marker is %q, expected %q", existing, s.cfg.InstallationID)
	}
	if existing == "" {
		created, err := s.client.SetNX(ctx, installationMarkerKey, s.cfg.InstallationID, 0).Result()
		if err != nil {
			return err
		}
		if !created {
			return errors.New("redis installation marker changed concurrently")
		}
	}
	now, err := s.client.Time(ctx).Result()
	if err != nil {
		return err
	}
	for _, eventID := range s.cfg.EventIDs {
		key := s.controlKey(eventID)
		currentInstallation, err := s.client.HGet(ctx, key, "installation_id").Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			return err
		}
		if currentInstallation != "" && currentInstallation != s.cfg.InstallationID {
			return fmt.Errorf("event %d belongs to installation %q", eventID, currentInstallation)
		}
		fields := map[string]any{
			"installation_id": s.cfg.InstallationID,
			"mode":            "DIRECT", "expected_epoch": "", "pause": 0,
			"capacity": s.cfg.Capacity, "active_count": 0, "reserved_count": 0,
			"admission_rate": s.cfg.AdmissionRate, "admission_burst": s.cfg.AdmissionBurst,
			"rate_tokens": s.cfg.AdmissionBurst, "rate_token_at_ms": now.UnixMilli(),
			"writer_owner": "", "writer_until_ms": 0, "writer_fence": 0,
		}
		if currentInstallation == "" {
			if err := s.client.HSet(ctx, key, fields).Err(); err != nil {
				return err
			}
		} else {
			for field, value := range fields {
				if err := s.client.HSetNX(ctx, key, field, value).Err(); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *Store) CheckInstallation(ctx context.Context) error {
	marker, err := s.client.Get(ctx, installationMarkerKey).Result()
	if err != nil {
		return fmt.Errorf("read redis installation marker: %w", err)
	}
	if marker != s.cfg.InstallationID {
		return fmt.Errorf("redis installation marker is %q, expected %q", marker, s.cfg.InstallationID)
	}
	for _, eventID := range s.cfg.EventIDs {
		values, err := s.client.HMGet(ctx, s.controlKey(eventID), "installation_id", "mode").Result()
		if err != nil {
			return err
		}
		if len(values) != 2 || toString(values[0]) != s.cfg.InstallationID || toString(values[1]) == "" {
			return fmt.Errorf("event %d control is missing or belongs to another installation", eventID)
		}
	}
	return nil
}

func (s *Store) Entry(ctx context.Context, eventID uint64, subjectID string, allowDirect bool) (EntryResult, error) {
	for attempt := 0; attempt < 3; attempt++ {
		activeEpoch, err := s.client.Get(ctx, s.activeEpochKey(eventID)).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			return EntryResult{}, err
		}
		epoch := activeEpoch
		if epoch == "" {
			epoch, err = id.New()
			if err != nil {
				return EntryResult{}, err
			}
		}
		bookingID, err := id.New()
		if err != nil {
			return EntryResult{}, err
		}
		keys := []string{s.controlKey(eventID), s.activeEpochKey(eventID), s.metaKey(eventID, epoch), s.bookingsKey(eventID), s.bookingExpiryKey(eventID)}
		args := []any{
			epoch, subjectID, bookingID, s.cfg.BookingIdleTTL.Milliseconds(), s.cfg.TicketTTL.Milliseconds(), boolInt(allowDirect),
			s.cfg.BookingMaxLifetime.Milliseconds(), s.cfg.MaxSeqPerEpoch, boolInt(s.signer.CanSign()),
		}
		result, err := s.entryScript.Run(ctx, s.client, keys, args...).Result()
		if err != nil {
			return EntryResult{}, err
		}
		values := resultStrings(result)
		if len(values) == 0 {
			return EntryResult{}, errors.New("entry script returned no result")
		}
		switch values[0] {
		case "RETRY_EPOCH":
			continue
		case "RECOVERING":
			return EntryResult{}, apierr.ErrRecovering
		case "CLOSED":
			return EntryResult{}, apierr.ErrEventClosed
		case "CAPACITY_REACHED":
			return EntryResult{}, apierr.New(429, "QUEUE_CAPACITY_REACHED", "the queue sequence limit was reached", false)
		case "KEY_UNAVAILABLE":
			return EntryResult{}, apierr.New(503, "TICKET_KEY_UNAVAILABLE", "ticket verification keys are unavailable", true)
		case "DIRECT":
			idle, _ := strconv.ParseInt(values[2], 10, 64)
			absolute, _ := strconv.ParseInt(values[3], 10, 64)
			nowMS, _ := strconv.ParseInt(values[4], 10, 64)
			return EntryResult{Kind: "DIRECT", Booking: &Booking{ID: values[1], SubjectID: subjectID, IdleExpiresAtMS: idle, AbsoluteExpiresAtMS: absolute}, ServerTime: time.UnixMilli(nowMS)}, nil
		case "TICKET":
			seq, _ := strconv.ParseUint(values[2], 10, 64)
			nowMS, _ := strconv.ParseInt(values[3], 10, 64)
			token, claims, err := s.signer.New(ctx, eventID, values[1], seq, subjectID, time.UnixMilli(nowMS), s.cfg.TicketTTL)
			if err != nil {
				return EntryResult{}, err
			}
			return EntryResult{Kind: "QUEUE", Ticket: token, Claims: &claims, ServerTime: time.UnixMilli(nowMS)}, nil
		default:
			return EntryResult{}, fmt.Errorf("unknown entry result %q", values[0])
		}
	}
	return EntryResult{}, errors.New("active queue epoch changed repeatedly")
}

func (s *Store) Heartbeat(ctx context.Context, eventID uint64, subjectID, token string) (HeartbeatResult, error) {
	for attempt := 0; attempt < 3; attempt++ {
		now, err := s.client.Time(ctx).Result()
		if err != nil {
			return HeartbeatResult{}, err
		}
		claims, err := s.signer.Verify(ctx, token, subjectID, eventID, s.cfg.MaxSeqPerEpoch, now)
		if err != nil {
			return HeartbeatResult{}, err
		}
		page, offset := PageOffset(claims.Seq, s.cfg.PageBits)
		slot := Slot(now.UnixMilli(), s.cfg.SlotWidth.Milliseconds())
		cleanupAt := (slot + s.cfg.ReadySlots() + 2) * s.cfg.SlotWidth.Milliseconds()
		// The stored maximum may be longer than the returned token, never shorter.
		prospectiveExpiry := now.Add(s.cfg.TicketTTL + s.cfg.DependencyTimeout).UnixMilli()
		keys := []string{
			s.metaKey(eventID, claims.Epoch), s.readyKey(eventID, claims.Epoch, slot, page), s.summaryKey(eventID, claims.Epoch, slot),
			s.spentKey(eventID, claims.Epoch, page), s.grantBySeqKey(eventID), s.grantsKey(eventID),
		}
		result, err := s.heartbeatScript.Run(ctx, s.client, keys,
			claims.Epoch, claims.Seq, offset, page, prospectiveExpiry, slot, s.cfg.SlotWidth.Milliseconds(), cleanupAt,
		).Result()
		if err != nil {
			return HeartbeatResult{}, err
		}
		values := resultStrings(result)
		if len(values) == 0 {
			return HeartbeatResult{}, errors.New("heartbeat script returned no result")
		}
		if values[0] == "RETRY_SLOT" {
			continue
		}
		switch values[0] {
		case "EXPIRED":
			return HeartbeatResult{}, apierr.ErrTicketExpired
		case "CLOSED":
			return HeartbeatResult{}, apierr.ErrEpochClosed
		case "SPENT":
			return HeartbeatResult{}, apierr.New(409, "TICKET_SPENT", "the queue ticket was already redeemed", false)
		case "WAITING", "GRANTED":
		default:
			return HeartbeatResult{}, fmt.Errorf("unknown heartbeat result %q", values[0])
		}
		nowMSIndex := 1
		if values[0] == "GRANTED" {
			nowMSIndex = 3
		}
		nowMS, _ := strconv.ParseInt(values[nowMSIndex], 10, 64)
		newToken, renewed, err := s.signer.Renew(ctx, claims, time.UnixMilli(nowMS), s.cfg.TicketTTL)
		if err != nil {
			return HeartbeatResult{}, err
		}
		response := HeartbeatResult{
			Status: values[0], Ticket: newToken, TicketExpiresAt: renewed.ExpiresAtMS, ServerTimeMS: nowMS,
			NextPollMS: s.cfg.PollInterval.Milliseconds(), Seq: strconv.FormatUint(claims.Seq, 10), Epoch: claims.Epoch,
		}
		if values[0] == "GRANTED" {
			response.GrantID = values[1]
			response.GrantExpiresAt, _ = strconv.ParseInt(values[2], 10, 64)
		}
		s.applyEstimate(ctx, eventID, claims, &response)
		return response, nil
	}
	return HeartbeatResult{}, errors.New("redis slot changed repeatedly during heartbeat")
}

func (s *Store) Redeem(ctx context.Context, eventID uint64, subjectID, token, grantID string) (Booking, error) {
	now, err := s.client.Time(ctx).Result()
	if err != nil {
		return Booking{}, err
	}
	claims, err := s.signer.Verify(ctx, token, subjectID, eventID, s.cfg.MaxSeqPerEpoch, now)
	if err != nil {
		return Booking{}, err
	}
	page, offset := PageOffset(claims.Seq, s.cfg.PageBits)
	bookingID, err := id.New()
	if err != nil {
		return Booking{}, err
	}
	keys := []string{
		s.controlKey(eventID), s.metaKey(eventID, claims.Epoch), s.reservedKey(eventID, claims.Epoch, page), s.spentKey(eventID, claims.Epoch, page),
		s.grantsKey(eventID), s.grantBySeqKey(eventID), s.grantExpiryKey(eventID), s.redeemedKey(eventID, claims.Epoch),
		s.bookingsKey(eventID), s.bookingExpiryKey(eventID),
	}
	result, err := s.redeemScript.Run(ctx, s.client, keys,
		claims.Epoch, claims.Seq, offset, grantID, subjectID, bookingID, s.cfg.BookingIdleTTL.Milliseconds(), s.cfg.BookingMaxLifetime.Milliseconds(),
	).Result()
	if err != nil {
		return Booking{}, err
	}
	values := resultStrings(result)
	if len(values) == 0 {
		return Booking{}, errors.New("redeem script returned no result")
	}
	switch values[0] {
	case "OK":
		idle, _ := strconv.ParseInt(values[2], 10, 64)
		absolute, _ := strconv.ParseInt(values[3], 10, 64)
		return Booking{ID: values[1], SubjectID: subjectID, IdleExpiresAtMS: idle, AbsoluteExpiresAtMS: absolute}, nil
	case "GRANT_EXPIRED", "GRANT_MISSING":
		return Booking{}, apierr.ErrGrantExpired
	case "BOOKING_ENDED":
		return Booking{}, apierr.ErrBookingExpired
	default:
		return Booking{}, apierr.ErrTicketInvalid
	}
}

func (s *Store) BookingHeartbeat(ctx context.Context, eventID uint64, subjectID, bookingID string) (Booking, error) {
	result, err := s.bookingHeartbeatScript.Run(ctx, s.client,
		[]string{s.bookingsKey(eventID), s.bookingExpiryKey(eventID)}, bookingID, subjectID, s.cfg.BookingIdleTTL.Milliseconds()).Result()
	if err != nil {
		return Booking{}, err
	}
	values := resultStrings(result)
	if len(values) == 0 {
		return Booking{}, errors.New("booking heartbeat returned no result")
	}
	switch values[0] {
	case "OK":
		idle, _ := strconv.ParseInt(values[1], 10, 64)
		absolute, _ := strconv.ParseInt(values[2], 10, 64)
		return Booking{ID: bookingID, SubjectID: subjectID, IdleExpiresAtMS: idle, AbsoluteExpiresAtMS: absolute}, nil
	case "OWNER_MISMATCH":
		return Booking{}, apierr.ErrForbidden
	default:
		return Booking{}, apierr.ErrBookingExpired
	}
}

func (s *Store) ValidateBooking(ctx context.Context, eventID uint64, subjectID, bookingID string) (Booking, error) {
	raw, err := s.client.HGet(ctx, s.bookingsKey(eventID), bookingID).Result()
	if errors.Is(err, redis.Nil) {
		return Booking{}, apierr.ErrBookingExpired
	}
	if err != nil {
		return Booking{}, err
	}
	var value struct {
		SubjectID           string `json:"subject_id"`
		IdleExpiresAtMS     int64  `json:"idle_expires_at_ms"`
		AbsoluteExpiresAtMS int64  `json:"absolute_expires_at_ms"`
	}
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return Booking{}, fmt.Errorf("decode booking state: %w", err)
	}
	if value.SubjectID != subjectID {
		return Booking{}, apierr.ErrForbidden
	}
	now, err := s.client.Time(ctx).Result()
	if err != nil {
		return Booking{}, err
	}
	if now.UnixMilli() >= value.IdleExpiresAtMS || now.UnixMilli() >= value.AbsoluteExpiresAtMS {
		return Booking{}, apierr.ErrBookingExpired
	}
	return Booking{ID: bookingID, SubjectID: subjectID, IdleExpiresAtMS: value.IdleExpiresAtMS, AbsoluteExpiresAtMS: value.AbsoluteExpiresAtMS}, nil
}

func (s *Store) LeaveBooking(ctx context.Context, eventID uint64, subjectID, bookingID string) error {
	result, err := s.bookingLeaveScript.Run(ctx, s.client,
		[]string{s.controlKey(eventID), s.bookingsKey(eventID), s.bookingExpiryKey(eventID)}, bookingID, subjectID).Result()
	if err != nil {
		return err
	}
	values := resultStrings(result)
	if len(values) > 0 && values[0] == "OWNER_MISMATCH" {
		return apierr.ErrForbidden
	}
	return nil
}

func (s *Store) ActiveEpoch(ctx context.Context, eventID uint64) (string, error) {
	epoch, err := s.client.Get(ctx, s.activeEpochKey(eventID)).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	return epoch, err
}

func (s *Store) AcquireLease(ctx context.Context, eventID uint64, owner string) (int64, bool, error) {
	result, err := s.acquireLeaseScript.Run(ctx, s.client, []string{s.controlKey(eventID)}, owner, s.cfg.SchedulerLeaseTTL.Milliseconds()).Result()
	if err != nil {
		return 0, false, err
	}
	values := resultStrings(result)
	if len(values) == 0 {
		return 0, false, errors.New("lease script returned no result")
	}
	if values[0] == "BUSY" {
		return 0, false, nil
	}
	fence, _ := strconv.ParseInt(values[1], 10, 64)
	return fence, true, nil
}

func (s *Store) Candidates(ctx context.Context, eventID uint64, epoch string, limit int) ([]uint64, error) {
	nextRaw, err := s.client.HGet(ctx, s.metaKey(eventID, epoch), "next_seq").Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	nextSeq, err := strconv.ParseUint(nextRaw, 10, 64)
	if err != nil || nextSeq == 0 {
		return nil, err
	}
	now, err := s.client.Time(ctx).Result()
	if err != nil {
		return nil, err
	}
	currentSlot := Slot(now.UnixMilli(), s.cfg.SlotWidth.Milliseconds())
	slots := Slots(currentSlot, s.cfg.ReadySlots())
	summaryKeys := make([]string, 0, len(slots))
	for _, slot := range slots {
		summaryKeys = append(summaryKeys, s.summaryKey(eventID, epoch, slot))
	}
	summaries, err := s.getBytes(ctx, summaryKeys)
	if err != nil {
		return nil, err
	}
	summaryUnion := Eligible(summaries, nil, nil)
	maxPage := (nextSeq - 1) / s.cfg.PageBits
	hinted := make(map[uint64]struct{})
	pages := make([]uint64, 0, maxPage+1)
	for _, page := range SetOffsets(summaryUnion, 0) {
		if page <= maxPage {
			hinted[page] = struct{}{}
			pages = append(pages, page)
		}
	}
	// A bounded full fallback prevents a damaged summary from becoming a permanent false negative.
	for page := uint64(0); page <= maxPage; page++ {
		if _, ok := hinted[page]; !ok {
			pages = append(pages, page)
		}
	}
	candidates := make([]uint64, 0, limit)
	for _, page := range pages {
		eligible, err := s.eligiblePage(ctx, eventID, epoch, slots, page)
		if err != nil {
			return nil, err
		}
		for _, offset := range SetOffsets(eligible, limit-len(candidates)) {
			seq := page*s.cfg.PageBits + offset
			if seq < nextSeq {
				candidates = append(candidates, seq)
			}
			if len(candidates) >= limit {
				return candidates, nil
			}
		}
	}
	return candidates, nil
}

func (s *Store) Claim(ctx context.Context, eventID uint64, epoch, owner string, fence int64, seq uint64) (Grant, bool, error) {
	page, offset := PageOffset(seq, s.cfg.PageBits)
	now, err := s.client.Time(ctx).Result()
	if err != nil {
		return Grant{}, false, err
	}
	slots := Slots(Slot(now.UnixMilli(), s.cfg.SlotWidth.Milliseconds()), s.cfg.ReadySlots())
	keys := []string{
		s.controlKey(eventID), s.metaKey(eventID, epoch), s.spentKey(eventID, epoch, page), s.reservedKey(eventID, epoch, page),
		s.grantsKey(eventID), s.grantBySeqKey(eventID), s.grantExpiryKey(eventID),
	}
	for _, slot := range slots {
		keys = append(keys, s.readyKey(eventID, epoch, slot, page))
	}
	grantID, err := id.New()
	if err != nil {
		return Grant{}, false, err
	}
	result, err := s.claimScript.Run(ctx, s.client, keys,
		epoch, seq, offset, fence, owner, grantID, s.cfg.GrantTTL.Milliseconds()).Result()
	if err != nil {
		return Grant{}, false, err
	}
	values := resultStrings(result)
	if len(values) == 0 {
		return Grant{}, false, errors.New("claim script returned no result")
	}
	if values[0] != "OK" {
		return Grant{}, false, nil
	}
	expires, _ := strconv.ParseInt(values[1], 10, 64)
	return Grant{ID: grantID, Epoch: epoch, Seq: seq, ExpiresAtMS: expires}, true, nil
}

func (s *Store) RefreshStats(ctx context.Context, eventID uint64, epoch string) error {
	nextRaw, err := s.client.HGet(ctx, s.metaKey(eventID, epoch), "next_seq").Result()
	if err != nil {
		return err
	}
	nextSeq, err := strconv.ParseUint(nextRaw, 10, 64)
	if err != nil {
		return err
	}
	now, err := s.client.Time(ctx).Result()
	if err != nil {
		return err
	}
	counts := make([]int64, 0)
	prefixes := make([]int64, 0)
	var prefix int64
	if nextSeq > 0 {
		slots := Slots(Slot(now.UnixMilli(), s.cfg.SlotWidth.Milliseconds()), s.cfg.ReadySlots())
		maxPage := (nextSeq - 1) / s.cfg.PageBits
		counts = make([]int64, maxPage+1)
		prefixes = make([]int64, maxPage+1)
		for page := uint64(0); page <= maxPage; page++ {
			eligible, err := s.eligiblePage(ctx, eventID, epoch, slots, page)
			if err != nil {
				return err
			}
			prefixes[page] = prefix
			counts[page] = int64(CountBits(eligible))
			prefix += counts[page]
		}
	}
	snapshot := StatsSnapshot{Epoch: epoch, Counts: counts, Prefixes: prefixes, NextSeq: nextSeq, AsOfMS: now.UnixMilli()}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	return s.client.HSet(ctx, s.statsKey(eventID, epoch), "snapshot", raw).Err()
}

func (s *Store) applyEstimate(ctx context.Context, eventID uint64, claims ticket.Claims, response *HeartbeatResult) {
	raw, err := s.client.HGet(ctx, s.statsKey(eventID, claims.Epoch), "snapshot").Bytes()
	if err != nil {
		response.EstimateStale = true
		return
	}
	var stats StatsSnapshot
	if json.Unmarshal(raw, &stats) != nil || stats.Epoch != claims.Epoch {
		response.EstimateStale = true
		return
	}
	page, offset := PageOffset(claims.Seq, s.cfg.PageBits)
	if page >= uint64(len(stats.Counts)) || page >= uint64(len(stats.Prefixes)) {
		response.EstimateStale = true
		return
	}
	length := s.cfg.PageBits
	pageStart := page * s.cfg.PageBits
	if stats.NextSeq > pageStart && stats.NextSeq-pageStart < length {
		length = stats.NextSeq - pageStart
	}
	if length == 0 {
		response.EstimateStale = true
		return
	}
	estimate := stats.Prefixes[page] + int64(math.Floor(float64(stats.Counts[page])*float64(offset)/float64(length)))
	if estimate < 0 {
		estimate = 0
	}
	response.EstimatedAhead = &estimate
	response.StatsAsOfMS = &stats.AsOfMS
	response.EstimateStale = response.ServerTimeMS-stats.AsOfMS > 2*s.cfg.StatsInterval.Milliseconds()
}

func (s *Store) eligiblePage(ctx context.Context, eventID uint64, epoch string, slots []int64, page uint64) ([]byte, error) {
	keys := make([]string, 0, len(slots)+2)
	for _, slot := range slots {
		keys = append(keys, s.readyKey(eventID, epoch, slot, page))
	}
	keys = append(keys, s.spentKey(eventID, epoch, page), s.reservedKey(eventID, epoch, page))
	values, err := s.getBytes(ctx, keys)
	if err != nil {
		return nil, err
	}
	return Eligible(values[:len(slots)], values[len(slots)], values[len(slots)+1]), nil
}

func (s *Store) getBytes(ctx context.Context, keys []string) ([][]byte, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	values, err := s.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	result := make([][]byte, len(values))
	for i, value := range values {
		switch typed := value.(type) {
		case string:
			result[i] = []byte(typed)
		case []byte:
			result[i] = typed
		}
	}
	return result, nil
}

func (s *Store) controlKey(eventID uint64) string     { return s.prefix(eventID) + ":control" }
func (s *Store) activeEpochKey(eventID uint64) string { return s.prefix(eventID) + ":active_epoch" }
func (s *Store) metaKey(eventID uint64, epoch string) string {
	return s.epochPrefix(eventID, epoch) + ":meta"
}
func (s *Store) readyKey(eventID uint64, epoch string, slot int64, page uint64) string {
	return fmt.Sprintf("%s:ready:%d:%d", s.epochPrefix(eventID, epoch), slot, page)
}
func (s *Store) summaryKey(eventID uint64, epoch string, slot int64) string {
	return fmt.Sprintf("%s:summary:%d", s.epochPrefix(eventID, epoch), slot)
}
func (s *Store) spentKey(eventID uint64, epoch string, page uint64) string {
	return fmt.Sprintf("%s:spent:%d", s.epochPrefix(eventID, epoch), page)
}
func (s *Store) reservedKey(eventID uint64, epoch string, page uint64) string {
	return fmt.Sprintf("%s:reserved:%d", s.epochPrefix(eventID, epoch), page)
}
func (s *Store) statsKey(eventID uint64, epoch string) string {
	return s.epochPrefix(eventID, epoch) + ":stats"
}
func (s *Store) redeemedKey(eventID uint64, epoch string) string {
	return s.epochPrefix(eventID, epoch) + ":redeemed"
}
func (s *Store) grantsKey(eventID uint64) string        { return s.prefix(eventID) + ":grants" }
func (s *Store) grantBySeqKey(eventID uint64) string    { return s.prefix(eventID) + ":grant_by_seq" }
func (s *Store) grantExpiryKey(eventID uint64) string   { return s.prefix(eventID) + ":grant_expiry" }
func (s *Store) bookingsKey(eventID uint64) string      { return s.prefix(eventID) + ":bookings" }
func (s *Store) bookingExpiryKey(eventID uint64) string { return s.prefix(eventID) + ":booking_expiry" }
func (s *Store) prefix(eventID uint64) string           { return fmt.Sprintf("q:{%d}", eventID) }
func (s *Store) epochPrefix(eventID uint64, epoch string) string {
	return s.prefix(eventID) + ":e:" + epoch
}

func resultStrings(value any) []string {
	raw, ok := value.([]any)
	if !ok {
		if items, ok := value.([]interface{}); ok {
			raw = items
		} else {
			return nil
		}
	}
	result := make([]string, len(raw))
	for i, item := range raw {
		result[i] = toString(item)
	}
	return result
}

func toString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case nil:
		return ""
	default:
		return fmt.Sprint(typed)
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func parseGrant(idValue, raw string) (Grant, error) {
	var decoded struct {
		Epoch       string          `json:"epoch"`
		Seq         json.RawMessage `json:"seq"`
		ExpiresAtMS int64           `json:"expires_at_ms"`
	}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return Grant{}, err
	}
	seqText := strings.Trim(string(decoded.Seq), `"`)
	seq, err := strconv.ParseUint(seqText, 10, 64)
	if err != nil {
		return Grant{}, err
	}
	return Grant{ID: idValue, Epoch: decoded.Epoch, Seq: seq, ExpiresAtMS: decoded.ExpiresAtMS}, nil
}
