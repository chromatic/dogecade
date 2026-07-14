package relay

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromatic/dogecade/internal/services"
)

// These tests exercise the dispatcher against a real HTTP stub board (using
// the default client factory, i.e. the real *Client) rather than a fake
// pulser, so they cover the actual HTTP failure modes a real Tasmota board
// can produce: connection refused, slow/timing out, and 5xx responses.

func TestDispatchStubBoardDownExhaustsAndRefundsExactlyOnce(t *testing.T) {
	ctx := context.Background()
	storeDB := newTestStore(t)
	ledger := services.NewLedgerService(storeDB)
	settings := services.NewSettingsService(storeDB)
	if err := settings.SetRelayMaxAttempts(ctx, 2); err != nil {
		t.Fatalf("SetRelayMaxAttempts failed: %v", err)
	}
	d := NewDispatcher(storeDB, ledger, settings)

	// Nothing listens here; connection is refused immediately.
	machineID := seedMachine(t, ctx, storeDB)
	boardID := seedRelayBoard(t, ctx, storeDB, "http://127.0.0.1:1")
	seedMachineRelay(t, ctx, storeDB, machineID, boardID, 1)
	userID := seedUser(t, ctx, storeDB)
	creditTokens(t, ctx, storeDB, userID, 1)
	creditTokens(t, ctx, storeDB, userID, -1)
	pulseID := seedPendingPulse(t, ctx, storeDB, machineID, sql.NullInt64{Int64: userID, Valid: true}, "token_redemption")

	if err := d.DispatchOnce(ctx); err != nil {
		t.Fatalf("DispatchOnce (1st) failed: %v", err)
	}
	state, attempts := pulseState(t, ctx, storeDB, pulseID)
	if state != "pending" || attempts != 1 {
		t.Fatalf("expected pending/attempts=1 after 1st failure, got state=%q attempts=%d", state, attempts)
	}

	forceRetryNow(t, ctx, storeDB, pulseID)
	if err := d.DispatchOnce(ctx); err != nil {
		t.Fatalf("DispatchOnce (2nd) failed: %v", err)
	}

	state, attempts = pulseState(t, ctx, storeDB, pulseID)
	if state != "failed" {
		t.Errorf("expected state 'failed' after exhausting attempts against a down board, got %q", state)
	}
	if attempts != 2 {
		t.Errorf("expected attempts=2, got %d", attempts)
	}
	if got := balance(t, ctx, storeDB, userID); got != 1 {
		t.Errorf("expected balance refunded to 1, got %d", got)
	}
	if got := refundCount(t, ctx, storeDB, pulseID); got != 1 {
		t.Errorf("expected exactly 1 refund, got %d", got)
	}

	// A third pass must not re-process the now-failed pulse.
	if err := d.DispatchOnce(ctx); err != nil {
		t.Fatalf("DispatchOnce (3rd) failed: %v", err)
	}
	if got := refundCount(t, ctx, storeDB, pulseID); got != 1 {
		t.Errorf("expected refund to remain exactly-once, got %d", got)
	}
}

func TestDispatchStubBoardSlowTimesOutAndRetries(t *testing.T) {
	ctx := context.Background()
	storeDB := newTestStore(t)
	ledger := services.NewLedgerService(storeDB)
	settings := services.NewSettingsService(storeDB)
	if err := settings.SetRelayMaxAttempts(ctx, 5); err != nil {
		t.Fatalf("SetRelayMaxAttempts failed: %v", err)
	}
	d := NewDispatcher(storeDB, ledger, settings)
	d.SetAttemptTimeout(30 * time.Millisecond)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(`{"Power1":"ON"}`))
	}))
	defer srv.Close()

	machineID := seedMachine(t, ctx, storeDB)
	boardID := seedRelayBoard(t, ctx, storeDB, srv.URL)
	seedMachineRelay(t, ctx, storeDB, machineID, boardID, 1)
	pulseID := seedPendingPulse(t, ctx, storeDB, machineID, sql.NullInt64{}, "direct_pay")

	if err := d.DispatchOnce(ctx); err != nil {
		t.Fatalf("DispatchOnce failed: %v", err)
	}

	state, attempts := pulseState(t, ctx, storeDB, pulseID)
	if state != "pending" {
		t.Errorf("expected state 'pending' after a timed-out attempt, got %q", state)
	}
	if attempts != 1 {
		t.Errorf("expected attempts=1 after timeout, got %d", attempts)
	}
}

func TestDispatchStubBoard500sThenRecoversMidRetry(t *testing.T) {
	ctx := context.Background()
	storeDB := newTestStore(t)
	ledger := services.NewLedgerService(storeDB)
	settings := services.NewSettingsService(storeDB)
	if err := settings.SetRelayMaxAttempts(ctx, 5); err != nil {
		t.Fatalf("SetRelayMaxAttempts failed: %v", err)
	}
	d := NewDispatcher(storeDB, ledger, settings)

	var requestCount atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requestCount.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`internal error`))
			return
		}
		_, _ = w.Write([]byte(`{"Power1":"ON"}`))
	}))
	defer srv.Close()

	machineID := seedMachine(t, ctx, storeDB)
	boardID := seedRelayBoard(t, ctx, storeDB, srv.URL)
	seedMachineRelay(t, ctx, storeDB, machineID, boardID, 1)
	userID := seedUser(t, ctx, storeDB)
	creditTokens(t, ctx, storeDB, userID, 1)
	creditTokens(t, ctx, storeDB, userID, -1)
	pulseID := seedPendingPulse(t, ctx, storeDB, machineID, sql.NullInt64{Int64: userID, Valid: true}, "token_redemption")

	for i := 0; i < 2; i++ {
		if err := d.DispatchOnce(ctx); err != nil {
			t.Fatalf("DispatchOnce (attempt %d) failed: %v", i+1, err)
		}
		state, attempts := pulseState(t, ctx, storeDB, pulseID)
		if state != "pending" {
			t.Fatalf("expected 'pending' after 500 response (attempt %d), got %q", i+1, state)
		}
		if attempts != i+1 {
			t.Fatalf("expected attempts=%d, got %d", i+1, attempts)
		}
		forceRetryNow(t, ctx, storeDB, pulseID)
	}

	// Third attempt hits the now-recovering board (n=3) and should succeed.
	if err := d.DispatchOnce(ctx); err != nil {
		t.Fatalf("DispatchOnce (recovery attempt) failed: %v", err)
	}

	state, _ := pulseState(t, ctx, storeDB, pulseID)
	if state != "sent" {
		t.Errorf("expected state 'sent' after the board recovered mid-retry, got %q", state)
	}
	if got := refundCount(t, ctx, storeDB, pulseID); got != 0 {
		t.Errorf("expected no refund since the pulse ultimately succeeded, got %d", got)
	}
	if got := balance(t, ctx, storeDB, userID); got != 0 {
		t.Errorf("expected balance to remain 0 (token stays spent on success), got %d", got)
	}
}
