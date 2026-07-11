package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPulseSendsBacklogCommandWithPulseTimeAndPower(t *testing.T) {
	var gotCmnd string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCmnd = r.URL.Query().Get("cmnd")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"PulseTime1":"2","Power1":"ON"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	if err := c.Pulse(context.Background(), 1, 0); err != nil {
		t.Fatalf("Pulse failed: %v", err)
	}

	want := "Backlog PulseTime1 2; Power1 ON"
	if gotCmnd != want {
		t.Errorf("expected cmnd %q, got %q", want, gotCmnd)
	}
}

func TestPulseUsesCustomPulseTime(t *testing.T) {
	var gotCmnd string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCmnd = r.URL.Query().Get("cmnd")
		w.Write([]byte(`{"PulseTime3":"5","Power3":"ON"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	if err := c.Pulse(context.Background(), 3, 5); err != nil {
		t.Fatalf("Pulse failed: %v", err)
	}

	want := "Backlog PulseTime3 5; Power3 ON"
	if gotCmnd != want {
		t.Errorf("expected cmnd %q, got %q", want, gotCmnd)
	}
}

func TestPulseReturnsErrorOnHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`error`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	if err := c.Pulse(context.Background(), 1, 0); err == nil {
		t.Error("expected error for HTTP 500 response, got nil")
	}
}

func TestPulseReturnsErrorOnUnknownCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"Command":"Unknown"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	if err := c.Pulse(context.Background(), 1, 0); err == nil {
		t.Error("expected error for Tasmota unknown-command response, got nil")
	}
}

func TestPulseReturnsErrorOnUnreachableBoard(t *testing.T) {
	c := NewClient("http://127.0.0.1:1")
	if err := c.Pulse(context.Background(), 1, 0); err == nil {
		t.Error("expected error for unreachable board, got nil")
	}
}

func TestPulseRespectsContextTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Write([]byte(`{"Power1":"ON"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := c.Pulse(ctx, 1, 0)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestStatusParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cmnd") != "Status" {
			t.Errorf("expected cmnd=Status, got %q", r.URL.Query().Get("cmnd"))
		}
		w.Write([]byte(`{"Status":{"Module":1,"DeviceName":"relay1"}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	result, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if _, ok := result.Raw["Status"]; !ok {
		t.Error("expected Raw to contain a Status key")
	}
}

func TestStatusReturnsErrorWhenUnreachable(t *testing.T) {
	c := NewClient("http://127.0.0.1:1")
	if _, err := c.Status(context.Background()); err == nil {
		t.Error("expected error for unreachable board, got nil")
	}
}

func TestNewClientTrimsTrailingSlash(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL + "/")
	if err := c.Pulse(context.Background(), 1, 0); err != nil {
		t.Fatalf("Pulse failed: %v", err)
	}
	if !strings.HasPrefix(gotPath, "/cm") {
		t.Errorf("expected path to start with /cm, got %q (trailing slash not trimmed?)", gotPath)
	}
}
