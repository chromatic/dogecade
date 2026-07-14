package services

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/chromatic/dogecade/internal/store"
)

// directPayPurpose is the addresses.purpose value used for machine-direct
// addresses (see design.md's "Direct pay-to-machine" section).
const directPayPurpose = "machine_direct"

// DirectPayAddress is a projection of a machine's active direct-pay address
// for display purposes.
type DirectPayAddress struct {
	ID         int64
	Address    string
	UseCount   int
	AssignedAt string
}

// DirectPayService manages the "pay the machine directly" purchase path
// (design.md's secondary flow): each direct-pay-enabled machine always has
// at most one active address (enforced by idx_addresses_one_active_per_machine),
// drawn from the same address pool as customer token purchases but tagged
// with purpose='machine_direct' at import time.
type DirectPayService struct {
	store    *store.Store
	settings *SettingsService
}

// NewDirectPayService creates a new DirectPayService wrapping the given
// Store and SettingsService.
func NewDirectPayService(s *store.Store, settings *SettingsService) *DirectPayService {
	return &DirectPayService{store: s, settings: settings}
}

// ActiveAddress returns the machine's current active direct-pay address, if
// any. The bool return is false (with a zero DirectPayAddress) if the
// machine has no active address yet.
func (svc *DirectPayService) ActiveAddress(ctx context.Context, machineID int64) (DirectPayAddress, bool, error) {
	var a DirectPayAddress
	err := svc.store.DB().QueryRowContext(ctx,
		`SELECT id, address, use_count, COALESCE(assigned_at, '') FROM addresses
		 WHERE machine_id = ? AND purpose = ? AND state = 'assigned'`,
		machineID, directPayPurpose,
	).Scan(&a.ID, &a.Address, &a.UseCount, &a.AssignedAt)
	if err == sql.ErrNoRows {
		return DirectPayAddress{}, false, nil
	}
	if err != nil {
		return DirectPayAddress{}, false, fmt.Errorf("failed to look up active direct-pay address for machine %d: %w", machineID, err)
	}
	return a, true, nil
}

// Activate claims the oldest machine_direct pool address and binds it to
// machineID as its active address. Returns ErrPoolEmpty if no machine_direct
// addresses are available. Callers should check ActiveAddress first;
// Activate does not replace an existing active address (the DB's partial
// unique index would reject a second one anyway).
func (svc *DirectPayService) Activate(ctx context.Context, machineID int64) (string, error) {
	tx, err := svc.store.DB().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return "", fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	address, err := claimPoolAddressForMachine(ctx, tx, machineID)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("failed to commit transaction: %w", err)
	}
	return address, nil
}

// claimPoolAddressForMachine claims the oldest machine_direct pool address
// and assigns it to machineID within tx. Returns ErrPoolEmpty if none are
// available.
func claimPoolAddressForMachine(ctx context.Context, tx *sql.Tx, machineID int64) (string, error) {
	var id int64
	var address string
	err := tx.QueryRowContext(ctx,
		"SELECT id, address FROM addresses WHERE state = 'pool' AND purpose = ? ORDER BY id ASC LIMIT 1",
		directPayPurpose,
	).Scan(&id, &address)
	if err == sql.ErrNoRows {
		return "", ErrPoolEmpty
	}
	if err != nil {
		return "", fmt.Errorf("failed to select pool address: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	result, err := tx.ExecContext(ctx,
		"UPDATE addresses SET state = 'assigned', assigned_at = ?, machine_id = ?, use_count = 0 WHERE id = ? AND state = 'pool'",
		now, machineID, id,
	)
	if err != nil {
		return "", fmt.Errorf("failed to assign address to machine %d: %w", machineID, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return "", fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rows == 0 {
		return "", ErrPoolEmpty
	}
	return address, nil
}

// Rotate retires a machine's currently active direct-pay address (if any)
// and activates a fresh one from the pool (8.3). If the pool has no
// machine_direct addresses available, the current active address is left
// untouched (never leaving the machine addressless) and an alert is raised;
// Rotate returns ErrPoolEmpty in that case.
func (svc *DirectPayService) Rotate(ctx context.Context, machineID int64) (string, error) {
	tx, err := svc.store.DB().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return "", fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Check for a replacement before touching the currently active address,
	// so a rotation attempt during an empty pool can never leave the
	// machine without an address to show customers.
	var newID int64
	var newAddress string
	err = tx.QueryRowContext(ctx,
		"SELECT id, address FROM addresses WHERE state = 'pool' AND purpose = ? ORDER BY id ASC LIMIT 1",
		directPayPurpose,
	).Scan(&newID, &newAddress)
	if err == sql.ErrNoRows {
		// Nothing to write in this transaction (the pool is empty); release
		// it before touching the DB again through InsertAlertIfNotExists, or
		// its own connection acquisition would deadlock against this
		// still-open transaction on SQLite's single-writer connection.
		if rbErr := tx.Rollback(); rbErr != nil && rbErr != sql.ErrTxDone {
			return "", fmt.Errorf("failed to release transaction: %w", rbErr)
		}
		if alertErr := InsertAlertIfNotExists(ctx, svc.store.DB(), "direct_pay_pool_empty",
			fmt.Sprintf("Machine %d could not rotate its direct-pay address: pool is empty", machineID)); alertErr != nil {
			return "", fmt.Errorf("failed to record pool-empty alert: %w", alertErr)
		}
		return "", ErrPoolEmpty
	}
	if err != nil {
		return "", fmt.Errorf("failed to select pool address: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.ExecContext(ctx,
		"UPDATE addresses SET state = 'retired', retired_at = ? WHERE machine_id = ? AND purpose = ? AND state = 'assigned'",
		now, machineID, directPayPurpose,
	); err != nil {
		return "", fmt.Errorf("failed to retire old direct-pay address for machine %d: %w", machineID, err)
	}

	result, err := tx.ExecContext(ctx,
		"UPDATE addresses SET state = 'assigned', assigned_at = ?, machine_id = ?, use_count = 0 WHERE id = ? AND state = 'pool'",
		now, machineID, newID,
	)
	if err != nil {
		return "", fmt.Errorf("failed to assign new address to machine %d: %w", machineID, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return "", fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rows == 0 {
		return "", ErrPoolEmpty
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("failed to commit transaction: %w", err)
	}
	return newAddress, nil
}

// CreditDeposit is called (via NewDirectPayAwareCreditHook) when a deposit
// lands on a machine_direct address. It converts the deposit amount into
// whole credits at the machine's direct_play_price_koinu (floor division,
// capped by the direct_pay_max_credits_per_tx setting), recording the koinu
// remainder for auditability the same way purchase crediting does, and
// queues that many pending 'direct_pay' credit_pulses rows for the
// dispatcher to fire — no user account is involved (design.md's anonymous
// "cool factor" path).
//
// If the machine's direct_play_price_koinu isn't configured (e.g. direct
// pay was disabled after this address was activated), the deposit is left
// with its remainder unset and no pulses are queued; there's no price to
// convert against.
func (svc *DirectPayService) CreditDeposit(ctx context.Context, depositID, addressID, machineID int64) error {
	var amountKoinu int64
	if err := svc.store.DB().QueryRowContext(ctx,
		"SELECT amount_koinu FROM deposits WHERE id = ?", depositID,
	).Scan(&amountKoinu); err != nil {
		return fmt.Errorf("failed to look up deposit %d: %w", depositID, err)
	}

	var priceKoinu sql.NullInt64
	if err := svc.store.DB().QueryRowContext(ctx,
		"SELECT direct_play_price_koinu FROM machines WHERE id = ?", machineID,
	).Scan(&priceKoinu); err != nil {
		return fmt.Errorf("failed to look up machine %d: %w", machineID, err)
	}
	if !priceKoinu.Valid || priceKoinu.Int64 <= 0 {
		return nil
	}

	credits := amountKoinu / priceKoinu.Int64
	remainder := amountKoinu % priceKoinu.Int64

	maxCredits, err := svc.settings.GetDirectPayMaxCreditsPerTx(ctx)
	if err != nil {
		return err
	}
	if maxCredits > 0 && credits > int64(maxCredits) {
		credits = int64(maxCredits)
	}

	tx, err := svc.store.DB().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		"UPDATE deposits SET remainder_koinu = ? WHERE id = ?", remainder, depositID,
	); err != nil {
		return fmt.Errorf("failed to record remainder for deposit %d: %w", depositID, err)
	}

	if credits > 0 {
		now := time.Now().UTC().Format(time.RFC3339)
		for i := int64(0); i < credits; i++ {
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO credit_pulses (machine_id, source, state, attempts, created_at) VALUES (?, 'direct_pay', 'pending', 0, ?)",
				machineID, now,
			); err != nil {
				return fmt.Errorf("failed to insert credit pulse for machine %d: %w", machineID, err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE addresses SET use_count = use_count + 1 WHERE id = ?", addressID,
		); err != nil {
			return fmt.Errorf("failed to increment use count for address %d: %w", addressID, err)
		}
	}

	return tx.Commit()
}

// NewDirectPayAwareCreditHook returns a CreditHook that routes each deposit
// to either the ordinary token-purchase crediting logic (NewPurchaseCreditHook)
// or DirectPayService.CreditDeposit, based on the depositing address's
// purpose — so DepositPipeline only needs a single CreditHook regardless of
// which purchase path the address belongs to.
func NewDirectPayAwareCreditHook(s *store.Store, settings *SettingsService, ledger *LedgerService, directPay *DirectPayService) CreditHook {
	purchaseHook := NewPurchaseCreditHook(s, settings, ledger)
	return func(ctx context.Context, depositID int64) error {
		var addressID int64
		if err := s.DB().QueryRowContext(ctx,
			"SELECT address_id FROM deposits WHERE id = ?", depositID,
		).Scan(&addressID); err != nil {
			return fmt.Errorf("failed to look up deposit %d: %w", depositID, err)
		}

		var purpose string
		var machineID sql.NullInt64
		if err := s.DB().QueryRowContext(ctx,
			"SELECT purpose, machine_id FROM addresses WHERE id = ?", addressID,
		).Scan(&purpose, &machineID); err != nil {
			return fmt.Errorf("failed to look up address %d: %w", addressID, err)
		}

		if purpose == directPayPurpose && machineID.Valid {
			return directPay.CreditDeposit(ctx, depositID, addressID, machineID.Int64)
		}
		return purchaseHook(ctx, depositID)
	}
}
