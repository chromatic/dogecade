package relay

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/chromatic/dogecade/internal/services"
	"github.com/chromatic/dogecade/internal/store"
)

// pulser is the subset of *Client used by the dispatcher, abstracted so
// tests can inject fakes keyed by board base URL instead of standing up a
// real HTTP server for every failure scenario.
type pulser interface {
	Pulse(ctx context.Context, relayNumber int, pulseTimeDeciseconds int) error
}

// ClientFactory creates a pulser for a board's base URL. Defaults to
// wrapping NewClient; overridable in tests via Dispatcher.SetClientFactory.
type ClientFactory func(baseURL string) pulser

func defaultClientFactory(baseURL string) pulser {
	return NewClient(baseURL)
}

const (
	defaultPollInterval          = 2 * time.Second
	backoffBase                  = 500 * time.Millisecond
	backoffCap                   = 30 * time.Second
	dispatchPulseTimeDeciseconds = DefaultPulseTimeDeciseconds
	// attemptTimeout bounds a single Tasmota HTTP call so a hanging board
	// can't stall the dispatcher indefinitely; it's independent of the
	// overall ctx passed to DispatchOnce, which stays live for bookkeeping
	// writes even after an attempt's sub-context expires.
	attemptTimeout = 5 * time.Second
)

// Dispatcher turns pending credit_pulses rows into Tasmota HTTP pulses. On
// exhaustion of retries it marks the pulse failed, refunds the token
// (token_redemption source only), and raises an alert.
//
// There is no separate "claimed" pulse state: the dispatcher processes
// pending pulses sequentially in a single goroutine (see Run), so a crash
// mid-dispatch simply leaves the row in 'pending' with its prior attempts
// count — the next poll picks it up again. This trades a small chance of a
// duplicate physical pulse (if the crash lands after the HTTP call
// succeeded but before the row was marked 'sent') for simplicity; a
// physical coin-switch trigger can't be made exactly-once without hardware
// idempotency the boards don't offer.
type Dispatcher struct {
	store          *store.Store
	ledger         *services.LedgerService
	settings       *services.SettingsService
	clientFactory  ClientFactory
	pollInterval   time.Duration
	attemptTimeout time.Duration

	mu         sync.Mutex
	lastSentAt map[int64]time.Time // machine_id -> last time we engaged its relay
}

// NewDispatcher creates a Dispatcher wired to the given store, ledger, and
// settings service.
func NewDispatcher(s *store.Store, ledger *services.LedgerService, settings *services.SettingsService) *Dispatcher {
	return &Dispatcher{
		store:          s,
		ledger:         ledger,
		settings:       settings,
		clientFactory:  defaultClientFactory,
		pollInterval:   defaultPollInterval,
		attemptTimeout: attemptTimeout,
		lastSentAt:     make(map[int64]time.Time),
	}
}

// SetClientFactory overrides how the dispatcher builds a pulser for a given
// board base URL. Intended for tests.
func (d *Dispatcher) SetClientFactory(f ClientFactory) {
	d.clientFactory = f
}

// SetPollInterval overrides the polling interval. Intended for tests.
func (d *Dispatcher) SetPollInterval(interval time.Duration) {
	d.pollInterval = interval
}

// SetAttemptTimeout overrides how long a single Tasmota HTTP call is allowed
// to take before it's treated as a failed attempt. Intended for tests.
func (d *Dispatcher) SetAttemptTimeout(timeout time.Duration) {
	d.attemptTimeout = timeout
}

// Run starts the dispatch loop. It polls for eligible pending pulses at
// pollInterval until ctx is cancelled. Should be called in a goroutine.
func (d *Dispatcher) Run(ctx context.Context) {
	if err := d.DispatchOnce(ctx); err != nil {
		slog.Error("relay dispatch failed", "err", err)
	}

	ticker := time.NewTicker(d.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := d.DispatchOnce(ctx); err != nil {
				slog.Error("relay dispatch failed", "err", err)
			}
		}
	}
}

// pendingPulse is a row read from credit_pulses in 'pending' state.
type pendingPulse struct {
	ID        int64
	MachineID int64
	UserID    sql.NullInt64
	Source    string
	Attempts  int
}

// DispatchOnce processes every pending, currently-eligible credit_pulses
// row once: resolving each machine's relay binding, firing the pulse, and
// recording success, retry, or failure+refund. Errors dispatching an
// individual pulse do not stop processing of the others; only a failure to
// query the pending set itself is returned.
func (d *Dispatcher) DispatchOnce(ctx context.Context) error {
	pulses, err := d.fetchPendingPulses(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch pending pulses: %w", err)
	}

	for _, p := range pulses {
		d.processPulse(ctx, p)
	}

	return nil
}

func (d *Dispatcher) fetchPendingPulses(ctx context.Context) ([]pendingPulse, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	rows, err := d.store.DB().QueryContext(ctx, `
		SELECT id, machine_id, user_id, source, attempts
		FROM credit_pulses
		WHERE state = 'pending' AND (next_attempt_at IS NULL OR next_attempt_at <= ?)
		ORDER BY machine_id, id`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var pulses []pendingPulse
	for rows.Next() {
		var p pendingPulse
		if err := rows.Scan(&p.ID, &p.MachineID, &p.UserID, &p.Source, &p.Attempts); err != nil {
			return nil, err
		}
		pulses = append(pulses, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return pulses, nil
}

// processPulse resolves the machine's relay binding, waits out the
// per-machine spacing gap, fires the pulse, and records the outcome. The
// HTTP attempt itself is bounded by its own sub-context (attemptTimeout)
// derived from ctx, so a slow/hanging board can't also poison the
// bookkeeping writes that follow — those use the original ctx, which stays
// live even if the attempt's sub-context expired.
func (d *Dispatcher) processPulse(ctx context.Context, p pendingPulse) {
	d.waitForGap(ctx, p.MachineID)

	binding, err := d.resolveBinding(ctx, p.MachineID)
	if err == nil {
		attemptCtx, cancel := context.WithTimeout(ctx, d.attemptTimeout)
		client := d.clientFactory(binding.BaseURL)
		err = client.Pulse(attemptCtx, binding.RelayNumber, dispatchPulseTimeDeciseconds)
		cancel()
	}

	d.recordAttempt(p.MachineID)

	if err == nil {
		if markErr := d.markSent(ctx, p.ID); markErr != nil {
			slog.Error("failed to mark pulse sent", "pulse_id", p.ID, "err", markErr)
		}
		return
	}

	attempt := p.Attempts + 1
	maxAttempts, settingsErr := d.settings.GetRelayMaxAttempts(ctx)
	if settingsErr != nil {
		slog.Error("failed to read relay_max_attempts, using default", "err", settingsErr)
		maxAttempts = 5
	}

	if attempt >= maxAttempts {
		if failErr := d.markFailedAndRefund(ctx, p, attempt, err); failErr != nil {
			slog.Error("failed to mark pulse failed", "pulse_id", p.ID, "err", failErr)
		}
		return
	}

	if retryErr := d.markPendingRetry(ctx, p.ID, attempt, err); retryErr != nil {
		slog.Error("failed to schedule pulse retry", "pulse_id", p.ID, "err", retryErr)
	}
}

// waitForGap blocks until at least the configured spacing gap has elapsed
// since the last pulse (successful or not) sent to this machine, so a
// cabinet's coin-switch scan matrix can register consecutive pulses
// separately. A single dispatcher goroutine processes pulses sequentially,
// so blocking here is a deliberate, simple way to enforce the gap.
func (d *Dispatcher) waitForGap(ctx context.Context, machineID int64) {
	gapMs, err := d.settings.GetRelayPulseGapMs(ctx)
	if err != nil {
		gapMs = 750
	}
	gap := time.Duration(gapMs) * time.Millisecond

	d.mu.Lock()
	last, ok := d.lastSentAt[machineID]
	d.mu.Unlock()
	if !ok {
		return
	}

	wait := gap - time.Since(last)
	if wait <= 0 {
		return
	}

	select {
	case <-time.After(wait):
	case <-ctx.Done():
	}
}

func (d *Dispatcher) recordAttempt(machineID int64) {
	d.mu.Lock()
	d.lastSentAt[machineID] = time.Now()
	d.mu.Unlock()
}

// TestFire immediately pulses the machine's bound relay, bypassing the
// credit_pulses queue entirely. Used by the admin "test fire" button/CLI to
// manually verify a machine's wiring, not by the redemption flow.
func (d *Dispatcher) TestFire(ctx context.Context, machineID int64) error {
	binding, err := d.resolveBinding(ctx, machineID)
	if err != nil {
		return err
	}
	client := d.clientFactory(binding.BaseURL)
	if err := client.Pulse(ctx, binding.RelayNumber, dispatchPulseTimeDeciseconds); err != nil {
		return fmt.Errorf("failed to pulse machine %d: %w", machineID, err)
	}
	return nil
}

type relayBinding struct {
	BaseURL     string
	RelayNumber int
}

// resolveBinding finds the single active relay board/channel bound to a
// machine. Returns an error (treated like any other dispatch failure, so
// it's subject to the same retry/exhaustion/refund handling) if no active
// binding exists.
func (d *Dispatcher) resolveBinding(ctx context.Context, machineID int64) (relayBinding, error) {
	var b relayBinding
	err := d.store.DB().QueryRowContext(ctx, `
		SELECT rb.base_url, mr.relay_number
		FROM machine_relays mr
		JOIN relay_boards rb ON rb.id = mr.board_id
		WHERE mr.machine_id = ? AND mr.is_active = 1 AND rb.is_active = 1
		LIMIT 1`, machineID).Scan(&b.BaseURL, &b.RelayNumber)
	if err == sql.ErrNoRows {
		return relayBinding{}, fmt.Errorf("no active relay binding for machine %d", machineID)
	}
	if err != nil {
		return relayBinding{}, fmt.Errorf("failed to resolve relay binding for machine %d: %w", machineID, err)
	}
	return b, nil
}

func (d *Dispatcher) markSent(ctx context.Context, pulseID int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.store.DB().ExecContext(ctx,
		"UPDATE credit_pulses SET state = 'sent', sent_at = ?, next_attempt_at = NULL WHERE id = ?",
		now, pulseID,
	)
	return err
}

func (d *Dispatcher) markPendingRetry(ctx context.Context, pulseID int64, attempt int, dispatchErr error) error {
	nextAttemptAt := time.Now().UTC().Add(backoffDuration(attempt)).Format(time.RFC3339)
	_, err := d.store.DB().ExecContext(ctx,
		"UPDATE credit_pulses SET attempts = ?, last_error = ?, next_attempt_at = ? WHERE id = ?",
		attempt, dispatchErr.Error(), nextAttemptAt, pulseID,
	)
	return err
}

// markFailedAndRefund marks the pulse failed, refunds the token if this was
// a token_redemption pulse, and raises a deduped admin alert.
func (d *Dispatcher) markFailedAndRefund(ctx context.Context, p pendingPulse, attempt int, dispatchErr error) error {
	_, err := d.store.DB().ExecContext(ctx,
		"UPDATE credit_pulses SET state = 'failed', attempts = ?, last_error = ? WHERE id = ?",
		attempt, dispatchErr.Error(), p.ID,
	)
	if err != nil {
		return fmt.Errorf("failed to mark pulse %d failed: %w", p.ID, err)
	}

	if p.Source == "token_redemption" && p.UserID.Valid {
		note := fmt.Sprintf("relay dispatch failed after %d attempts: %v", attempt, dispatchErr)
		if err := d.ledger.Refund(ctx, p.UserID.Int64, p.ID, note); err != nil {
			return fmt.Errorf("failed to refund pulse %d: %w", p.ID, err)
		}
	}

	message := fmt.Sprintf("Relay dispatch failed for machine %d (pulse %d) after %d attempts: %v", p.MachineID, p.ID, attempt, dispatchErr)
	if err := services.InsertAlertIfNotExists(ctx, d.store.DB(), "relay_dispatch_failed", message); err != nil {
		return fmt.Errorf("failed to insert relay_dispatch_failed alert: %w", err)
	}

	return nil
}

// backoffDuration returns an exponential backoff delay for the given
// (1-indexed) attempt number, capped at backoffCap.
func backoffDuration(attempt int) time.Duration {
	d := backoffBase * time.Duration(math.Pow(2, float64(attempt-1)))
	if d > backoffCap {
		return backoffCap
	}
	return d
}
