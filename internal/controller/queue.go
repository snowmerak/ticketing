package controller

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/snowmerak/ticketing/internal/service"
	"github.com/snowmerak/ticketing/lib/adapter"
)

// QueueController handles HTTP requests for queue operations
type QueueController struct {
	queueService *service.QueueService
	logger       adapter.Logger
}

// NewQueueController creates a new QueueController
func NewQueueController(queueService *service.QueueService, logger adapter.Logger) *QueueController {
	return &QueueController{
		queueService: queueService,
		logger:       logger,
	}
}

// JoinQueueRequest represents the request body for joining a queue
type JoinQueueRequest struct {
	EventID   uuid.UUID `json:"event_id"`
	UserID    uuid.UUID `json:"user_id"`
	SessionID string    `json:"session_id"`
}

// JoinQueue handles POST /queue/join
func (c *QueueController) JoinQueue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c.logger.Info(ctx, "Join queue request", "method", r.Method, "path", r.URL.Path)

	var req JoinQueueRequest
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

	// Join queue
	entry, err := c.queueService.JoinQueue(ctx, req.EventID, req.UserID, req.SessionID)
	if err != nil {
		c.logger.Error(ctx, "Failed to join queue", "error", err)
		http.Error(w, "Failed to join queue: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(entry)
}

// GetQueuePosition handles GET /queue/position/{event_id}/{user_id}
func (c *QueueController) GetQueuePosition(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vars := mux.Vars(r)

	eventID, err := uuid.Parse(vars["event_id"])
	if err != nil {
		c.logger.Error(ctx, "Invalid event ID", "id", vars["event_id"], "error", err)
		http.Error(w, "Invalid event ID", http.StatusBadRequest)
		return
	}

	userID, err := uuid.Parse(vars["user_id"])
	if err != nil {
		c.logger.Error(ctx, "Invalid user ID", "id", vars["user_id"], "error", err)
		http.Error(w, "Invalid user ID", http.StatusBadRequest)
		return
	}

	entry, err := c.queueService.GetQueuePosition(ctx, eventID, userID)
	if err != nil {
		c.logger.Error(ctx, "Failed to get queue position", "error", err)
		http.Error(w, "Failed to get queue position", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entry)
}

// GetQueueStatus handles GET /queue/status/{session_id}
func (c *QueueController) GetQueueStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vars := mux.Vars(r)

	sessionID := vars["session_id"]
	if sessionID == "" {
		http.Error(w, "Session ID is required", http.StatusBadRequest)
		return
	}

	entry, err := c.queueService.GetQueueStatus(ctx, sessionID)
	if err != nil {
		c.logger.Error(ctx, "Failed to get queue status", "session_id", sessionID, "error", err)
		http.Error(w, "Failed to get queue status: "+err.Error(), http.StatusNotFound)
		return
	}

	// Calculate estimated wait time
	waitTime, err := c.queueService.EstimateWaitTime(ctx, entry.EventID, entry.UserID)
	if err != nil {
		c.logger.Warn(ctx, "Failed to estimate wait time", "error", err)
		waitTime = 0
	}

	response := map[string]interface{}{
		"entry":               entry,
		"estimated_wait_time": waitTime.String(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// GetQueueLength handles GET /queue/length/{event_id}
func (c *QueueController) GetQueueLength(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vars := mux.Vars(r)

	eventID, err := uuid.Parse(vars["event_id"])
	if err != nil {
		c.logger.Error(ctx, "Invalid event ID", "id", vars["event_id"], "error", err)
		http.Error(w, "Invalid event ID", http.StatusBadRequest)
		return
	}

	length, err := c.queueService.GetQueueLength(ctx, eventID)
	if err != nil {
		c.logger.Error(ctx, "Failed to get queue length", "error", err)
		http.Error(w, "Failed to get queue length", http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"event_id": eventID,
		"length":   length,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// ProcessQueue handles POST /queue/process/{event_id}
func (c *QueueController) ProcessQueue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vars := mux.Vars(r)

	eventID, err := uuid.Parse(vars["event_id"])
	if err != nil {
		c.logger.Error(ctx, "Invalid event ID", "id", vars["event_id"], "error", err)
		http.Error(w, "Invalid event ID", http.StatusBadRequest)
		return
	}

	entry, err := c.queueService.ProcessQueue(ctx, eventID)
	if err != nil {
		c.logger.Error(ctx, "Failed to process queue", "error", err)
		http.Error(w, "Failed to process queue: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entry)
}

// RefreshSessionRequest represents the request body for refreshing a session
type RefreshSessionRequest struct {
	SessionID string `json:"session_id"`
}

// RefreshSession handles POST /queue/refresh
func (c *QueueController) RefreshSession(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req RefreshSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.logger.Error(ctx, "Failed to decode request body", "error", err)
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.SessionID == "" {
		http.Error(w, "Session ID is required", http.StatusBadRequest)
		return
	}

	if err := c.queueService.RefreshSession(ctx, req.SessionID); err != nil {
		c.logger.Error(ctx, "Failed to refresh session", "error", err)
		http.Error(w, "Failed to refresh session: "+err.Error(), http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"message": "Session refreshed successfully",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// RegisterRoutes registers all queue routes
func (c *QueueController) RegisterRoutes(router *mux.Router) {
	router.HandleFunc("/queue/join", c.JoinQueue).Methods("POST")
	router.HandleFunc("/queue/position/{event_id}/{user_id}", c.GetQueuePosition).Methods("GET")
	router.HandleFunc("/queue/status/{session_id}", c.GetQueueStatus).Methods("GET")
	router.HandleFunc("/queue/length/{event_id}", c.GetQueueLength).Methods("GET")
	router.HandleFunc("/queue/process/{event_id}", c.ProcessQueue).Methods("POST")
	router.HandleFunc("/queue/refresh", c.RefreshSession).Methods("POST")
}
