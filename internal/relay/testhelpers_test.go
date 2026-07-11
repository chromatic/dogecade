package relay

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromatic/dogecade/internal/store"
)

var seedCounter atomic.Int64

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seedUser(t *testing.T, ctx context.Context, s *store.Store) int64 {
	t.Helper()
	n := seedCounter.Add(1)
	now := time.Now().UTC().Format(time.RFC3339)
	var id int64
	err := s.DB().QueryRowContext(ctx,
		"INSERT INTO users (subject_hash, created_at) VALUES (?, ?) RETURNING id",
		fmt.Sprintf("subject-%d", n), now,
	).Scan(&id)
	if err != nil {
		t.Fatalf("failed to seed user: %v", err)
	}
	return id
}

func seedMachine(t *testing.T, ctx context.Context, s *store.Store) int64 {
	t.Helper()
	n := seedCounter.Add(1)
	slug := fmt.Sprintf("machine-%d", n)
	now := time.Now().UTC().Format(time.RFC3339)
	var id int64
	err := s.DB().QueryRowContext(ctx,
		"INSERT INTO machines (slug, name, is_active, created_at) VALUES (?, ?, 1, ?) RETURNING id",
		slug, slug, now,
	).Scan(&id)
	if err != nil {
		t.Fatalf("failed to seed machine: %v", err)
	}
	return id
}

func seedRelayBoard(t *testing.T, ctx context.Context, s *store.Store, baseURL string) int64 {
	t.Helper()
	n := seedCounter.Add(1)
	var id int64
	err := s.DB().QueryRowContext(ctx,
		"INSERT INTO relay_boards (name, base_url, is_active) VALUES (?, ?, 1) RETURNING id",
		fmt.Sprintf("board-%d", n), baseURL,
	).Scan(&id)
	if err != nil {
		t.Fatalf("failed to seed relay board: %v", err)
	}
	return id
}

func seedMachineRelay(t *testing.T, ctx context.Context, s *store.Store, machineID, boardID int64, relayNumber int) {
	t.Helper()
	_, err := s.DB().ExecContext(ctx,
		"INSERT INTO machine_relays (machine_id, board_id, relay_number, is_active) VALUES (?, ?, ?, 1)",
		machineID, boardID, relayNumber,
	)
	if err != nil {
		t.Fatalf("failed to seed machine_relay: %v", err)
	}
}

// seedPendingPulse inserts a pending credit_pulses row. userID may be
// sql.NullInt64{} (invalid) for a direct_pay pulse with no bound user.
func seedPendingPulse(t *testing.T, ctx context.Context, s *store.Store, machineID int64, userID sql.NullInt64, source string) int64 {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	var id int64
	err := s.DB().QueryRowContext(ctx,
		`INSERT INTO credit_pulses (machine_id, user_id, source, state, attempts, created_at)
		 VALUES (?, ?, ?, 'pending', 0, ?) RETURNING id`,
		machineID, userID, source, now,
	).Scan(&id)
	if err != nil {
		t.Fatalf("failed to seed pending pulse: %v", err)
	}
	return id
}

func creditTokens(t *testing.T, ctx context.Context, s *store.Store, userID, tokens int64) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.DB().ExecContext(ctx,
		"INSERT INTO token_ledger (user_id, delta, kind, note, created_at) VALUES (?, ?, 'admin_adjust', 'test seed', ?)",
		userID, tokens, now,
	)
	if err != nil {
		t.Fatalf("failed to credit tokens: %v", err)
	}
}

func pulseState(t *testing.T, ctx context.Context, s *store.Store, pulseID int64) (state string, attempts int) {
	t.Helper()
	err := s.DB().QueryRowContext(ctx,
		"SELECT state, attempts FROM credit_pulses WHERE id = ?", pulseID,
	).Scan(&state, &attempts)
	if err != nil {
		t.Fatalf("failed to query pulse state: %v", err)
	}
	return state, attempts
}

func forceRetryNow(t *testing.T, ctx context.Context, s *store.Store, pulseID int64) {
	t.Helper()
	_, err := s.DB().ExecContext(ctx,
		"UPDATE credit_pulses SET next_attempt_at = NULL WHERE id = ?", pulseID,
	)
	if err != nil {
		t.Fatalf("failed to force retry: %v", err)
	}
}

func balance(t *testing.T, ctx context.Context, s *store.Store, userID int64) int64 {
	t.Helper()
	var bal int64
	err := s.DB().QueryRowContext(ctx,
		"SELECT COALESCE(SUM(delta), 0) FROM token_ledger WHERE user_id = ?", userID,
	).Scan(&bal)
	if err != nil {
		t.Fatalf("failed to query balance: %v", err)
	}
	return bal
}

func alertCount(t *testing.T, ctx context.Context, s *store.Store, kind string) int {
	t.Helper()
	var count int
	err := s.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM alerts WHERE kind = ? AND acked_at IS NULL", kind,
	).Scan(&count)
	if err != nil {
		t.Fatalf("failed to count alerts: %v", err)
	}
	return count
}

func refundCount(t *testing.T, ctx context.Context, s *store.Store, pulseID int64) int {
	t.Helper()
	var count int
	err := s.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM token_ledger WHERE kind = 'refund' AND pulse_id = ?", pulseID,
	).Scan(&count)
	if err != nil {
		t.Fatalf("failed to count refunds: %v", err)
	}
	return count
}
