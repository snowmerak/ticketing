package controller

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/snowmerak/ticketing/internal/service"
	"github.com/snowmerak/ticketing/lib/adapter"
	"github.com/snowmerak/ticketing/lib/domain"
)

// EventController handles HTTP requests for event operations
type EventController struct {
	eventService *service.EventService
	logger       adapter.Logger
}

// NewEventController creates a new EventController
func NewEventController(eventService *service.EventService, logger adapter.Logger) *EventController {
	return &EventController{
		eventService: eventService,
		logger:       logger,
	}
}

// CreateEventRequest represents the request body for creating an event
type CreateEventRequest struct {
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	StartTime     time.Time `json:"start_time"`
	EndTime       time.Time `json:"end_time"`
	Venue         string    `json:"venue"`
	TotalTickets  int       `json:"total_tickets"`
	IsSeatedEvent bool      `json:"is_seated_event"`
}

// CreateEvent handles POST /events
func (c *EventController) CreateEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c.logger.Info(ctx, "Creating event", "method", r.Method, "path", r.URL.Path)

	var req CreateEventRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.logger.Error(ctx, "Failed to decode request body", "error", err)
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate request
	if req.Name == "" {
		http.Error(w, "Event name is required", http.StatusBadRequest)
		return
	}

	if req.StartTime.IsZero() || req.EndTime.IsZero() {
		http.Error(w, "Start time and end time are required", http.StatusBadRequest)
		return
	}

	if req.StartTime.After(req.EndTime) {
		http.Error(w, "Start time must be before end time", http.StatusBadRequest)
		return
	}

	if req.TotalTickets <= 0 {
		http.Error(w, "Total tickets must be positive", http.StatusBadRequest)
		return
	}

	// Create event
	event := &domain.Event{
		ID:               uuid.New(),
		Name:             req.Name,
		Description:      req.Description,
		StartTime:        req.StartTime,
		EndTime:          req.EndTime,
		Venue:            req.Venue,
		Status:           string(domain.EventStatusActive),
		TotalTickets:     req.TotalTickets,
		AvailableTickets: req.TotalTickets,
		IsSeatedEvent:    req.IsSeatedEvent,
	}

	if err := c.eventService.CreateEvent(ctx, event); err != nil {
		c.logger.Error(ctx, "Failed to create event", "error", err)
		http.Error(w, "Failed to create event", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(event)
}

// GetEvent handles GET /events/{id}
func (c *EventController) GetEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vars := mux.Vars(r)

	eventID, err := uuid.Parse(vars["id"])
	if err != nil {
		c.logger.Error(ctx, "Invalid event ID", "id", vars["id"], "error", err)
		http.Error(w, "Invalid event ID", http.StatusBadRequest)
		return
	}

	event, err := c.eventService.GetEvent(ctx, eventID)
	if err != nil {
		c.logger.Error(ctx, "Failed to get event", "event_id", eventID, "error", err)
		http.Error(w, "Event not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(event)
}

// GetActiveEvents handles GET /events/active
func (c *EventController) GetActiveEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	events, err := c.eventService.GetActiveEvents(ctx)
	if err != nil {
		c.logger.Error(ctx, "Failed to get active events", "error", err)
		http.Error(w, "Failed to get events", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(events)
}

// UpdateEventRequest represents the request body for updating an event
type UpdateEventRequest struct {
	Name          *string    `json:"name,omitempty"`
	Description   *string    `json:"description,omitempty"`
	StartTime     *time.Time `json:"start_time,omitempty"`
	EndTime       *time.Time `json:"end_time,omitempty"`
	Venue         *string    `json:"venue,omitempty"`
	Status        *string    `json:"status,omitempty"`
	TotalTickets  *int       `json:"total_tickets,omitempty"`
	IsSeatedEvent *bool      `json:"is_seated_event,omitempty"`
}

// UpdateEvent handles PUT /events/{id}
func (c *EventController) UpdateEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vars := mux.Vars(r)

	eventID, err := uuid.Parse(vars["id"])
	if err != nil {
		c.logger.Error(ctx, "Invalid event ID", "id", vars["id"], "error", err)
		http.Error(w, "Invalid event ID", http.StatusBadRequest)
		return
	}

	var req UpdateEventRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.logger.Error(ctx, "Failed to decode request body", "error", err)
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Get existing event
	event, err := c.eventService.GetEvent(ctx, eventID)
	if err != nil {
		c.logger.Error(ctx, "Failed to get event", "event_id", eventID, "error", err)
		http.Error(w, "Event not found", http.StatusNotFound)
		return
	}

	// Update fields
	if req.Name != nil {
		event.Name = *req.Name
	}
	if req.Description != nil {
		event.Description = *req.Description
	}
	if req.StartTime != nil {
		event.StartTime = *req.StartTime
	}
	if req.EndTime != nil {
		event.EndTime = *req.EndTime
	}
	if req.Venue != nil {
		event.Venue = *req.Venue
	}
	if req.Status != nil {
		event.Status = *req.Status
	}
	if req.TotalTickets != nil {
		event.TotalTickets = *req.TotalTickets
	}
	if req.IsSeatedEvent != nil {
		event.IsSeatedEvent = *req.IsSeatedEvent
	}

	if err := c.eventService.UpdateEvent(ctx, event); err != nil {
		c.logger.Error(ctx, "Failed to update event", "error", err)
		http.Error(w, "Failed to update event", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(event)
}

// DeleteEvent handles DELETE /events/{id}
func (c *EventController) DeleteEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vars := mux.Vars(r)

	eventID, err := uuid.Parse(vars["id"])
	if err != nil {
		c.logger.Error(ctx, "Invalid event ID", "id", vars["id"], "error", err)
		http.Error(w, "Invalid event ID", http.StatusBadRequest)
		return
	}

	if err := c.eventService.DeleteEvent(ctx, eventID); err != nil {
		c.logger.Error(ctx, "Failed to delete event", "event_id", eventID, "error", err)
		http.Error(w, "Failed to delete event", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// CreateSeatsRequest represents the request body for creating seats
type CreateSeatsRequest struct {
	Seats []SeatRequest `json:"seats"`
}

// SeatRequest represents a seat in the request
type SeatRequest struct {
	Section string `json:"section"`
	Row     string `json:"row"`
	Number  string `json:"number"`
	Price   int64  `json:"price"`
}

// CreateSeats handles POST /events/{id}/seats
func (c *EventController) CreateSeats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vars := mux.Vars(r)

	eventID, err := uuid.Parse(vars["id"])
	if err != nil {
		c.logger.Error(ctx, "Invalid event ID", "id", vars["id"], "error", err)
		http.Error(w, "Invalid event ID", http.StatusBadRequest)
		return
	}

	var req CreateSeatsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.logger.Error(ctx, "Failed to decode request body", "error", err)
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if len(req.Seats) == 0 {
		http.Error(w, "At least one seat is required", http.StatusBadRequest)
		return
	}

	// Convert to domain seats
	seats := make([]*domain.Seat, len(req.Seats))
	for i, seatReq := range req.Seats {
		seats[i] = &domain.Seat{
			ID:      uuid.New(),
			EventID: eventID,
			Section: seatReq.Section,
			Row:     seatReq.Row,
			Number:  seatReq.Number,
			Price:   seatReq.Price,
			Status:  string(domain.SeatStatusAvailable),
		}
	}

	if err := c.eventService.CreateSeatsForEvent(ctx, eventID, seats); err != nil {
		c.logger.Error(ctx, "Failed to create seats", "error", err)
		http.Error(w, "Failed to create seats", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message": "Seats created successfully",
		"count":   len(seats),
	})
}

// GetAvailableSeats handles GET /events/{id}/seats/available
func (c *EventController) GetAvailableSeats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vars := mux.Vars(r)

	eventID, err := uuid.Parse(vars["id"])
	if err != nil {
		c.logger.Error(ctx, "Invalid event ID", "id", vars["id"], "error", err)
		http.Error(w, "Invalid event ID", http.StatusBadRequest)
		return
	}

	seats, err := c.eventService.GetAvailableSeats(ctx, eventID)
	if err != nil {
		c.logger.Error(ctx, "Failed to get available seats", "error", err)
		http.Error(w, "Failed to get available seats", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(seats)
}

// RegisterRoutes registers all event routes
func (c *EventController) RegisterRoutes(router *mux.Router) {
	router.HandleFunc("/events", c.CreateEvent).Methods("POST")
	router.HandleFunc("/events/active", c.GetActiveEvents).Methods("GET")
	router.HandleFunc("/events/{id}", c.GetEvent).Methods("GET")
	router.HandleFunc("/events/{id}", c.UpdateEvent).Methods("PUT")
	router.HandleFunc("/events/{id}", c.DeleteEvent).Methods("DELETE")
	router.HandleFunc("/events/{id}/seats", c.CreateSeats).Methods("POST")
	router.HandleFunc("/events/{id}/seats/available", c.GetAvailableSeats).Methods("GET")
}
