package booking

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"

	"github.com/snowmerak/ticketing/internal/apierr"
	"github.com/snowmerak/ticketing/internal/id"
)

type Store struct {
	db      *sql.DB
	holdTTL time.Duration
	limit   chan struct{}
}

type CreateHoldRequest struct {
	EventID        uint64   `json:"-"`
	SubjectID      string   `json:"-"`
	BookingID      string   `json:"booking_id"`
	IdempotencyKey string   `json:"-"`
	Mode           string   `json:"mode"`
	SeatIDs        []uint64 `json:"seat_ids,omitempty"`
	SectionID      uint64   `json:"section_id,omitempty"`
	Quantity       int      `json:"quantity,omitempty"`
}

type Hold struct {
	ID           string    `json:"hold_id"`
	EventID      string    `json:"event_id"`
	SubjectID    string    `json:"-"`
	BookingID    string    `json:"booking_id"`
	State        string    `json:"state"`
	ExpiresAt    time.Time `json:"expires_at"`
	SeatID       string    `json:"seat_id"`
	DisplayAlias string    `json:"display_alias"`
	OrderID      string    `json:"order_id,omitempty"`
}

type Order struct {
	ID              string    `json:"order_id"`
	EventID         string    `json:"event_id"`
	HoldID          string    `json:"hold_id"`
	SubjectID       string    `json:"-"`
	SeatID          string    `json:"seat_id"`
	DisplayAlias    string    `json:"display_alias"`
	PaymentResultID string    `json:"payment_result_id"`
	CreatedAt       time.Time `json:"created_at"`
}

type Seat struct {
	SeatID          string  `json:"seat_id"`
	SectionID       string  `json:"section_id"`
	DisplayAlias    string  `json:"display_alias"`
	RowLabel        string  `json:"row_label,omitempty"`
	SeatNumber      *uint64 `json:"seat_number,omitempty"`
	AllocationOrder uint64  `json:"allocation_order"`
	State           string  `json:"state"`
}

type SeatMap struct {
	EventID string    `json:"event_id"`
	Version int64     `json:"version"`
	AsOf    time.Time `json:"as_of"`
	Seats   []Seat    `json:"seats"`
}

func NewStore(db *sql.DB, holdTTL time.Duration, mutationLimit int) *Store {
	if mutationLimit < 1 {
		mutationLimit = 1
	}
	return &Store{db: db, holdTTL: holdTTL, limit: make(chan struct{}, mutationLimit)}
}

func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) CreateHold(ctx context.Context, request CreateHoldRequest) (Hold, error) {
	if err := validateHoldRequest(request); err != nil {
		return Hold{}, err
	}
	release, err := s.acquire(ctx)
	if err != nil {
		return Hold{}, err
	}
	defer release()

	requestHash, err := hashRequest(request)
	if err != nil {
		return Hold{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return Hold{}, err
	}
	defer tx.Rollback()

	guardOrderID, err := lockPurchaseGuard(ctx, tx, request.EventID, request.SubjectID)
	if err != nil {
		return Hold{}, err
	}
	if existing, found, err := findIdempotentHold(ctx, tx, request.EventID, request.SubjectID, request.IdempotencyKey); err != nil {
		return Hold{}, err
	} else if found {
		if !equalHash(existing.requestHash, requestHash[:]) {
			return Hold{}, apierr.ErrIdempotency
		}
		return s.loadHoldTx(ctx, tx, existing.holdID)
	}
	if guardOrderID.Valid {
		return Hold{}, apierr.ErrPurchaseLimit
	}

	holdID, err := id.New()
	if err != nil {
		return Hold{}, err
	}
	var databaseNow time.Time
	if err := tx.QueryRowContext(ctx, `SELECT UTC_TIMESTAMP(6)`).Scan(&databaseNow); err != nil {
		return Hold{}, err
	}
	expiresAt := databaseNow.Add(s.holdTTL)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO holds (hold_id, event_id, subject_id, booking_id, state, expires_at, idempotency_key, request_hash)
VALUES (?, ?, ?, ?, 'HELD', ?, ?, ?)`,
		holdID, request.EventID, request.SubjectID, request.BookingID, expiresAt, request.IdempotencyKey, requestHash[:]); err != nil {
		if isDuplicate(err) {
			return Hold{}, apierr.ErrIdempotency
		}
		return Hold{}, err
	}

	seatID, err := s.lockSeatForHold(ctx, tx, request)
	if err != nil {
		return Hold{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO hold_items (hold_id, event_id, seat_id) VALUES (?, ?, ?)`, holdID, request.EventID, seatID); err != nil {
		return Hold{}, err
	}
	result, err := tx.ExecContext(ctx, `
UPDATE seat_inventory
SET state = 'HELD', hold_id = ?, order_id = NULL, version = version + 1
WHERE event_id = ? AND seat_id = ? AND state = 'AVAILABLE'`, holdID, request.EventID, seatID)
	if err != nil {
		return Hold{}, err
	}
	rows, _ := result.RowsAffected()
	if rows != 1 {
		return Hold{}, apierr.ErrSeatUnavailable
	}
	hold, err := s.loadHoldTx(ctx, tx, holdID)
	if err != nil {
		return Hold{}, err
	}
	if err := tx.Commit(); err != nil {
		return Hold{}, err
	}
	return hold, nil
}

func validateHoldRequest(request CreateHoldRequest) error {
	if request.EventID == 0 || request.SubjectID == "" || request.BookingID == "" || request.IdempotencyKey == "" || len(request.IdempotencyKey) > 128 {
		return apierr.ErrInvalidRequest
	}
	switch request.Mode {
	case "specified":
		if len(request.SeatIDs) != 1 || request.SeatIDs[0] == 0 || request.Quantity > 1 {
			return apierr.ErrPurchaseLimit
		}
	case "auto":
		if request.SectionID == 0 || request.Quantity != 1 || len(request.SeatIDs) != 0 {
			return apierr.ErrPurchaseLimit
		}
	default:
		return apierr.ErrInvalidRequest
	}
	return nil
}

func hashRequest(request CreateHoldRequest) ([32]byte, error) {
	canonical := struct {
		BookingID string   `json:"booking_id"`
		Mode      string   `json:"mode"`
		SeatIDs   []uint64 `json:"seat_ids,omitempty"`
		SectionID uint64   `json:"section_id,omitempty"`
		Quantity  int      `json:"quantity"`
	}{BookingID: request.BookingID, Mode: request.Mode, SeatIDs: request.SeatIDs, SectionID: request.SectionID, Quantity: request.Quantity}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(raw), nil
}

func (s *Store) lockSeatForHold(ctx context.Context, tx *sql.Tx, request CreateHoldRequest) (uint64, error) {
	var seatID uint64
	var state string
	var err error
	if request.Mode == "specified" {
		err = tx.QueryRowContext(ctx, `
SELECT seat_id, state
FROM seat_inventory
WHERE event_id = ? AND seat_id = ?
FOR UPDATE NOWAIT`, request.EventID, request.SeatIDs[0]).Scan(&seatID, &state)
	} else {
		err = tx.QueryRowContext(ctx, `
SELECT seat_id, state
FROM seat_inventory
WHERE event_id = ? AND section_id = ? AND state = 'AVAILABLE'
ORDER BY allocation_order, seat_id
LIMIT 1
FOR UPDATE SKIP LOCKED`, request.EventID, request.SectionID).Scan(&seatID, &state)
	}
	if errors.Is(err, sql.ErrNoRows) {
		if request.Mode == "auto" {
			return 0, apierr.ErrNoAssignable
		}
		return 0, apierr.ErrSeatUnavailable
	}
	if err != nil {
		if isNowaitConflict(err) {
			return 0, apierr.ErrSeatBusy
		}
		return 0, err
	}
	if state != "AVAILABLE" {
		return 0, apierr.ErrSeatUnavailable
	}
	return seatID, nil
}

func (s *Store) GetHold(ctx context.Context, holdID, subjectID string) (Hold, error) {
	hold, err := s.loadHold(ctx, s.db, holdID)
	if err != nil {
		return Hold{}, err
	}
	if hold.SubjectID != subjectID {
		return Hold{}, apierr.ErrForbidden
	}
	return hold, nil
}

func (s *Store) CancelHold(ctx context.Context, holdID, subjectID string) (Hold, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return Hold{}, err
	}
	defer release()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return Hold{}, err
	}
	defer tx.Rollback()
	hold, err := s.lockHold(ctx, tx, holdID)
	if err != nil {
		return Hold{}, err
	}
	if hold.SubjectID != subjectID {
		return Hold{}, apierr.ErrForbidden
	}
	if hold.State != "HELD" {
		if err := tx.Commit(); err != nil {
			return Hold{}, err
		}
		return hold, nil
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE seat_inventory
SET state = 'AVAILABLE', hold_id = NULL, version = version + 1
WHERE event_id = ? AND seat_id = ? AND state = 'HELD' AND hold_id = ?`, hold.EventID, hold.SeatID, hold.ID); err != nil {
		return Hold{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE holds SET state = 'CANCELLED' WHERE hold_id = ? AND state = 'HELD'`, hold.ID); err != nil {
		return Hold{}, err
	}
	hold.State = "CANCELLED"
	if err := tx.Commit(); err != nil {
		return Hold{}, err
	}
	return hold, nil
}

func (s *Store) ConfirmHold(ctx context.Context, holdID, subjectID, paymentResultID string) (Order, error) {
	if paymentResultID == "" {
		return Order{}, apierr.ErrInvalidRequest
	}
	preflight, err := s.loadHold(ctx, s.db, holdID)
	if err != nil {
		return Order{}, err
	}
	if preflight.SubjectID != subjectID {
		return Order{}, apierr.ErrForbidden
	}
	release, err := s.acquire(ctx)
	if err != nil {
		return Order{}, err
	}
	defer release()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return Order{}, err
	}
	defer tx.Rollback()
	guardOrderID, err := lockPurchaseGuard(ctx, tx, parseUint(preflight.EventID), subjectID)
	if err != nil {
		return Order{}, err
	}
	if existing, found, err := loadOrderByHoldTx(ctx, tx, holdID); err != nil {
		return Order{}, err
	} else if found {
		return existing, nil
	}
	if guardOrderID.Valid {
		return Order{}, apierr.ErrPurchaseLimit
	}
	hold, err := s.lockHold(ctx, tx, holdID)
	if err != nil {
		return Order{}, err
	}
	if hold.SubjectID != subjectID {
		return Order{}, apierr.ErrForbidden
	}
	var databaseNow time.Time
	if err := tx.QueryRowContext(ctx, `SELECT UTC_TIMESTAMP(6)`).Scan(&databaseNow); err != nil {
		return Order{}, err
	}
	if hold.State != "HELD" || !databaseNow.Before(hold.ExpiresAt) {
		return Order{}, apierr.ErrHoldExpired
	}
	var state, ownerHoldID, alias string
	if err := tx.QueryRowContext(ctx, `
SELECT i.state, COALESCE(i.hold_id, ''), d.display_alias
FROM seat_inventory i
JOIN seat_definitions d ON d.event_id = i.event_id AND d.seat_id = i.seat_id
WHERE i.event_id = ? AND i.seat_id = ?
FOR UPDATE`, hold.EventID, hold.SeatID).Scan(&state, &ownerHoldID, &alias); err != nil {
		return Order{}, err
	}
	if state != "HELD" || ownerHoldID != hold.ID {
		return Order{}, apierr.ErrSeatUnavailable
	}
	orderID, err := id.New()
	if err != nil {
		return Order{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO orders (order_id, event_id, hold_id, subject_id, payment_result_id)
VALUES (?, ?, ?, ?, ?)`, orderID, hold.EventID, hold.ID, subjectID, paymentResultID); err != nil {
		if isDuplicate(err) {
			return Order{}, apierr.ErrPurchaseLimit
		}
		return Order{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO order_items (order_id, event_id, seat_id, display_alias_snapshot)
VALUES (?, ?, ?, ?)`, orderID, hold.EventID, hold.SeatID, alias); err != nil {
		return Order{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE seat_inventory
SET state = 'SOLD', hold_id = NULL, order_id = ?, version = version + 1
WHERE event_id = ? AND seat_id = ? AND state = 'HELD' AND hold_id = ?`, orderID, hold.EventID, hold.SeatID, hold.ID); err != nil {
		return Order{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE holds SET state = 'CONFIRMED' WHERE hold_id = ? AND state = 'HELD'`, hold.ID); err != nil {
		return Order{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE event_purchase_guards SET order_id = ?
WHERE event_id = ? AND subject_id = ? AND order_id IS NULL`, orderID, hold.EventID, subjectID); err != nil {
		return Order{}, err
	}
	order := Order{
		ID: orderID, EventID: hold.EventID, HoldID: hold.ID, SubjectID: subjectID,
		SeatID: hold.SeatID, DisplayAlias: alias, PaymentResultID: paymentResultID, CreatedAt: databaseNow,
	}
	if err := tx.Commit(); err != nil {
		return Order{}, err
	}
	return order, nil
}

func (s *Store) ExpireHolds(ctx context.Context, limit int) (int, error) {
	if limit < 1 {
		return 0, nil
	}
	release, err := s.acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer release()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
SELECT hold_id
FROM holds
WHERE state = 'HELD' AND expires_at <= UTC_TIMESTAMP(6)
ORDER BY expires_at, hold_id
LIMIT ?
FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return 0, err
	}
	var holdIDs []string
	for rows.Next() {
		var holdID string
		if err := rows.Scan(&holdID); err != nil {
			rows.Close()
			return 0, err
		}
		holdIDs = append(holdIDs, holdID)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, holdID := range holdIDs {
		var eventID, seatID uint64
		if err := tx.QueryRowContext(ctx, `SELECT event_id, seat_id FROM hold_items WHERE hold_id = ?`, holdID).Scan(&eventID, &seatID); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE seat_inventory
SET state = 'AVAILABLE', hold_id = NULL, version = version + 1
WHERE event_id = ? AND seat_id = ? AND state = 'HELD' AND hold_id = ?`, eventID, seatID, holdID); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE holds SET state = 'EXPIRED' WHERE hold_id = ? AND state = 'HELD'`, holdID); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(holdIDs), nil
}

func (s *Store) LoadSeatMap(ctx context.Context, eventID uint64) (SeatMap, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT d.seat_id, d.section_id, d.display_alias, COALESCE(d.row_label, ''), d.seat_number,
       d.allocation_order, i.state, i.version
FROM seat_definitions d
JOIN seat_inventory i ON i.event_id = d.event_id AND i.seat_id = d.seat_id
WHERE d.event_id = ?
ORDER BY d.section_id, d.allocation_order, d.seat_id`, eventID)
	if err != nil {
		return SeatMap{}, err
	}
	defer rows.Close()
	result := SeatMap{EventID: strconv.FormatUint(eventID, 10), AsOf: time.Now().UTC()}
	for rows.Next() {
		var seat Seat
		var seatID, sectionID, allocationOrder, version uint64
		var seatNumber sql.NullInt64
		if err := rows.Scan(&seatID, &sectionID, &seat.DisplayAlias, &seat.RowLabel, &seatNumber, &allocationOrder, &seat.State, &version); err != nil {
			return SeatMap{}, err
		}
		seat.SeatID = strconv.FormatUint(seatID, 10)
		seat.SectionID = strconv.FormatUint(sectionID, 10)
		seat.AllocationOrder = allocationOrder
		if seatNumber.Valid {
			value := uint64(seatNumber.Int64)
			seat.SeatNumber = &value
		}
		if int64(version) > result.Version {
			result.Version = int64(version)
		}
		result.Seats = append(result.Seats, seat)
	}
	return result, rows.Err()
}

type idempotentHold struct {
	holdID      string
	requestHash []byte
}

func findIdempotentHold(ctx context.Context, tx *sql.Tx, eventID uint64, subjectID, key string) (idempotentHold, bool, error) {
	var result idempotentHold
	err := tx.QueryRowContext(ctx, `
SELECT hold_id, request_hash FROM holds
WHERE event_id = ? AND subject_id = ? AND idempotency_key = ?`, eventID, subjectID, key).Scan(&result.holdID, &result.requestHash)
	if errors.Is(err, sql.ErrNoRows) {
		return idempotentHold{}, false, nil
	}
	return result, err == nil, err
}

func lockPurchaseGuard(ctx context.Context, tx *sql.Tx, eventID uint64, subjectID string) (sql.NullString, error) {
	if _, err := tx.ExecContext(ctx, `INSERT IGNORE INTO event_purchase_guards (event_id, subject_id) VALUES (?, ?)`, eventID, subjectID); err != nil {
		return sql.NullString{}, err
	}
	var orderID sql.NullString
	if err := tx.QueryRowContext(ctx, `
SELECT order_id FROM event_purchase_guards
WHERE event_id = ? AND subject_id = ?
FOR UPDATE`, eventID, subjectID).Scan(&orderID); err != nil {
		return sql.NullString{}, err
	}
	return orderID, nil
}

func (s *Store) lockHold(ctx context.Context, tx *sql.Tx, holdID string) (Hold, error) {
	return s.loadHoldQuery(ctx, tx, holdID, true)
}

func (s *Store) loadHoldTx(ctx context.Context, tx *sql.Tx, holdID string) (Hold, error) {
	return s.loadHoldQuery(ctx, tx, holdID, false)
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) loadHold(ctx context.Context, query queryer, holdID string) (Hold, error) {
	return s.loadHoldFrom(ctx, query, holdID, "")
}

func (s *Store) loadHoldQuery(ctx context.Context, tx *sql.Tx, holdID string, lock bool) (Hold, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	return s.loadHoldFrom(ctx, tx, holdID, suffix)
}

func (s *Store) loadHoldFrom(ctx context.Context, query queryer, holdID, suffix string) (Hold, error) {
	var hold Hold
	var eventID, seatID uint64
	err := query.QueryRowContext(ctx, `
SELECT h.hold_id, h.event_id, h.subject_id, h.booking_id, h.state, h.expires_at,
       hi.seat_id, d.display_alias, COALESCE(o.order_id, '')
FROM holds h
JOIN hold_items hi ON hi.hold_id = h.hold_id
JOIN seat_definitions d ON d.event_id = hi.event_id AND d.seat_id = hi.seat_id
LEFT JOIN orders o ON o.hold_id = h.hold_id
WHERE h.hold_id = ?`+suffix, holdID).Scan(
		&hold.ID, &eventID, &hold.SubjectID, &hold.BookingID, &hold.State, &hold.ExpiresAt,
		&seatID, &hold.DisplayAlias, &hold.OrderID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Hold{}, apierr.ErrHoldNotFound
	}
	if err != nil {
		return Hold{}, err
	}
	hold.EventID = strconv.FormatUint(eventID, 10)
	hold.SeatID = strconv.FormatUint(seatID, 10)
	return hold, nil
}

func loadOrderByHoldTx(ctx context.Context, tx *sql.Tx, holdID string) (Order, bool, error) {
	var order Order
	var eventID, seatID uint64
	err := tx.QueryRowContext(ctx, `
SELECT o.order_id, o.event_id, o.hold_id, o.subject_id, oi.seat_id,
       oi.display_alias_snapshot, o.payment_result_id, o.created_at
FROM orders o
JOIN order_items oi ON oi.order_id = o.order_id
WHERE o.hold_id = ?`, holdID).Scan(
		&order.ID, &eventID, &order.HoldID, &order.SubjectID, &seatID,
		&order.DisplayAlias, &order.PaymentResultID, &order.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Order{}, false, nil
	}
	if err != nil {
		return Order{}, false, err
	}
	order.EventID = strconv.FormatUint(eventID, 10)
	order.SeatID = strconv.FormatUint(seatID, 10)
	return order, true, nil
}

func (s *Store) acquire(ctx context.Context) (func(), error) {
	select {
	case s.limit <- struct{}{}:
		return func() { <-s.limit }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func equalHash(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var different byte
	for i := range left {
		different |= left[i] ^ right[i]
	}
	return different == 0
}

func isDuplicate(err error) bool {
	var mysqlErr *mysqlDriver.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

func isNowaitConflict(err error) bool {
	var mysqlErr *mysqlDriver.MySQLError
	return errors.As(err, &mysqlErr) && (mysqlErr.Number == 3572 || mysqlErr.Number == 1205)
}

func parseUint(value string) uint64 {
	parsed, _ := strconv.ParseUint(value, 10, 64)
	return parsed
}

func (s *Store) ResetEventForTests(ctx context.Context, eventID uint64) error {
	statements := []string{
		`DELETE FROM event_purchase_guards WHERE event_id = ?`,
		`DELETE oi FROM order_items oi JOIN orders o ON o.order_id = oi.order_id WHERE o.event_id = ?`,
		`UPDATE seat_inventory SET state = 'AVAILABLE', hold_id = NULL, order_id = NULL, version = version + 1 WHERE event_id = ?`,
		`DELETE FROM orders WHERE event_id = ?`,
		`DELETE hi FROM hold_items hi JOIN holds h ON h.hold_id = hi.hold_id WHERE h.event_id = ?`,
		`DELETE FROM holds WHERE event_id = ?`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement, eventID); err != nil {
			return err
		}
	}
	return nil
}

func (h Hold) String() string { return fmt.Sprintf("hold %s/%s", h.EventID, h.ID) }
