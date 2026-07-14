package services

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/chromatic/dogecade/internal/store"
)

// CreditHook is a callback function that credits a deposit when it reaches
// the confirmed state. It receives the deposit ID for reference.
// Phase 4's token core will replace this with real ledger crediting.
// Returning an error leaves the deposit at 'confirmed' (retryable) and propagates the error.
type CreditHook func(ctx context.Context, depositID int64) error

// NoopCreditHook is a default no-op credit hook used when none is provided.
// It simply returns nil, allowing deposits to advance through the state machine
// without performing any actual crediting (suitable for testing or early phases).
func NoopCreditHook(ctx context.Context, depositID int64) error {
	return nil
}

// DepositPipeline consumes PaymentEvents from the chain watcher and manages
// the deposit state machine: seen → confirmed → credited (or orphaned on reorg).
type DepositPipeline struct {
	store    *store.Store
	settings *SettingsService
	pool     *PoolService
	credit   CreditHook
}

// NewDepositPipeline creates a new DepositPipeline.
// If credit is nil, uses the no-op hook by default.
func NewDepositPipeline(s *store.Store, settings *SettingsService, pool *PoolService, credit CreditHook) *DepositPipeline {
	if credit == nil {
		credit = NoopCreditHook
	}
	return &DepositPipeline{
		store:    s,
		settings: settings,
		pool:     pool,
		credit:   credit,
	}
}

// HandleEvent processes a payment event from the chain watcher.
// It follows the deposit state machine:
// 1. Look up the address_id via the pool. If not found (not one of ours), return nil (no error).
// 2. Upsert the deposit in 'seen' state.
// 3. Advance the state machine based on confirmation thresholds.
// Returns an error if the credit hook fails (deposit remains at 'confirmed' for retry).
func (p *DepositPipeline) HandleEvent(ctx context.Context, address, txid string, vout uint32, amountKoinu int64, confirmations int, blockHeight int64) error {
	// Step 1: Look up the address in the pool
	addressID, err := p.pool.AddressIDByAddress(ctx, address)
	if err != nil {
		return fmt.Errorf("failed to lookup address: %w", err)
	}
	if addressID == 0 {
		// Not one of ours; ignore
		return nil
	}

	// Step 2 & 3: Upsert and advance state machine in a transaction
	return p.upsertAndAdvance(ctx, addressID, address, txid, vout, amountKoinu, confirmations, blockHeight)
}

// HandleReorg processes a reorg-removed transaction.
// For deposits in 'seen' or 'confirmed' state, sets them to 'orphaned'.
// For deposits in 'credited' state, leaves them as-is but inserts an alert
// (deposit_reorged_after_credit) with dedup to avoid spam on repeated reorg calls.
func (p *DepositPipeline) HandleReorg(ctx context.Context, txid string, vout uint32) error {
	// Look up the deposit by (txid, vout)
	var depositID int64
	var state string
	err := p.store.DB().QueryRowContext(ctx,
		"SELECT id, state FROM deposits WHERE txid = ? AND vout = ?",
		txid, vout,
	).Scan(&depositID, &state)
	if err == sql.ErrNoRows {
		// No deposit for this transaction; no-op
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to lookup deposit: %w", err)
	}

	switch state {
	case "seen", "confirmed":
		// Mark as orphaned
		_, err := p.store.DB().ExecContext(ctx,
			"UPDATE deposits SET state = 'orphaned' WHERE id = ?",
			depositID,
		)
		if err != nil {
			return fmt.Errorf("failed to orphan deposit: %w", err)
		}
	case "credited":
		// Do not modify state; insert alert with dedup
		return p.insertReorgAlert(ctx, depositID, txid)
	}
	// If state is already 'orphaned', no-op

	return nil
}

// upsertAndAdvance handles the upsert and state machine logic atomically.
func (p *DepositPipeline) upsertAndAdvance(ctx context.Context, addressID int64, address, txid string, vout uint32, amountKoinu int64, confirmations int, blockHeight int64) error {
	// Read settings BEFORE starting transaction to avoid nested DB queries
	minConf, err := p.settings.GetMinConfirmations(ctx)
	if err != nil {
		return fmt.Errorf("failed to get min_confirmations: %w", err)
	}

	zeroConfMax, err := p.settings.GetZeroConfMaxKoinu(ctx)
	if err != nil {
		return fmt.Errorf("failed to get zero_conf_max_koinu: %w", err)
	}

	tx, err := p.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UTC().Format(time.RFC3339)

	// Upsert the deposit using ON CONFLICT ... DO UPDATE
	var depositID int64
	var currentState string

	// Try to look up existing deposit
	lookupErr := tx.QueryRowContext(ctx,
		"SELECT id, state FROM deposits WHERE txid = ? AND vout = ?",
		txid, vout,
	).Scan(&depositID, &currentState)

	if lookupErr == sql.ErrNoRows {
		// Deposit doesn't exist; insert it
		result, err := tx.ExecContext(ctx, `
			INSERT INTO deposits (address_id, txid, vout, amount_koinu, confirmations, block_height, state, created_at)
			VALUES (?, ?, ?, ?, ?, ?, 'seen', ?)
		`,
			addressID, txid, vout, amountKoinu, confirmations,
			func() interface{} {
				if blockHeight > 0 {
					return blockHeight
				}
				return nil
			}(),
			now,
		)
		if err != nil {
			return fmt.Errorf("failed to insert deposit: %w", err)
		}
		lastID, err := result.LastInsertId()
		if err != nil {
			return fmt.Errorf("failed to get last insert id: %w", err)
		}
		depositID = lastID
		currentState = "seen"
	} else if lookupErr != nil {
		return fmt.Errorf("failed to lookup deposit: %w", lookupErr)
	} else {
		// Deposit exists; update it
		_, err := tx.ExecContext(ctx, `
			UPDATE deposits SET
				confirmations = ?,
				block_height = CASE WHEN ? > 0 THEN ? ELSE block_height END
			WHERE id = ?
		`,
			confirmations,
			blockHeight, blockHeight,
			depositID,
		)
		if err != nil {
			return fmt.Errorf("failed to update deposit: %w", err)
		}
	}

	// Step 3: Advance state machine based on confirmation thresholds
	// Check if we can advance from 'seen' to 'confirmed'
	var newState string
	if currentState == "seen" {
		if confirmations >= minConf || (zeroConfMax > 0 && amountKoinu <= zeroConfMax) {
			newState = "confirmed"
		}
	} else if currentState == "confirmed" {
		newState = "confirmed"
	} else if currentState == "credited" || currentState == "orphaned" {
		// Already terminal; don't modify
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit transaction: %w", err)
		}
		return nil
	}

	// Update to new state if needed
	if newState != "" && newState != currentState {
		_, err := tx.ExecContext(ctx,
			"UPDATE deposits SET state = ? WHERE id = ?",
			newState, depositID,
		)
		if err != nil {
			return fmt.Errorf("failed to update deposit state to %s: %w", newState, err)
		}
		currentState = newState
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	// The credit hook (p.credit) opens its own transaction against the same
	// *sql.DB, which has exactly one pooled connection (see store.Open) —
	// SQLite serializes writes anyway. Calling it while the transaction
	// above was still open meant it could never acquire a connection,
	// deadlocking forever (the outer tx held the only connection and
	// wouldn't commit until credit() returned). So: commit the
	// seen->confirmed transition first, then run credit() unlocked, then a
	// second short transaction marks 'credited'.
	//
	// currentState == "confirmed" here on every event for a deposit that's
	// already confirmed (not just the seen->confirmed transition), since a
	// deposit can receive further confirmation events while still awaiting
	// crediting. Without a claim, each of those would re-run the credit
	// hook. claimForCrediting atomically claims the deposit first, so only
	// one caller ever gets to run the hook for it.
	if currentState == "confirmed" {
		claimed, err := p.claimForCrediting(ctx, depositID, now)
		if err != nil {
			return fmt.Errorf("failed to claim deposit for crediting: %w", err)
		}
		if !claimed {
			// Another in-flight call already owns crediting this deposit.
			return nil
		}

		if err := p.credit(ctx, depositID); err != nil {
			// Release the claim so a later event can retry crediting.
			if _, relErr := p.store.DB().ExecContext(ctx,
				"UPDATE deposits SET crediting_claimed_at = NULL WHERE id = ?",
				depositID,
			); relErr != nil {
				return fmt.Errorf("credit hook failed: %w (also failed to release claim: %v)", err, relErr)
			}
			return fmt.Errorf("credit hook failed: %w", err)
		}

		if _, err := p.store.DB().ExecContext(ctx,
			"UPDATE deposits SET state = 'credited', credited_at = ? WHERE id = ?",
			now, depositID,
		); err != nil {
			return fmt.Errorf("failed to update deposit to credited: %w", err)
		}
	}

	return nil
}

// creditClaimStaleAfter bounds how long a crediting claim is honored before
// it's considered abandoned (the process that took it crashed mid-credit)
// and can be reclaimed by a later event, so a crash doesn't permanently
// strand a deposit at 'confirmed'.
const creditClaimStaleAfter = 2 * time.Minute

// claimForCrediting atomically claims depositID for crediting, so only one
// caller ever runs the credit hook for a given deposit even if multiple
// confirmation events arrive concurrently or are redelivered. Returns
// (true, nil) if the claim was acquired, (false, nil) if another live claim
// already owns it.
func (p *DepositPipeline) claimForCrediting(ctx context.Context, depositID int64, now string) (bool, error) {
	staleBefore := time.Now().UTC().Add(-creditClaimStaleAfter).Format(time.RFC3339)
	result, err := p.store.DB().ExecContext(ctx, `
		UPDATE deposits SET crediting_claimed_at = ?
		WHERE id = ? AND state = 'confirmed'
		AND (crediting_claimed_at IS NULL OR crediting_claimed_at < ?)
	`,
		now, depositID, staleBefore,
	)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// insertReorgAlert inserts a deposit_reorged_after_credit alert with dedup.
// Only inserts if no unacked alert for this deposit already exists.
func (p *DepositPipeline) insertReorgAlert(ctx context.Context, depositID int64, txid string) error {
	// Check for existing unacked alert for this deposit
	var existingCount int
	err := p.store.DB().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM alerts
		WHERE kind = 'deposit_reorged_after_credit'
		AND message LIKE ?
		AND acked_at IS NULL
	`,
		fmt.Sprintf("%%deposit_id:%d,%%", depositID),
	).Scan(&existingCount)
	if err != nil {
		return fmt.Errorf("failed to check for existing alert: %w", err)
	}

	if existingCount > 0 {
		// Alert already exists; don't spam
		return nil
	}

	// Insert new alert
	now := time.Now().UTC().Format(time.RFC3339)
	message := fmt.Sprintf("Deposit reorged after credit (deposit_id:%d, txid:%s)", depositID, txid)
	_, err = p.store.DB().ExecContext(ctx,
		"INSERT INTO alerts (kind, message, created_at) VALUES (?, ?, ?)",
		"deposit_reorged_after_credit", message, now,
	)
	if err != nil {
		return fmt.Errorf("failed to insert alert: %w", err)
	}

	return nil
}
