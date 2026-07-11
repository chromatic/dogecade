package services

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/chromatic/dogecade/internal/store"
)

// PurchaseService assigns pool addresses to users for token purchases.
type PurchaseService struct {
	store *store.Store
}

// NewPurchaseService creates a new PurchaseService wrapping the given Store.
func NewPurchaseService(s *store.Store) *PurchaseService {
	return &PurchaseService{store: s}
}

// StartPurchase returns a token_deposit address for the given user to pay
// into. If the user already has an address assigned to them that hasn't
// been paid yet, that same address is returned (so tapping "buy" twice
// doesn't burn a second address). Otherwise it atomically claims the oldest
// pool address and binds it to the user. Returns ErrPoolEmpty if the pool
// has no addresses available.
//
// This claim-and-bind is done directly here rather than via
// PoolService.Assign because binding the user_id must happen in the same
// transaction as the claim: PoolService.Assign only knows about claiming,
// not about users.
func (svc *PurchaseService) StartPurchase(ctx context.Context, userID int64) (address string, err error) {
	tx, err := svc.store.DB().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return "", fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Reuse an existing assigned-but-unpaid address for this user, if any.
	err = tx.QueryRowContext(ctx,
		"SELECT address FROM addresses WHERE user_id = ? AND purpose = 'token_deposit' AND state = 'assigned' LIMIT 1",
		userID,
	).Scan(&address)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return "", fmt.Errorf("failed to commit transaction: %w", err)
		}
		return address, nil
	}
	if err != sql.ErrNoRows {
		return "", fmt.Errorf("failed to look up existing assigned address: %w", err)
	}

	// Claim the oldest pool address and bind it to the user.
	var id int64
	err = tx.QueryRowContext(ctx,
		"SELECT id, address FROM addresses WHERE state = 'pool' AND purpose = 'token_deposit' ORDER BY id ASC LIMIT 1",
	).Scan(&id, &address)
	if err == sql.ErrNoRows {
		return "", ErrPoolEmpty
	}
	if err != nil {
		return "", fmt.Errorf("failed to select pool address: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	result, err := tx.ExecContext(ctx,
		"UPDATE addresses SET state = 'assigned', assigned_at = ?, user_id = ? WHERE id = ? AND state = 'pool'",
		now, userID, id,
	)
	if err != nil {
		return "", fmt.Errorf("failed to assign address to user: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return "", fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rowsAffected == 0 {
		// Another caller claimed this row first; the caller may retry.
		return "", ErrPoolEmpty
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("failed to commit transaction: %w", err)
	}
	return address, nil
}

// NewPurchaseCreditHook returns a CreditHook (see deposit_pipeline.go) that
// credits the depositing address's bound user with
// amount_koinu / token_price_koinu whole tokens (floor division), recording
// the koinu remainder on the deposit row for auditability.
//
// Crediting is keyed off the deposit's address's user_id, not its current
// state — this also implements the "late payment" rule: a payment landing
// on an address after it's been retired (but not released back to the pool,
// which is the only operation that clears user_id) still credits the user
// it was originally assigned to.
//
// If the address has no bound user (e.g. a machine_direct address, or one
// that was released back to the pool before payment), the deposit is left
// uncredited; there's no user to credit.
func NewPurchaseCreditHook(s *store.Store, settings *SettingsService, ledger *LedgerService) CreditHook {
	return func(ctx context.Context, depositID int64) error {
		var amountKoinu, addressID int64
		err := s.DB().QueryRowContext(ctx,
			"SELECT amount_koinu, address_id FROM deposits WHERE id = ?",
			depositID,
		).Scan(&amountKoinu, &addressID)
		if err != nil {
			return fmt.Errorf("failed to look up deposit %d: %w", depositID, err)
		}

		var userID sql.NullInt64
		err = s.DB().QueryRowContext(ctx,
			"SELECT user_id FROM addresses WHERE id = ?",
			addressID,
		).Scan(&userID)
		if err != nil {
			return fmt.Errorf("failed to look up address %d for deposit %d: %w", addressID, depositID, err)
		}

		priceKoinu, err := settings.GetTokenPriceKoinu(ctx)
		if err != nil {
			return err
		}
		if priceKoinu <= 0 {
			return fmt.Errorf("invalid token_price_koinu setting: %d", priceKoinu)
		}

		tokens := amountKoinu / priceKoinu
		remainder := amountKoinu % priceKoinu

		tx, err := s.DB().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return fmt.Errorf("failed to begin transaction: %w", err)
		}
		defer tx.Rollback()

		if _, err := tx.ExecContext(ctx,
			"UPDATE deposits SET remainder_koinu = ? WHERE id = ?",
			remainder, depositID,
		); err != nil {
			return fmt.Errorf("failed to record remainder for deposit %d: %w", depositID, err)
		}

		if userID.Valid && tokens > 0 {
			if err := ledger.creditPurchaseTx(ctx, tx, userID.Int64, depositID, tokens); err != nil {
				return err
			}
		}

		return tx.Commit()
	}
}
