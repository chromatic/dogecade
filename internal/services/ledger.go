package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/chromatic/dogecade/internal/store"
)

// Ledger entry kinds. Every token_ledger row is exactly one of these.
const (
	LedgerKindPurchase    = "purchase"
	LedgerKindRedemption  = "redemption"
	LedgerKindRefund      = "refund"
	LedgerKindAdminAdjust = "admin_adjust"
)

// ErrInsufficientBalance is returned when a debit would take a user's
// token balance below zero.
var ErrInsufficientBalance = errors.New("insufficient token balance")

// LedgerService provides append-only access to the token_ledger table.
// A user's balance is always derived as SUM(delta); rows are never updated
// or deleted, so the ledger doubles as an audit trail.
type LedgerService struct {
	store *store.Store
}

// NewLedgerService creates a new LedgerService wrapping the given Store.
func NewLedgerService(s *store.Store) *LedgerService {
	return &LedgerService{store: s}
}

// Balance returns a user's current token balance (SUM of all ledger deltas).
func (svc *LedgerService) Balance(ctx context.Context, userID int64) (int64, error) {
	return svc.balanceTx(ctx, svc.store.DB(), userID)
}

// balanceTx computes a user's balance using the given queryer, so callers
// that need the balance and a subsequent write to be atomic (e.g. redemption)
// can pass a *sql.Tx.
func (svc *LedgerService) balanceTx(ctx context.Context, q queryer, userID int64) (int64, error) {
	var balance int64
	err := q.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(delta), 0) FROM token_ledger WHERE user_id = ?",
		userID,
	).Scan(&balance)
	if err != nil {
		return 0, fmt.Errorf("failed to compute balance for user %d: %w", userID, err)
	}
	return balance, nil
}

// queryer is the subset of *sql.DB / *sql.Tx used by ledger helpers, letting
// them run either standalone or as part of a caller-managed transaction.
type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// CreditPurchase records a positive ledger entry crediting tokens purchased
// via a deposit. tokens must be positive (callers should skip crediting
// entirely, rather than call this with tokens<=0, when a deposit doesn't
// cover even one token).
func (svc *LedgerService) CreditPurchase(ctx context.Context, userID, depositID, tokens int64) error {
	if tokens <= 0 {
		return fmt.Errorf("tokens must be positive, got %d", tokens)
	}
	tx, err := svc.store.DB().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := svc.creditPurchaseTx(ctx, tx, userID, depositID, tokens); err != nil {
		return err
	}
	return tx.Commit()
}

func (svc *LedgerService) creditPurchaseTx(ctx context.Context, tx *sql.Tx, userID, depositID, tokens int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := tx.ExecContext(ctx,
		"INSERT INTO token_ledger (user_id, delta, kind, deposit_id, created_at) VALUES (?, ?, ?, ?, ?)",
		userID, tokens, LedgerKindPurchase, depositID, now,
	)
	if err != nil {
		return fmt.Errorf("failed to insert purchase ledger entry: %w", err)
	}
	return nil
}

// DebitRedemption records a one-token debit for a redemption, referencing the
// given credit_pulses row. Returns ErrInsufficientBalance if the user's
// balance is below one token; this check-then-insert happens within a single
// transaction so concurrent redemptions of a 1-token balance can't both
// succeed.
func (svc *LedgerService) DebitRedemption(ctx context.Context, userID, pulseID int64) error {
	tx, err := svc.store.DB().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := svc.debitRedemptionTx(ctx, tx, userID, pulseID); err != nil {
		return err
	}
	return tx.Commit()
}

func (svc *LedgerService) debitRedemptionTx(ctx context.Context, tx *sql.Tx, userID, pulseID int64) error {
	balance, err := svc.balanceTx(ctx, tx, userID)
	if err != nil {
		return err
	}
	if balance < 1 {
		return ErrInsufficientBalance
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = tx.ExecContext(ctx,
		"INSERT INTO token_ledger (user_id, delta, kind, pulse_id, created_at) VALUES (?, -1, ?, ?, ?)",
		userID, LedgerKindRedemption, pulseID, now,
	)
	if err != nil {
		return fmt.Errorf("failed to insert redemption ledger entry: %w", err)
	}
	return nil
}

// Refund records a one-token credit reversing a failed redemption's debit,
// referencing the same credit_pulses row so the two entries can be
// correlated.
func (svc *LedgerService) Refund(ctx context.Context, userID, pulseID int64, note string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := svc.store.DB().ExecContext(ctx,
		"INSERT INTO token_ledger (user_id, delta, kind, pulse_id, note, created_at) VALUES (?, 1, ?, ?, ?, ?)",
		userID, LedgerKindRefund, pulseID, note, now,
	)
	if err != nil {
		return fmt.Errorf("failed to insert refund ledger entry: %w", err)
	}
	return nil
}

// LedgerEntry is a projection of a token_ledger row for display purposes.
type LedgerEntry struct {
	ID        int64
	Delta     int64
	Kind      string
	Note      string
	CreatedAt string
}

// History returns a user's ledger entries, most recent first, up to limit
// rows.
func (svc *LedgerService) History(ctx context.Context, userID int64, limit int) ([]LedgerEntry, error) {
	rows, err := svc.store.DB().QueryContext(ctx,
		"SELECT id, delta, kind, COALESCE(note, ''), created_at FROM token_ledger WHERE user_id = ? ORDER BY id DESC LIMIT ?",
		userID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query ledger history for user %d: %w", userID, err)
	}
	defer func() { _ = rows.Close() }()

	var entries []LedgerEntry
	for rows.Next() {
		var e LedgerEntry
		if err := rows.Scan(&e.ID, &e.Delta, &e.Kind, &e.Note, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan ledger entry: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// AdminAdjust records an arbitrary admin-initiated balance adjustment
// (positive or negative), with a required note for audit purposes.
func (svc *LedgerService) AdminAdjust(ctx context.Context, userID, delta int64, note string) error {
	if note == "" {
		return fmt.Errorf("note is required for admin adjustments")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := svc.store.DB().ExecContext(ctx,
		"INSERT INTO token_ledger (user_id, delta, kind, note, created_at) VALUES (?, ?, ?, ?, ?)",
		userID, delta, LedgerKindAdminAdjust, note, now,
	)
	if err != nil {
		return fmt.Errorf("failed to insert admin_adjust ledger entry: %w", err)
	}
	return nil
}
