package apierr

import (
	"errors"
	"fmt"
	"net/http"
)

type Error struct {
	Code         string `json:"code"`
	Message      string `json:"message"`
	Retryable    bool   `json:"retryable"`
	RetryAfterMS int64  `json:"retry_after_ms,omitempty"`
	HTTPStatus   int    `json:"-"`
	Cause        error  `json:"-"`
}

func (e *Error) Error() string {
	if e.Cause == nil {
		return e.Code + ": " + e.Message
	}
	return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
}

func (e *Error) Unwrap() error { return e.Cause }

func New(status int, code, message string, retryable bool) *Error {
	return &Error{HTTPStatus: status, Code: code, Message: message, Retryable: retryable}
}

func Wrap(status int, code, message string, retryable bool, cause error) *Error {
	return &Error{HTTPStatus: status, Code: code, Message: message, Retryable: retryable, Cause: cause}
}

func As(err error) *Error {
	var target *Error
	if errors.As(err, &target) {
		return target
	}
	return Wrap(http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error", true, err)
}

var (
	ErrUnauthenticated = New(http.StatusUnauthorized, "UNAUTHENTICATED", "authentication is required", false)
	ErrForbidden       = New(http.StatusForbidden, "FORBIDDEN", "the authenticated subject does not own this resource", false)
	ErrInvalidRequest  = New(http.StatusBadRequest, "INVALID_REQUEST", "the request is invalid", false)
	ErrTicketExpired   = New(http.StatusUnauthorized, "TICKET_EXPIRED", "the queue ticket has expired", false)
	ErrTicketInvalid   = New(http.StatusUnauthorized, "TICKET_INVALID", "the queue ticket is invalid", false)
	ErrEpochClosed     = New(http.StatusConflict, "EPOCH_CLOSED", "the queue epoch is closed", false)
	ErrEventClosed     = New(http.StatusGone, "EVENT_CLOSED", "the event is closed", false)
	ErrAdmissionPaused = New(http.StatusServiceUnavailable, "ADMISSION_PAUSED", "admission is paused", true)
	ErrGrantExpired    = New(http.StatusGone, "GRANT_EXPIRED", "the admission grant has expired", false)
	ErrBookingExpired  = New(http.StatusGone, "BOOKING_EXPIRED", "the booking permit has expired", false)
	ErrSeatBusy        = New(http.StatusConflict, "SEAT_BUSY", "the seat is being changed by another request", true)
	ErrSeatUnavailable = New(http.StatusConflict, "SEAT_UNAVAILABLE", "the seat is not available", false)
	ErrNoAssignable    = New(http.StatusConflict, "NO_ASSIGNABLE_SEATS_NOW", "no seat can be assigned at this moment", true)
	ErrHoldExpired     = New(http.StatusGone, "HOLD_EXPIRED", "the seat hold has expired", false)
	ErrHoldNotFound    = New(http.StatusNotFound, "HOLD_NOT_FOUND", "the seat hold does not exist", false)
	ErrIdempotency     = New(http.StatusConflict, "IDEMPOTENCY_CONFLICT", "the idempotency key was reused with a different request", false)
	ErrPurchaseLimit   = New(http.StatusConflict, "SEAT_PURCHASE_LIMIT_EXCEEDED", "one seat has already been purchased for this event", false)
	ErrRecovering      = New(http.StatusServiceUnavailable, "QUEUE_RECOVERING", "queue authority is recovering", true)
)
