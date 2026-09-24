package httpapi

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/snowmerak/ticketing/internal/apierr"
	"github.com/snowmerak/ticketing/internal/booking"
	"github.com/snowmerak/ticketing/internal/config"
	"github.com/snowmerak/ticketing/internal/id"
	"github.com/snowmerak/ticketing/internal/queue"
)

//go:embed web/index.html
var webFiles embed.FS

var validSubject = regexp.MustCompile(`^[A-Za-z0-9._:@-]{1,128}$`)

type Server struct {
	cfg       config.Config
	queue     *queue.Store
	booking   *booking.Store
	seatCache *booking.SeatCache
	db        *sql.DB
	redis     *redis.Client
	logger    *slog.Logger
	mux       *http.ServeMux

	metricsMu sync.Mutex
	requests  map[string]uint64
}

func New(cfg config.Config, queueStore *queue.Store, bookingStore *booking.Store, seatCache *booking.SeatCache, db *sql.DB, redisClient *redis.Client, logger *slog.Logger) *Server {
	server := &Server{
		cfg: cfg, queue: queueStore, booking: bookingStore, seatCache: seatCache,
		db: db, redis: redisClient, logger: logger, mux: http.NewServeMux(), requests: make(map[string]uint64),
	}
	server.routes()
	return server
}

func (s *Server) Handler() http.Handler {
	return s.recoverMiddleware(s.logMiddleware(s.mux))
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /", s.handleIndex)
	s.mux.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "alive"})
	})
	s.mux.HandleFunc("GET /readyz", s.handleReady)
	s.mux.HandleFunc("GET /metrics", s.handleMetrics)
	s.mux.HandleFunc("POST /events/{event}/entry", s.withSubject(s.handleEntry))
	s.mux.HandleFunc("POST /queue/heartbeat", s.withSubject(s.handleQueueHeartbeat))
	s.mux.HandleFunc("POST /queue/redeem", s.withSubject(s.handleQueueRedeem))
	s.mux.HandleFunc("POST /booking/heartbeat", s.withSubject(s.handleBookingHeartbeat))
	s.mux.HandleFunc("POST /booking/leave", s.withSubject(s.handleBookingLeave))
	s.mux.HandleFunc("GET /events/{event}/seat-map", s.withSubject(s.handleSeatMap))
	s.mux.HandleFunc("POST /events/{event}/holds", s.withSubject(s.handleCreateHold))
	s.mux.HandleFunc("GET /holds/{hold}", s.withSubject(s.handleGetHold))
	s.mux.HandleFunc("POST /holds/{hold}/cancel", s.withSubject(s.handleCancelHold))
	s.mux.HandleFunc("POST /holds/{hold}/confirm", s.withSubject(s.handleConfirmHold))
	s.mux.HandleFunc("GET /debug/events/{event}", s.withSubject(s.handleDebugEvent))
}

type subjectHandler func(http.ResponseWriter, *http.Request, string) error

func (s *Server) withSubject(next subjectHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		subject := r.Header.Get("X-Subject-ID")
		if !validSubject.MatchString(subject) {
			writeError(w, apierr.ErrUnauthenticated)
			return
		}
		if err := next(w, r, subject); err != nil {
			writeError(w, err)
		}
	}
}

func (s *Server) handleIndex(w http.ResponseWriter, _ *http.Request) {
	raw, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "client unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(raw)
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.DependencyTimeout)
	defer cancel()
	if err := s.redis.Ping(ctx).Err(); err != nil {
		writeError(w, apierr.Wrap(http.StatusServiceUnavailable, "REDIS_UNAVAILABLE", "redis is unavailable", true, err))
		return
	}
	if err := s.queue.CheckInstallation(ctx); err != nil {
		writeError(w, apierr.Wrap(http.StatusServiceUnavailable, "QUEUE_RECOVERING", "queue installation state is invalid", true, err))
		return
	}
	if err := s.db.PingContext(ctx); err != nil {
		writeError(w, apierr.Wrap(http.StatusServiceUnavailable, "MYSQL_UNAVAILABLE", "mysql is unavailable", true, err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	s.metricsMu.Lock()
	copyValues := make(map[string]uint64, len(s.requests))
	for key, value := range s.requests {
		copyValues[key] = value
	}
	s.metricsMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"requests": copyValues})
}

func (s *Server) handleEntry(w http.ResponseWriter, r *http.Request, subject string) error {
	eventID, err := pathUint(r, "event")
	if err != nil {
		return err
	}
	var request struct {
		Ticket string `json:"ticket"`
	}
	if err := decodeOptionalJSON(r, &request); err != nil {
		return err
	}
	if request.Ticket != "" {
		result, err := s.queue.Heartbeat(r.Context(), eventID, subject, request.Ticket)
		if err != nil {
			return redisDependencyError(err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"kind": "QUEUE", "heartbeat": result})
		return nil
	}
	allowDirect := s.databaseHealthy(r.Context())
	result, err := s.queue.Entry(r.Context(), eventID, subject, allowDirect)
	if err != nil {
		return redisDependencyError(err)
	}
	response := map[string]any{"kind": result.Kind, "server_time_ms": result.ServerTime.UnixMilli()}
	if result.Ticket != "" {
		response["ticket"] = result.Ticket
		response["ticket_expires_at_ms"] = result.Claims.ExpiresAtMS
		response["seq"] = strconv.FormatUint(result.Claims.Seq, 10)
		response["epoch"] = result.Claims.Epoch
	} else {
		response["booking"] = result.Booking
	}
	writeJSON(w, http.StatusOK, response)
	return nil
}

func (s *Server) handleQueueHeartbeat(w http.ResponseWriter, r *http.Request, subject string) error {
	var request struct {
		EventID string `json:"event_id"`
		Ticket  string `json:"ticket"`
	}
	if err := decodeJSON(r, &request); err != nil {
		return err
	}
	eventID, err := parseUint(request.EventID)
	if err != nil || request.Ticket == "" {
		return apierr.ErrInvalidRequest
	}
	result, err := s.queue.Heartbeat(r.Context(), eventID, subject, request.Ticket)
	if err != nil {
		return redisDependencyError(err)
	}
	writeJSON(w, http.StatusOK, result)
	return nil
}

func (s *Server) handleQueueRedeem(w http.ResponseWriter, r *http.Request, subject string) error {
	var request struct {
		EventID string `json:"event_id"`
		Ticket  string `json:"ticket"`
		GrantID string `json:"grant_id"`
	}
	if err := decodeJSON(r, &request); err != nil {
		return err
	}
	eventID, err := parseUint(request.EventID)
	if err != nil || request.Ticket == "" || request.GrantID == "" {
		return apierr.ErrInvalidRequest
	}
	bookingPermit, err := s.queue.Redeem(r.Context(), eventID, subject, request.Ticket, request.GrantID)
	if err != nil {
		return redisDependencyError(err)
	}
	writeJSON(w, http.StatusOK, bookingPermit)
	return nil
}

func (s *Server) handleBookingHeartbeat(w http.ResponseWriter, r *http.Request, subject string) error {
	eventID, bookingID, err := decodeBookingRequest(r)
	if err != nil {
		return err
	}
	permit, err := s.queue.BookingHeartbeat(r.Context(), eventID, subject, bookingID)
	if err != nil {
		return redisDependencyError(err)
	}
	writeJSON(w, http.StatusOK, permit)
	return nil
}

func (s *Server) handleBookingLeave(w http.ResponseWriter, r *http.Request, subject string) error {
	eventID, bookingID, err := decodeBookingRequest(r)
	if err != nil {
		return err
	}
	if err := s.queue.LeaveBooking(r.Context(), eventID, subject, bookingID); err != nil {
		return redisDependencyError(err)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "LEFT"})
	return nil
}

func (s *Server) handleSeatMap(w http.ResponseWriter, r *http.Request, subject string) error {
	eventID, err := pathUint(r, "event")
	if err != nil {
		return err
	}
	bookingID := r.Header.Get("X-Booking-ID")
	if bookingID == "" {
		return apierr.ErrBookingExpired
	}
	if _, err := s.queue.ValidateBooking(r.Context(), eventID, subject, bookingID); err != nil {
		return redisDependencyError(err)
	}
	snapshot, ok := s.seatCache.Get(eventID)
	if !ok {
		loaded, err := s.booking.LoadSeatMap(r.Context(), eventID)
		if err != nil {
			return err
		}
		snapshot = loaded
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"event_id": snapshot.EventID, "version": snapshot.Version, "as_of": snapshot.AsOf,
		"is_stale": time.Since(snapshot.AsOf) > 3*time.Second, "seats": snapshot.Seats,
	})
	return nil
}

func (s *Server) handleCreateHold(w http.ResponseWriter, r *http.Request, subject string) error {
	eventID, err := pathUint(r, "event")
	if err != nil {
		return err
	}
	var request struct {
		BookingID string   `json:"booking_id"`
		Mode      string   `json:"mode"`
		SeatIDs   []string `json:"seat_ids"`
		SectionID string   `json:"section_id"`
		Quantity  int      `json:"quantity"`
	}
	if err := decodeJSON(r, &request); err != nil {
		return err
	}
	if _, err := s.queue.ValidateBooking(r.Context(), eventID, subject, request.BookingID); err != nil {
		return redisDependencyError(err)
	}
	seatIDs := make([]uint64, 0, len(request.SeatIDs))
	for _, value := range request.SeatIDs {
		seatID, err := parseUint(value)
		if err != nil {
			return apierr.ErrInvalidRequest
		}
		seatIDs = append(seatIDs, seatID)
	}
	var sectionID uint64
	if request.SectionID != "" {
		sectionID, err = parseUint(request.SectionID)
		if err != nil {
			return apierr.ErrInvalidRequest
		}
	}
	hold, err := s.booking.CreateHold(r.Context(), booking.CreateHoldRequest{
		EventID: eventID, SubjectID: subject, BookingID: request.BookingID,
		IdempotencyKey: r.Header.Get("Idempotency-Key"), Mode: request.Mode,
		SeatIDs: seatIDs, SectionID: sectionID, Quantity: request.Quantity,
	})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, hold)
	return nil
}

func (s *Server) handleGetHold(w http.ResponseWriter, r *http.Request, subject string) error {
	hold, err := s.booking.GetHold(r.Context(), r.PathValue("hold"), subject)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, hold)
	return nil
}

func (s *Server) handleCancelHold(w http.ResponseWriter, r *http.Request, subject string) error {
	hold, err := s.booking.CancelHold(r.Context(), r.PathValue("hold"), subject)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, hold)
	return nil
}

func (s *Server) handleConfirmHold(w http.ResponseWriter, r *http.Request, subject string) error {
	var request struct {
		BookingID       string `json:"booking_id"`
		PaymentResultID string `json:"payment_result_id"`
	}
	if err := decodeJSON(r, &request); err != nil {
		return err
	}
	hold, err := s.booking.GetHold(r.Context(), r.PathValue("hold"), subject)
	if err != nil {
		return err
	}
	if request.BookingID == "" || request.BookingID != hold.BookingID {
		return apierr.ErrForbidden
	}
	eventID, _ := parseUint(hold.EventID)
	if _, err := s.queue.ValidateBooking(r.Context(), eventID, subject, request.BookingID); err != nil {
		return redisDependencyError(err)
	}
	order, err := s.booking.ConfirmHold(r.Context(), hold.ID, subject, request.PaymentResultID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, order)
	return nil
}

func (s *Server) handleDebugEvent(w http.ResponseWriter, r *http.Request, _ string) error {
	eventID, err := pathUint(r, "event")
	if err != nil {
		return err
	}
	state, err := s.queue.DebugState(r.Context(), eventID)
	if err != nil {
		return redisDependencyError(err)
	}
	writeJSON(w, http.StatusOK, state)
	return nil
}

func (s *Server) databaseHealthy(parent context.Context) bool {
	ctx, cancel := context.WithTimeout(parent, s.cfg.DependencyTimeout)
	defer cancel()
	return s.db.PingContext(ctx) == nil
}

func (s *Server) logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		requestID, _ := id.New()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		key := fmt.Sprintf("%s %s %d", r.Method, route, recorder.status/100)
		s.metricsMu.Lock()
		s.requests[key]++
		s.metricsMu.Unlock()
		s.logger.Info("http_request",
			"request_id", requestID, "method", r.Method, "route", route,
			"status", recorder.status, "duration_ms", time.Since(started).Milliseconds())
	})
}

func (s *Server) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Error("http_panic", "error", recovered)
				writeError(w, apierr.New(http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error", true))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func decodeBookingRequest(r *http.Request) (uint64, string, error) {
	var request struct {
		EventID   string `json:"event_id"`
		BookingID string `json:"booking_id"`
	}
	if err := decodeJSON(r, &request); err != nil {
		return 0, "", err
	}
	eventID, err := parseUint(request.EventID)
	if err != nil || request.BookingID == "" {
		return 0, "", apierr.ErrInvalidRequest
	}
	return eventID, request.BookingID, nil
}

func decodeJSON(r *http.Request, destination any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return apierr.Wrap(http.StatusBadRequest, "INVALID_REQUEST", "request JSON is invalid", false, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return apierr.ErrInvalidRequest
	}
	return nil
}

func decodeOptionalJSON(r *http.Request, destination any) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	return decodeJSON(r, destination)
}

func pathUint(r *http.Request, name string) (uint64, error) {
	value, err := parseUint(r.PathValue(name))
	if err != nil {
		return 0, apierr.ErrInvalidRequest
	}
	return value, nil
}

func parseUint(value string) (uint64, error) {
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed == 0 {
		return 0, apierr.ErrInvalidRequest
	}
	return parsed, nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error) {
	apiError := apierr.As(err)
	status := apiError.HTTPStatus
	if status == 0 {
		status = http.StatusInternalServerError
	}
	writeJSON(w, status, apiError)
}

func redisDependencyError(err error) error {
	var stable *apierr.Error
	if errors.As(err, &stable) {
		return err
	}
	return apierr.Wrap(http.StatusServiceUnavailable, "REDIS_UNAVAILABLE", "redis is unavailable", true, err)
}

func (s *Server) Shutdown(_ context.Context) error { return nil }

func normalizeHeader(value string) string { return strings.TrimSpace(value) }
