package controller

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/snowmerak/ticketing/internal/service"
	"github.com/snowmerak/ticketing/lib/adapter"
)

// TicketingController handles HTTP requests for ticketing operations
type TicketingController struct {
	ticketingService *service.TicketingService
	logger           adapter.Logger
}

// NewTicketingController creates a new TicketingController
func NewTicketingController(ticketingService *service.TicketingService, logger adapter.Logger) *TicketingController {
	return &TicketingController{
		ticketingService: ticketingService,
		logger:           logger,
	}
}

// PurchaseTicketRequest represents the request body for purchasing a ticket
type PurchaseTicketRequest struct {
	EventID   uuid.UUID  `json:"event_id"`
	UserID    uuid.UUID  `json:"user_id"`
	SeatID    *uuid.UUID `json:"seat_id,omitempty"`
	SessionID string     `json:"session_id"`
}

// PurchaseTicket handles POST /tickets/purchase
func (c *TicketingController) PurchaseTicket(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c.logger.Info(ctx, "Purchase ticket request", "method", r.Method, "path", r.URL.Path)

	var req PurchaseTicketRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.logger.Error(ctx, "Failed to decode request body", "error", err)
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate request
	if req.EventID == uuid.Nil {
		http.Error(w, "Event ID is required", http.StatusBadRequest)
		return
	}

	if req.UserID == uuid.Nil {
		http.Error(w, "User ID is required", http.StatusBadRequest)
		return
	}

	if req.SessionID == "" {
		http.Error(w, "Session ID is required", http.StatusBadRequest)
		return
	}

	// Purchase ticket
	ticket, err := c.ticketingService.PurchaseTicket(ctx, req.EventID, req.UserID, req.SeatID, req.SessionID)
	if err != nil {
		c.logger.Error(ctx, "Failed to purchase ticket", "error", err)
		http.Error(w, "Failed to purchase ticket: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(ticket)
}

// ConfirmTicket handles POST /tickets/{id}/confirm
func (c *TicketingController) ConfirmTicket(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vars := mux.Vars(r)

	ticketID, err := uuid.Parse(vars["id"])
	if err != nil {
		c.logger.Error(ctx, "Invalid ticket ID", "id", vars["id"], "error", err)
		http.Error(w, "Invalid ticket ID", http.StatusBadRequest)
		return
	}

	if err := c.ticketingService.ConfirmTicket(ctx, ticketID); err != nil {
		c.logger.Error(ctx, "Failed to confirm ticket", "ticket_id", ticketID, "error", err)
		http.Error(w, "Failed to confirm ticket: "+err.Error(), http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"message": "Ticket confirmed successfully",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// CancelTicket handles POST /tickets/{id}/cancel
func (c *TicketingController) CancelTicket(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vars := mux.Vars(r)

	ticketID, err := uuid.Parse(vars["id"])
	if err != nil {
		c.logger.Error(ctx, "Invalid ticket ID", "id", vars["id"], "error", err)
		http.Error(w, "Invalid ticket ID", http.StatusBadRequest)
		return
	}

	if err := c.ticketingService.CancelTicket(ctx, ticketID); err != nil {
		c.logger.Error(ctx, "Failed to cancel ticket", "ticket_id", ticketID, "error", err)
		http.Error(w, "Failed to cancel ticket: "+err.Error(), http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"message": "Ticket cancelled successfully",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// GetTicket handles GET /tickets/{id}
func (c *TicketingController) GetTicket(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vars := mux.Vars(r)

	ticketID, err := uuid.Parse(vars["id"])
	if err != nil {
		c.logger.Error(ctx, "Invalid ticket ID", "id", vars["id"], "error", err)
		http.Error(w, "Invalid ticket ID", http.StatusBadRequest)
		return
	}

	ticket, err := c.ticketingService.GetTicket(ctx, ticketID)
	if err != nil {
		c.logger.Error(ctx, "Failed to get ticket", "ticket_id", ticketID, "error", err)
		http.Error(w, "Ticket not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ticket)
}

// GetUserTickets handles GET /tickets/user/{user_id}
func (c *TicketingController) GetUserTickets(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vars := mux.Vars(r)

	userID, err := uuid.Parse(vars["user_id"])
	if err != nil {
		c.logger.Error(ctx, "Invalid user ID", "id", vars["user_id"], "error", err)
		http.Error(w, "Invalid user ID", http.StatusBadRequest)
		return
	}

	tickets, err := c.ticketingService.GetUserTickets(ctx, userID)
	if err != nil {
		c.logger.Error(ctx, "Failed to get user tickets", "user_id", userID, "error", err)
		http.Error(w, "Failed to get user tickets", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tickets)
}

// RegisterRoutes registers all ticketing routes
func (c *TicketingController) RegisterRoutes(router *mux.Router) {
	router.HandleFunc("/tickets/purchase", c.PurchaseTicket).Methods("POST")
	router.HandleFunc("/tickets/{id}/confirm", c.ConfirmTicket).Methods("POST")
	router.HandleFunc("/tickets/{id}/cancel", c.CancelTicket).Methods("POST")
	router.HandleFunc("/tickets/{id}", c.GetTicket).Methods("GET")
	router.HandleFunc("/tickets/user/{user_id}", c.GetUserTickets).Methods("GET")
}
