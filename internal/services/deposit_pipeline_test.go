package services

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/chromatic/dogecade/internal/store"
)

// getDepositState reads a deposit from the database.
type depositRow struct {
	id         int64
	state      string
	amount     int64
	confirms   int
	blockhash  sql.NullInt64
	creditedAt sql.NullString
}

func getDeposit(t *testing.T, ctx context.Context, s *store.Store, txid string, vout uint32) *depositRow {
	row := &depositRow{}
	err := s.DB().QueryRowContext(ctx,
		"SELECT id, state, amount_koinu, confirmations, block_height, credited_at FROM deposits WHERE txid = ? AND vout = ?",
		txid, vout,
	).Scan(&row.id, &row.state, &row.amount, &row.confirms, &row.blockhash, &row.creditedAt)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		t.Fatalf("failed to query deposit: %v", err)
	}
	return row
}

// countUnackedAlertsForDeposit returns the count of unacked alerts of a given kind for a specific deposit.
func countUnackedAlertsForDeposit(t *testing.T, ctx context.Context, s *store.Store, kind string, depositID int64) int {
	var count int
	err := s.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM alerts WHERE kind = ? AND message LIKE ? AND acked_at IS NULL",
		kind,
		fmt.Sprintf("%%deposit_id:%d%%", depositID),
	).Scan(&count)
	if err != nil {
		t.Fatalf("failed to count alerts: %v", err)
	}
	return count
}

func TestDepositPipelineBasic(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	settings := NewSettingsService(s)
	pool := NewPoolService(s, settings)

	// Seed a pool address
	_ = seedPoolAddress(t, ctx, s, "D8LTMwqWJLhXBn7jrGDUGKHmfCkLV5qF7w", "token_deposit")

	// Create a pipeline with no-op credit hook
	pipeline := NewDepositPipeline(s, settings, pool, nil)

	// Test 1: Fresh PaymentEvent with confirmations=0 creates 'seen' deposit
	t.Run("seen-on-0-conf", func(t *testing.T) {
		if err := pipeline.HandleEvent(ctx, "D8LTMwqWJLhXBn7jrGDUGKHmfCkLV5qF7w", "tx1", 0, 1e8, 0, 0); err != nil {
			t.Fatalf("HandleEvent failed: %v", err)
		}

		row := getDeposit(t, ctx, s, "tx1", 0)
		if row == nil {
			t.Fatal("deposit not created")
		}
		if row.state != "seen" {
			t.Errorf("expected state=seen, got %q", row.state)
		}
		if row.amount != 1e8 {
			t.Errorf("expected amount=1e8, got %d", row.amount)
		}
		if row.confirms != 0 {
			t.Errorf("expected confirmations=0, got %d", row.confirms)
		}
	})

	// Test 2: PaymentEvent with confirmations >= min_confirmations advances seen->confirmed->credited
	t.Run("advance-on-confirm", func(t *testing.T) {
		if err := pipeline.HandleEvent(ctx, "D8LTMwqWJLhXBn7jrGDUGKHmfCkLV5qF7w", "tx2", 0, 1e8, 1, 100); err != nil {
			t.Fatalf("HandleEvent failed: %v", err)
		}

		row := getDeposit(t, ctx, s, "tx2", 0)
		if row == nil {
			t.Fatal("deposit not created")
		}
		if row.state != "credited" {
			t.Errorf("expected state=credited, got %q", row.state)
		}
		if !row.creditedAt.Valid {
			t.Error("credited_at should be set")
		}
	})

	// Test 3: Small amount with zero_conf_max_koinu goes straight through
	t.Run("zero-conf-max", func(t *testing.T) {
		// Set zero_conf_max_koinu to 5e8 (5 DOGE)
		if err := settings.SetZeroConfMaxKoinu(ctx, 5e8); err != nil {
			t.Fatalf("failed to set zero_conf_max_koinu: %v", err)
		}

		if err := pipeline.HandleEvent(ctx, "D8LTMwqWJLhXBn7jrGDUGKHmfCkLV5qF7w", "tx3", 0, 1e8, 0, 0); err != nil {
			t.Fatalf("HandleEvent failed: %v", err)
		}

		row := getDeposit(t, ctx, s, "tx3", 0)
		if row == nil {
			t.Fatal("deposit not created")
		}
		if row.state != "credited" {
			t.Errorf("expected state=credited at 0-conf, got %q", row.state)
		}
		if row.confirms != 0 {
			t.Errorf("expected confirmations=0, got %d", row.confirms)
		}
	})

	// Test 4: Re-delivering a PaymentEvent for already-credited is idempotent
	t.Run("idempotent-redelivery", func(t *testing.T) {
		// Get the original credited_at
		row1 := getDeposit(t, ctx, s, "tx2", 0)
		origCreditedAt := row1.creditedAt

		// Redeliver the event
		if err := pipeline.HandleEvent(ctx, "D8LTMwqWJLhXBn7jrGDUGKHmfCkLV5qF7w", "tx2", 0, 1e8, 1, 100); err != nil {
			t.Fatalf("HandleEvent failed: %v", err)
		}

		// Check that credited_at hasn't changed and state is still credited
		row2 := getDeposit(t, ctx, s, "tx2", 0)
		if row2.state != "credited" {
			t.Errorf("expected state=credited, got %q", row2.state)
		}
		if row2.creditedAt != origCreditedAt {
			t.Errorf("credited_at changed on redeliver: %v -> %v", origCreditedAt, row2.creditedAt)
		}
	})

	// Test 5: PaymentEvent for address NOT in pool is ignored
	t.Run("unknown-address-ignored", func(t *testing.T) {
		if err := pipeline.HandleEvent(ctx, "DUnknownAddress1234567890ABC", "tx4", 0, 1e8, 1, 101); err != nil {
			t.Fatalf("HandleEvent should not error on unknown address, got: %v", err)
		}

		row := getDeposit(t, ctx, s, "tx4", 0)
		if row != nil {
			t.Fatal("deposit should not be created for unknown address")
		}
	})
}

func TestDepositPipelineReorg(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	settings := NewSettingsService(s)
	pool := NewPoolService(s, settings)

	// Seed a pool address
	_ = seedPoolAddress(t, ctx, s, "D8LTMwqWJLhXBn7jrGDUGKHmfCkLV5qF7w", "token_deposit")

	// Create a pipeline with no-op credit hook
	pipeline := NewDepositPipeline(s, settings, pool, nil)

	// Test 1: HandleReorg on 'seen' deposit sets it to 'orphaned'
	t.Run("orphan-seen", func(t *testing.T) {
		// Create a seen deposit
		if err := pipeline.HandleEvent(ctx, "D8LTMwqWJLhXBn7jrGDUGKHmfCkLV5qF7w", "tx5", 0, 1e8, 0, 0); err != nil {
			t.Fatalf("HandleEvent failed: %v", err)
		}

		row := getDeposit(t, ctx, s, "tx5", 0)
		if row.state != "seen" {
			t.Errorf("expected state=seen, got %q", row.state)
		}

		// Reorg the transaction
		if err := pipeline.HandleReorg(ctx, "tx5", 0); err != nil {
			t.Fatalf("HandleReorg failed: %v", err)
		}

		row = getDeposit(t, ctx, s, "tx5", 0)
		if row.state != "orphaned" {
			t.Errorf("expected state=orphaned, got %q", row.state)
		}
	})

	// Test 2: HandleReorg on 'confirmed' deposit sets it to 'orphaned'
	t.Run("orphan-confirmed", func(t *testing.T) {
		// Create a confirmed deposit (via confirmed state)
		if err := pipeline.HandleEvent(ctx, "D8LTMwqWJLhXBn7jrGDUGKHmfCkLV5qF7w", "tx6", 0, 1e8, 1, 100); err != nil {
			t.Fatalf("HandleEvent failed: %v", err)
		}

		// At this point it should be credited (because confirmed deposits are auto-credited)
		row := getDeposit(t, ctx, s, "tx6", 0)
		if row.state == "credited" {
			// Manual downgrade to 'confirmed' for testing
			_, err := s.DB().ExecContext(ctx, "UPDATE deposits SET state = 'confirmed', credited_at = NULL WHERE txid = ? AND vout = ?", "tx6", 0)
			if err != nil {
				t.Fatalf("failed to downgrade to confirmed: %v", err)
			}
		}

		// Now reorg it
		if err := pipeline.HandleReorg(ctx, "tx6", 0); err != nil {
			t.Fatalf("HandleReorg failed: %v", err)
		}

		row = getDeposit(t, ctx, s, "tx6", 0)
		if row.state != "orphaned" {
			t.Errorf("expected state=orphaned, got %q", row.state)
		}
	})

	// Test 3: HandleReorg on 'credited' deposit leaves it 'credited' but inserts alert
	t.Run("alert-on-credited-reorg", func(t *testing.T) {
		// Create a credited deposit
		if err := pipeline.HandleEvent(ctx, "D8LTMwqWJLhXBn7jrGDUGKHmfCkLV5qF7w", "tx7", 0, 1e8, 1, 100); err != nil {
			t.Fatalf("HandleEvent failed: %v", err)
		}

		row := getDeposit(t, ctx, s, "tx7", 0)
		if row.state != "credited" {
			t.Errorf("expected state=credited, got %q", row.state)
		}

		// Reorg it
		if err := pipeline.HandleReorg(ctx, "tx7", 0); err != nil {
			t.Fatalf("HandleReorg failed: %v", err)
		}

		// Should still be credited
		row = getDeposit(t, ctx, s, "tx7", 0)
		if row.state != "credited" {
			t.Errorf("expected state=credited after reorg, got %q", row.state)
		}

		// Should have an unacked alert for this deposit
		alertCount := countUnackedAlertsForDeposit(t, ctx, s, "deposit_reorged_after_credit", row.id)
		if alertCount != 1 {
			t.Errorf("expected 1 unacked alert for this deposit, got %d", alertCount)
		}
	})

	// Test 4: Second HandleReorg on same credited deposit doesn't duplicate alert
	t.Run("no-duplicate-alert", func(t *testing.T) {
		// Create a credited deposit
		if err := pipeline.HandleEvent(ctx, "D8LTMwqWJLhXBn7jrGDUGKHmfCkLV5qF7w", "tx8", 0, 1e8, 1, 100); err != nil {
			t.Fatalf("HandleEvent failed: %v", err)
		}

		row := getDeposit(t, ctx, s, "tx8", 0)
		if row == nil {
			t.Fatal("deposit tx8 not found")
		}

		// First reorg
		if err := pipeline.HandleReorg(ctx, "tx8", 0); err != nil {
			t.Fatalf("first HandleReorg failed: %v", err)
		}

		alertCount1 := countUnackedAlertsForDeposit(t, ctx, s, "deposit_reorged_after_credit", row.id)
		if alertCount1 != 1 {
			t.Errorf("expected 1 alert after first reorg, got %d", alertCount1)
		}

		// Second reorg (same event)
		if err := pipeline.HandleReorg(ctx, "tx8", 0); err != nil {
			t.Fatalf("second HandleReorg failed: %v", err)
		}

		alertCount2 := countUnackedAlertsForDeposit(t, ctx, s, "deposit_reorged_after_credit", row.id)
		if alertCount2 != 1 {
			t.Errorf("expected still 1 alert after second reorg, got %d", alertCount2)
		}
	})
}

func TestDepositPipelineCreditHookError(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	settings := NewSettingsService(s)
	pool := NewPoolService(s, settings)

	// Seed a pool address
	_ = seedPoolAddress(t, ctx, s, "D8LTMwqWJLhXBn7jrGDUGKHmfCkLV5qF7w", "token_deposit")

	// Create a pipeline with a hook that fails
	failingHook := func(ctx context.Context, depositID int64) error {
		return os.ErrPermission // arbitrary error for testing
	}
	pipeline := NewDepositPipeline(s, settings, pool, failingHook)

	t.Run("credit-hook-error-leaves-confirmed", func(t *testing.T) {
		err := pipeline.HandleEvent(ctx, "D8LTMwqWJLhXBn7jrGDUGKHmfCkLV5qF7w", "tx9", 0, 1e8, 1, 100)
		if err == nil {
			t.Fatal("HandleEvent should return error from credit hook")
		}

		// Deposit should be at 'confirmed' state (not credited), retryable
		row := getDeposit(t, ctx, s, "tx9", 0)
		if row == nil {
			t.Fatal("deposit should exist")
		}
		if row.state != "confirmed" {
			t.Errorf("expected state=confirmed after hook error, got %q", row.state)
		}
		if row.creditedAt.Valid {
			t.Error("credited_at should not be set when hook fails")
		}
	})
}

func TestDepositPipelineUpdateConfirmations(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	settings := NewSettingsService(s)
	pool := NewPoolService(s, settings)

	// Seed a pool address
	_ = seedPoolAddress(t, ctx, s, "D8LTMwqWJLhXBn7jrGDUGKHmfCkLV5qF7w", "token_deposit")

	pipeline := NewDepositPipeline(s, settings, pool, nil)

	t.Run("confirmation-update", func(t *testing.T) {
		// First event: 0 confirmations, state=seen
		if err := pipeline.HandleEvent(ctx, "D8LTMwqWJLhXBn7jrGDUGKHmfCkLV5qF7w", "tx10", 0, 1e8, 0, 0); err != nil {
			t.Fatalf("HandleEvent failed: %v", err)
		}

		row := getDeposit(t, ctx, s, "tx10", 0)
		if row.state != "seen" {
			t.Errorf("expected state=seen, got %q", row.state)
		}
		if row.confirms != 0 {
			t.Errorf("expected confirmations=0, got %d", row.confirms)
		}

		// Second event: 1 confirmation, state=credited
		if err := pipeline.HandleEvent(ctx, "D8LTMwqWJLhXBn7jrGDUGKHmfCkLV5qF7w", "tx10", 0, 1e8, 1, 100); err != nil {
			t.Fatalf("HandleEvent failed: %v", err)
		}

		row = getDeposit(t, ctx, s, "tx10", 0)
		if row.state != "credited" {
			t.Errorf("expected state=credited, got %q", row.state)
		}
		if row.confirms != 1 {
			t.Errorf("expected confirmations=1, got %d", row.confirms)
		}
	})
}

func TestDepositPipelineOrphanedIsFinal(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	settings := NewSettingsService(s)
	pool := NewPoolService(s, settings)
	pipeline := NewDepositPipeline(s, settings, pool, nil)

	// Seed a pool address
	seedPoolAddress(t, ctx, s, "D8LTMwqWJLhXBn7jrGDUGKHmfCkLV5qF7w", "token_deposit")

	// Create a deposit in seen state
	if err := pipeline.HandleEvent(ctx, "D8LTMwqWJLhXBn7jrGDUGKHmfCkLV5qF7w", "tx_orphan", 0, 1e8, 0, 0); err != nil {
		t.Fatalf("first HandleEvent failed: %v", err)
	}

	row := getDeposit(t, ctx, s, "tx_orphan", 0)
	if row.state != "seen" {
		t.Errorf("expected initial state=seen, got %q", row.state)
	}

	// Reorg it to orphaned
	if err := pipeline.HandleReorg(ctx, "tx_orphan", 0); err != nil {
		t.Fatalf("HandleReorg failed: %v", err)
	}

	row = getDeposit(t, ctx, s, "tx_orphan", 0)
	if row.state != "orphaned" {
		t.Errorf("expected state=orphaned after reorg, got %q", row.state)
	}

	// Try to "confirm" the orphaned deposit by sending a confirmation event
	if err := pipeline.HandleEvent(ctx, "D8LTMwqWJLhXBn7jrGDUGKHmfCkLV5qF7w", "tx_orphan", 0, 1e8, 1, 100); err != nil {
		t.Fatalf("second HandleEvent failed: %v", err)
	}

	// Should still be orphaned (orphaned is a terminal state)
	row = getDeposit(t, ctx, s, "tx_orphan", 0)
	if row.state != "orphaned" {
		t.Errorf("expected state=orphaned (terminal), got %q after HandleEvent on orphaned", row.state)
	}
}

func TestDepositPipelineHandleReorgOnOrphanedIsNoop(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	settings := NewSettingsService(s)
	pool := NewPoolService(s, settings)
	pipeline := NewDepositPipeline(s, settings, pool, nil)

	// Seed a pool address
	seedPoolAddress(t, ctx, s, "D8LTMwqWJLhXBn7jrGDUGKHmfCkLV5qF7w", "token_deposit")

	// Create and orphan a deposit
	if err := pipeline.HandleEvent(ctx, "D8LTMwqWJLhXBn7jrGDUGKHmfCkLV5qF7w", "tx_orphan2", 0, 1e8, 0, 0); err != nil {
		t.Fatalf("HandleEvent failed: %v", err)
	}

	if err := pipeline.HandleReorg(ctx, "tx_orphan2", 0); err != nil {
		t.Fatalf("first HandleReorg failed: %v", err)
	}

	// Reorg it again - should be a no-op
	if err := pipeline.HandleReorg(ctx, "tx_orphan2", 0); err != nil {
		t.Fatalf("second HandleReorg failed: %v", err)
	}

	// State should still be orphaned
	row := getDeposit(t, ctx, s, "tx_orphan2", 0)
	if row.state != "orphaned" {
		t.Errorf("expected state=orphaned, got %q", row.state)
	}
}

func TestDepositPipelineAlertLikePatternDoesntMatchSimilarIDs(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	settings := NewSettingsService(s)
	pool := NewPoolService(s, settings)
	pipeline := NewDepositPipeline(s, settings, pool, nil)

	// Seed a pool address
	seedPoolAddress(t, ctx, s, "D8LTMwqWJLhXBn7jrGDUGKHmfCkLV5qF7w", "token_deposit")

	// Create two deposits: one with ID 123, one with ID 1234
	// We'll insert them manually to control their IDs for testing the LIKE pattern
	now := time.Now().UTC().Format(time.RFC3339)

	// Get the address_id
	var addressID int64
	err := s.DB().QueryRowContext(ctx,
		"SELECT id FROM addresses WHERE address = ?",
		"D8LTMwqWJLhXBn7jrGDUGKHmfCkLV5qF7w",
	).Scan(&addressID)
	if err != nil {
		t.Fatalf("failed to get address: %v", err)
	}

	// Insert two deposits with specific IDs
	result, err := s.DB().ExecContext(ctx,
		"INSERT INTO deposits (address_id, txid, vout, amount_koinu, confirmations, block_height, state, credited_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		addressID, "tx_123", 0, int64(1e8), 1, 100, "credited", now, now,
	)
	if err != nil {
		t.Fatalf("failed to insert first deposit: %v", err)
	}
	deposit123ID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("failed to get first deposit id: %v", err)
	}

	result, err = s.DB().ExecContext(ctx,
		"INSERT INTO deposits (address_id, txid, vout, amount_koinu, confirmations, block_height, state, credited_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		addressID, "tx_1234", 0, int64(1e8), 1, 100, "credited", now, now,
	)
	if err != nil {
		t.Fatalf("failed to insert second deposit: %v", err)
	}
	deposit1234ID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("failed to get second deposit id: %v", err)
	}

	// Reorg the first deposit (ID=123)
	if err := pipeline.HandleReorg(ctx, "tx_123", 0); err != nil {
		t.Fatalf("first HandleReorg failed: %v", err)
	}

	// Check that an alert was created for deposit 123
	var alert123Count int
	err = s.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM alerts WHERE kind = 'deposit_reorged_after_credit' AND message LIKE ?",
		fmt.Sprintf("%%deposit_id:%d,%%", deposit123ID),
	).Scan(&alert123Count)
	if err != nil {
		t.Fatalf("failed to query alerts for 123: %v", err)
	}
	if alert123Count != 1 {
		t.Errorf("expected 1 alert for deposit 123, got %d", alert123Count)
	}

	// The LIKE pattern should NOT match deposit 1234 (the bug would be if it did)
	var alert1234Count int
	err = s.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM alerts WHERE kind = 'deposit_reorged_after_credit' AND message LIKE ?",
		fmt.Sprintf("%%deposit_id:%d,%%", deposit1234ID),
	).Scan(&alert1234Count)
	if err != nil && err != sql.ErrNoRows {
		t.Fatalf("failed to query alerts for 1234: %v", err)
	}
	if alert1234Count != 0 {
		t.Errorf("expected 0 alerts for deposit 1234 (no reorg sent), got %d", alert1234Count)
	}

	// Now reorg deposit 1234 and verify its alert is separate
	if err := pipeline.HandleReorg(ctx, "tx_1234", 0); err != nil {
		t.Fatalf("second HandleReorg failed: %v", err)
	}

	err = s.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM alerts WHERE kind = 'deposit_reorged_after_credit' AND message LIKE ?",
		fmt.Sprintf("%%deposit_id:%d,%%", deposit1234ID),
	).Scan(&alert1234Count)
	if err != nil {
		t.Fatalf("failed to query alerts for 1234 after reorg: %v", err)
	}
	if alert1234Count != 1 {
		t.Errorf("expected 1 alert for deposit 1234 after reorg, got %d", alert1234Count)
	}

	// Verify both alerts still exist separately
	var totalAlerts int
	err = s.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM alerts WHERE kind = 'deposit_reorged_after_credit' AND acked_at IS NULL",
	).Scan(&totalAlerts)
	if err != nil {
		t.Fatalf("failed to count total alerts: %v", err)
	}
	if totalAlerts != 2 {
		t.Errorf("expected 2 total unacked alerts, got %d", totalAlerts)
	}
}
