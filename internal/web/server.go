package web

import (
	"context"
	"encoding/json"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"github.com/chromatic/dogecade/internal/store"
)

// nodeHealthChecker is an interface for checking node health.
// This allows us to test with fakes.
type nodeHealthChecker interface {
	Check(ctx context.Context) (string, error)
}

// Server holds the HTTP server configuration and dependencies.
type Server struct {
	store        *store.Store
	logger       *slog.Logger
	mux          *http.ServeMux
	checker      nodeHealthChecker
	pages        map[string]*template.Template
	customerDeps *CustomerDeps
	adminDeps    *AdminDeps
}

// NewServer creates a new HTTP server with handlers registered.
// If logger is nil, a no-op logger is used.
// If checker is nil, node health checks are not performed (old behavior).
func NewServer(s *store.Store, logger *slog.Logger, checker nodeHealthChecker) *Server {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(nil, nil))
	}

	mux := http.NewServeMux()
	srv := &Server{
		store:   s,
		logger:  logger,
		mux:     mux,
		checker: checker,
	}

	// Register handlers using Go 1.22+ pattern routing
	mux.HandleFunc("GET /healthz", srv.handleHealthz)

	return srv
}

// Handler returns the http.Handler for this server.
func (s *Server) Handler() http.Handler {
	return s.mux
}

// handleHealthz is the handler for GET /healthz.
// It returns 200 OK with a JSON response if the database is healthy,
// or 503 Service Unavailable with an error message if not.
// If a node health checker is configured, it includes the node state in the response.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	// Use a short timeout for the ping operation
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	// Attempt to ping the database
	err := s.store.Ping(ctx)

	w.Header().Set("Content-Type", "application/json")

	response := make(map[string]string)

	if err != nil {
		// Database is unhealthy
		w.WriteHeader(http.StatusServiceUnavailable)
		response["status"] = "unhealthy"
		response["error"] = err.Error()
		json.NewEncoder(w).Encode(response)
		return
	}

	// Database is healthy
	response["status"] = "healthy"

	// If node health checker is configured, include node state
	if s.checker != nil {
		nodeCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		nodeState, _ := s.checker.Check(nodeCtx)
		response["node"] = nodeState
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}
