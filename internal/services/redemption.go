package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/chromatic/dogecade/internal/store"
)

// ErrMachineNotFound is returned when redeeming against an unknown machine ID.
var ErrMachineNotFound = errors.New("machine not found")

// ErrMachineNotActive is returned when redeeming against a machine that
// exists but is marked inactive.
var ErrMachineNotActive = errors.New("machine is not active")

// RedemptionService debits a user's token balance to queue a credit pulse
// for a machine.
type RedemptionService struct {
	store  *store.Store
	ledger *LedgerService
}

// NewRedemptionService creates a new RedemptionService wrapping the given
// Store and LedgerService.
func NewRedemptionService(s *store.Store, ledger *LedgerService) *RedemptionService {
	return &RedemptionService{store: s, ledger: ledger}
}

// Redeem debits one token from userID and inserts a pending credit_pulses
// row for machineID, atomically: either both happen or neither does. The
// machine must exist and be active (relay-binding is checked again at
// dispatch time in Phase 5, once relay_boards/machine_relays exist).
//
// Returns the new credit_pulses row's ID on success.
func (svc *RedemptionService) Redeem(ctx context.Context, userID, machineID int64) (pulseID int64, err error) {
	tx, err := svc.store.DB().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var isActive bool
	err = tx.QueryRowContext(ctx,
		"SELECT is_active FROM machines WHERE id = ?",
		machineID,
	).Scan(&isActive)
	if err == sql.ErrNoRows {
		return 0, ErrMachineNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("failed to look up machine %d: %w", machineID, err)
	}
	if !isActive {
		return 0, ErrMachineNotActive
	}

	now := time.Now().UTC().Format(time.RFC3339)
	result, err := tx.ExecContext(ctx,
		"INSERT INTO credit_pulses (machine_id, user_id, source, state, attempts, created_at) VALUES (?, ?, 'token_redemption', 'pending', 0, ?)",
		machineID, userID, now,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to insert credit pulse: %w", err)
	}
	pulseID, err = result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get pulse ID: %w", err)
	}

	if err := svc.ledger.debitRedemptionTx(ctx, tx, userID, pulseID); err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit transaction: %w", err)
	}
	return pulseID, nil
}
