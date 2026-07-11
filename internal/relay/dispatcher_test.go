package relay

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/chromatic/dogecade/internal/services"
)

// fakePulser records calls and lets tests script per-call outcomes.
type fakePulser struct {
	mu    sync.Mutex
	calls []time.Time
	fn    func(callNum int, relayNumber, pulseTimeDeciseconds int) error
}

func (f *fakePulser) Pulse(ctx context.Context, relayNumber, pulseTimeDeciseconds int) error {
	f.mu.Lock()
	f.calls = append(f.calls, time.Now())
	callNum := len(f.calls)
	f.mu.Unlock()

	if f.fn != nil {
		return f.fn(callNum, relayNumber, pulseTimeDeciseconds)
	}
	return nil
}

func (f *fakePulser) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func TestDispatchOnceSendsPulseAndMarksSent(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := services.NewLedgerService(s)
	settings := services.NewSettingsService(s)
	d := NewDispatcher(s, ledger, settings)

	fp := &fakePulser{}
	d.SetClientFactory(func(baseURL string) pulser { return fp })

	machineID := seedMachine(t, ctx, s)
	boardID := seedRelayBoard(t, ctx, s, "http://board.invalid")
	seedMachineRelay(t, ctx, s, machineID, boardID, 2)
	userID := seedUser(t, ctx, s)
	creditTokens(t, ctx, s, userID, 1)
	pulseID := seedPendingPulse(t, ctx, s, machineID, sql.NullInt64{Int64: userID, Valid: true}, "token_redemption")

	if err := d.DispatchOnce(ctx); err != nil {
		t.Fatalf("DispatchOnce failed: %v", err)
	}

	state, attempts := pulseState(t, ctx, s, pulseID)
	if state != "sent" {
		t.Errorf("expected state 'sent', got %q", state)
	}
	if attempts != 0 {
		t.Errorf("expected attempts unchanged at 0 on success, got %d", attempts)
	}
	if fp.callCount() != 1 {
		t.Errorf("expected exactly 1 pulse call, got %d", fp.callCount())
	}
}

func TestDispatchOnceRetriesOnFailureThenSucceeds(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := services.NewLedgerService(s)
	settings := services.NewSettingsService(s)
	if err := settings.SetRelayMaxAttempts(ctx, 5); err != nil {
		t.Fatalf("SetRelayMaxAttempts failed: %v", err)
	}
	d := NewDispatcher(s, ledger, settings)

	fp := &fakePulser{fn: func(callNum, relayNumber, pulseTimeDeciseconds int) error {
		if callNum < 2 {
			return fmt.Errorf("board busy")
		}
		return nil
	}}
	d.SetClientFactory(func(baseURL string) pulser { return fp })

	machineID := seedMachine(t, ctx, s)
	boardID := seedRelayBoard(t, ctx, s, "http://board.invalid")
	seedMachineRelay(t, ctx, s, machineID, boardID, 1)
	pulseID := seedPendingPulse(t, ctx, s, machineID, sql.NullInt64{}, "direct_pay")

	if err := d.DispatchOnce(ctx); err != nil {
		t.Fatalf("DispatchOnce (1st) failed: %v", err)
	}
	state, attempts := pulseState(t, ctx, s, pulseID)
	if state != "pending" {
		t.Fatalf("expected state 'pending' after 1st failed attempt, got %q", state)
	}
	if attempts != 1 {
		t.Fatalf("expected attempts=1 after 1st failed attempt, got %d", attempts)
	}

	// Backoff would normally delay the retry; force it to be immediately
	// eligible so the test doesn't need to sleep out a real backoff window.
	forceRetryNow(t, ctx, s, pulseID)

	if err := d.DispatchOnce(ctx); err != nil {
		t.Fatalf("DispatchOnce (2nd) failed: %v", err)
	}
	state, _ = pulseState(t, ctx, s, pulseID)
	if state != "sent" {
		t.Errorf("expected state 'sent' after recovery, got %q", state)
	}
	if fp.callCount() != 2 {
		t.Errorf("expected exactly 2 pulse calls, got %d", fp.callCount())
	}
}

func TestDispatchOnceExhaustionRefundsAndAlerts(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := services.NewLedgerService(s)
	settings := services.NewSettingsService(s)
	if err := settings.SetRelayMaxAttempts(ctx, 1); err != nil {
		t.Fatalf("SetRelayMaxAttempts failed: %v", err)
	}
	d := NewDispatcher(s, ledger, settings)

	fp := &fakePulser{fn: func(callNum, relayNumber, pulseTimeDeciseconds int) error {
		return fmt.Errorf("board offline")
	}}
	d.SetClientFactory(func(baseURL string) pulser { return fp })

	machineID := seedMachine(t, ctx, s)
	boardID := seedRelayBoard(t, ctx, s, "http://board.invalid")
	seedMachineRelay(t, ctx, s, machineID, boardID, 1)
	userID := seedUser(t, ctx, s)
	creditTokens(t, ctx, s, userID, 1)
	// Simulate the redemption debit that already happened before dispatch.
	creditTokens(t, ctx, s, userID, -1)
	pulseID := seedPendingPulse(t, ctx, s, machineID, sql.NullInt64{Int64: userID, Valid: true}, "token_redemption")

	if err := d.DispatchOnce(ctx); err != nil {
		t.Fatalf("DispatchOnce failed: %v", err)
	}

	state, attempts := pulseState(t, ctx, s, pulseID)
	if state != "failed" {
		t.Errorf("expected state 'failed' after exhausting attempts, got %q", state)
	}
	if attempts != 1 {
		t.Errorf("expected attempts=1, got %d", attempts)
	}
	if got := balance(t, ctx, s, userID); got != 1 {
		t.Errorf("expected balance restored to 1 after refund, got %d", got)
	}
	if got := refundCount(t, ctx, s, pulseID); got != 1 {
		t.Errorf("expected exactly 1 refund ledger row, got %d", got)
	}
	if got := alertCount(t, ctx, s, "relay_dispatch_failed"); got != 1 {
		t.Errorf("expected 1 unacked relay_dispatch_failed alert, got %d", got)
	}

	// Re-running dispatch must not touch the now-failed pulse again (it's no
	// longer 'pending'), so the refund and alert stay exactly-once.
	if err := d.DispatchOnce(ctx); err != nil {
		t.Fatalf("second DispatchOnce failed: %v", err)
	}
	if got := refundCount(t, ctx, s, pulseID); got != 1 {
		t.Errorf("expected refund to remain exactly-once after a second dispatch pass, got %d", got)
	}
}

func TestDispatchOnceDirectPayFailureDoesNotRefund(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := services.NewLedgerService(s)
	settings := services.NewSettingsService(s)
	if err := settings.SetRelayMaxAttempts(ctx, 1); err != nil {
		t.Fatalf("SetRelayMaxAttempts failed: %v", err)
	}
	d := NewDispatcher(s, ledger, settings)

	fp := &fakePulser{fn: func(callNum, relayNumber, pulseTimeDeciseconds int) error {
		return fmt.Errorf("board offline")
	}}
	d.SetClientFactory(func(baseURL string) pulser { return fp })

	machineID := seedMachine(t, ctx, s)
	boardID := seedRelayBoard(t, ctx, s, "http://board.invalid")
	seedMachineRelay(t, ctx, s, machineID, boardID, 1)
	pulseID := seedPendingPulse(t, ctx, s, machineID, sql.NullInt64{}, "direct_pay")

	if err := d.DispatchOnce(ctx); err != nil {
		t.Fatalf("DispatchOnce failed: %v", err)
	}

	state, _ := pulseState(t, ctx, s, pulseID)
	if state != "failed" {
		t.Errorf("expected state 'failed', got %q", state)
	}
	if got := refundCount(t, ctx, s, pulseID); got != 0 {
		t.Errorf("expected no refund for a direct_pay pulse (no token was ever debited), got %d", got)
	}
}

func TestDispatchOnceNoActiveBindingIsTreatedAsFailure(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := services.NewLedgerService(s)
	settings := services.NewSettingsService(s)
	if err := settings.SetRelayMaxAttempts(ctx, 1); err != nil {
		t.Fatalf("SetRelayMaxAttempts failed: %v", err)
	}
	d := NewDispatcher(s, ledger, settings)
	d.SetClientFactory(func(baseURL string) pulser { return &fakePulser{} })

	machineID := seedMachine(t, ctx, s) // no machine_relays binding at all
	userID := seedUser(t, ctx, s)
	creditTokens(t, ctx, s, userID, 1)
	creditTokens(t, ctx, s, userID, -1)
	pulseID := seedPendingPulse(t, ctx, s, machineID, sql.NullInt64{Int64: userID, Valid: true}, "token_redemption")

	if err := d.DispatchOnce(ctx); err != nil {
		t.Fatalf("DispatchOnce failed: %v", err)
	}

	state, _ := pulseState(t, ctx, s, pulseID)
	if state != "failed" {
		t.Errorf("expected state 'failed' when machine has no relay binding, got %q", state)
	}
	if got := balance(t, ctx, s, userID); got != 1 {
		t.Errorf("expected balance restored to 1, got %d", got)
	}
}

func TestDispatchOnceEnforcesPerMachineGap(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := services.NewLedgerService(s)
	settings := services.NewSettingsService(s)
	if err := settings.SetRelayPulseGapMs(ctx, 100); err != nil {
		t.Fatalf("SetRelayPulseGapMs failed: %v", err)
	}
	d := NewDispatcher(s, ledger, settings)

	fp := &fakePulser{}
	d.SetClientFactory(func(baseURL string) pulser { return fp })

	machineID := seedMachine(t, ctx, s)
	boardID := seedRelayBoard(t, ctx, s, "http://board.invalid")
	seedMachineRelay(t, ctx, s, machineID, boardID, 1)
	seedPendingPulse(t, ctx, s, machineID, sql.NullInt64{}, "direct_pay")
	seedPendingPulse(t, ctx, s, machineID, sql.NullInt64{}, "direct_pay")

	if err := d.DispatchOnce(ctx); err != nil {
		t.Fatalf("DispatchOnce failed: %v", err)
	}

	fp.mu.Lock()
	if len(fp.calls) != 2 {
		fp.mu.Unlock()
		t.Fatalf("expected 2 pulse calls, got %d", len(fp.calls))
	}
	gap := fp.calls[1].Sub(fp.calls[0])
	fp.mu.Unlock()

	if gap < 100*time.Millisecond {
		t.Errorf("expected at least 100ms between pulses to the same machine, got %v", gap)
	}
}
