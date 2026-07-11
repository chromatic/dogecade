package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/chromatic/dogecade/internal/store"
)

// openTestStore is a test helper that creates a temporary SQLite database for testing.
func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	return s
}

// doHealthzRequest makes a GET request to /healthz and returns the response.
func doHealthzRequest(t *testing.T, server *Server) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, req)
	return w
}

func TestHealthzHealthy(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()

	server := NewServer(s, nil, nil)

	w := doHealthzRequest(t, server)

	// Expect 200 OK
	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	// Expect some body content
	if w.Body.Len() == 0 {
		t.Error("expected non-empty response body")
	}

	// Verify response is valid JSON with "status": "healthy"
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Errorf("expected valid JSON response: %v", err)
	}
	if resp["status"] != "healthy" {
		t.Errorf("expected status='healthy', got %q", resp["status"])
	}
}

func TestHealthzUnhealthy(t *testing.T) {
	s := openTestStore(t)
	s.Close()

	// Create server with closed store
	server := NewServer(s, nil, nil)

	w := doHealthzRequest(t, server)

	// Expect 503 Service Unavailable
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", w.Code)
	}

	// Expect error message in body
	if w.Body.Len() == 0 {
		t.Error("expected non-empty error response body")
	}

	// Verify response contains error and status=unhealthy
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Errorf("expected valid JSON response: %v", err)
	}
	if resp["status"] != "unhealthy" {
		t.Errorf("expected status='unhealthy', got %q", resp["status"])
	}
	if resp["error"] == "" {
		t.Error("expected non-empty error message")
	}
}

func TestHealthzWithCancelledContext(t *testing.T) {
	s := openTestStore(t)
	defer s.Close()

	server := NewServer(s, nil, nil)

	// Create request with already-cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req := httptest.NewRequest("GET", "/healthz", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	server.Handler().ServeHTTP(w, req)

	// Should return 503 due to context cancellation
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503 for cancelled context, got %d", w.Code)
	}
}

// Test that HTTP status code depends ONLY on DB health, not node health (per design.md).
func TestHealthzNodeHealthDoesNotAffectHTTPStatus(t *testing.T) {
	tests := []struct {
		name      string
		nodeState string
		wantCode  int
	}{
		{"node unconfigured", "unconfigured", http.StatusOK},
		{"node unreachable", "unreachable", http.StatusOK},
		{"node syncing", "syncing", http.StatusOK},
		{"node ok", "ok", http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openTestStore(t)
			defer s.Close()

			checker := &fakeNodeHealthChecker{state: tt.nodeState}
			server := NewServer(s, nil, checker)

			w := doHealthzRequest(t, server)

			if w.Code != tt.wantCode {
				t.Errorf("expected status %d, got %d", tt.wantCode, w.Code)
			}

			// Response should include node state
			if !bytes.Contains(w.Body.Bytes(), []byte(tt.nodeState)) {
				t.Errorf("expected %q in response, got: %s", tt.nodeState, w.Body.String())
			}
		})
	}
}

func TestHealthzWithNodeHealthChecker_DBDownNodeUp(t *testing.T) {
	// Verify that DB down returns 503 even if node is healthy
	s := openTestStore(t)
	s.Close()

	checker := &fakeNodeHealthChecker{state: "ok"}
	server := NewServer(s, nil, checker)

	w := doHealthzRequest(t, server)

	// Should return 503 because DB is down (node state is irrelevant)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503 when DB is down, got %d", w.Code)
	}
}

func TestHealthzWithNodeHealthChecker_Nil(t *testing.T) {
	// Test that nil checker works and response doesn't include node field
	s := openTestStore(t)
	defer s.Close()

	server := NewServer(s, nil, nil)

	w := doHealthzRequest(t, server)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	// Response should not include node key when checker is nil
	var resp map[string]interface{}
	if err := json.NewDecoder(bytes.NewReader(w.Body.Bytes())).Decode(&resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if _, hasNode := resp["node"]; hasNode {
		t.Error("expected no 'node' key in response when checker is nil")
	}
}

// fakeNodeHealthChecker is a test double for testing /healthz integration.
type fakeNodeHealthChecker struct {
	state string
}

func (f *fakeNodeHealthChecker) Check(ctx context.Context) (string, error) {
	return f.state, nil
}
