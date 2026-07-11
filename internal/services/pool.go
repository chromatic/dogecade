package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/chromatic/dogecade/internal/store"
)

var (
	// ErrPoolEmpty is returned when an Assign is attempted on an empty pool.
	ErrPoolEmpty = errors.New("pool is empty")
)

// PoolService manages the address pool: assignment, release, retirement,
// and low-water monitoring.
type PoolService struct {
	store    *store.Store
	settings *SettingsService
}

// NewPoolService creates a new PoolService wrapping the given Store and SettingsService.
func NewPoolService(s *store.Store, settings *SettingsService) *PoolService {
	return &PoolService{store: s, settings: settings}
}

// Assign atomically claims the oldest (lowest id) address from the pool
// (state='pool'), transitions it to state='assigned', sets assigned_at to now,
// and returns the address and its ID. If the pool is empty, returns ErrPoolEmpty.
// Assign is safe under concurrent callers (SQLite serializes writes; a race
// is detected by checking rows affected).
func (svc *PoolService) Assign(ctx context.Context, purpose string) (address string, addressID int64, err error) {
	// Use a transaction to atomically select and update the lowest-id pool row.
	tx, err := svc.store.DB().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return "", 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback() // No-op if already committed

	// Select the lowest-id pool row for this purpose
	var id int64
	err = tx.QueryRowContext(ctx,
		"SELECT id, address FROM addresses WHERE state = 'pool' AND purpose = ? ORDER BY id ASC LIMIT 1",
		purpose,
	).Scan(&id, &address)
	if err == sql.ErrNoRows {
		return "", 0, ErrPoolEmpty
	}
	if err != nil {
		return "", 0, fmt.Errorf("failed to select pool address: %w", err)
	}

	// Update the row to assigned
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := tx.ExecContext(ctx,
		"UPDATE addresses SET state = 'assigned', assigned_at = ? WHERE id = ? AND state = 'pool'",
		now, id,
	)
	if err != nil {
		return "", 0, fmt.Errorf("failed to update address: %w", err)
	}

	// Check rows affected to detect race condition
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return "", 0, fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rowsAffected == 0 {
		// Another caller claimed this row; retry from the top
		// In practice, this is extremely rare with SQLite's serialization
		return "", 0, ErrPoolEmpty
	}

	// Commit the transaction
	if err := tx.Commit(); err != nil {
		return "", 0, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return address, id, nil
}

// Release sets a previously-assigned address back to state='pool',
// clearing assigned_at, user_id, and machine_id. This enables reuse
// if the assignment is abandoned (e.g., tap twice during purchase).
func (svc *PoolService) Release(ctx context.Context, addressID int64) error {
	result, err := svc.store.DB().ExecContext(ctx,
		"UPDATE addresses SET state = 'pool', assigned_at = NULL, user_id = NULL, machine_id = NULL WHERE id = ?",
		addressID,
	)
	if err != nil {
		return fmt.Errorf("failed to release address %d: %w", addressID, err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("no address found with id %d", addressID)
	}

	return nil
}

// Retire sets an address to state='retired' and sets retired_at to now.
func (svc *PoolService) Retire(ctx context.Context, addressID int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := svc.store.DB().ExecContext(ctx,
		"UPDATE addresses SET state = 'retired', retired_at = ? WHERE id = ?",
		now, addressID,
	)
	if err != nil {
		return fmt.Errorf("failed to retire address %d: %w", addressID, err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("no address found with id %d", addressID)
	}

	return nil
}

// CountsByState returns a map of state -> count for all addresses.
func (svc *PoolService) CountsByState(ctx context.Context) (map[string]int, error) {
	rows, err := svc.store.DB().QueryContext(ctx,
		"SELECT state, COUNT(*) FROM addresses GROUP BY state",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query address counts: %w", err)
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			return nil, fmt.Errorf("failed to scan state count: %w", err)
		}
		counts[state] = count
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate state counts: %w", err)
	}

	return counts, nil
}

// AddressIDByAddress looks up the addresses.id for a given address string.
// Returns 0 if not found (no error), allowing callers to handle missing addresses.
func (svc *PoolService) AddressIDByAddress(ctx context.Context, addr string) (int64, error) {
	var id int64
	err := svc.store.DB().QueryRowContext(ctx,
		"SELECT id FROM addresses WHERE address = ?",
		addr,
	).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("failed to lookup address %q: %w", addr, err)
	}
	return id, nil
}

// Address is a projection of an addresses row for the admin pool browser.
type Address struct {
	ID         int64
	Address    string
	BatchID    int64
	State      string
	Purpose    string
	AssignedAt string
	RetiredAt  string
}

// ListByState returns addresses in the given state, most recently inserted
// first, up to limit rows. Used by the admin pool browser.
func (svc *PoolService) ListByState(ctx context.Context, state string, limit int) ([]Address, error) {
	rows, err := svc.store.DB().QueryContext(ctx, `
		SELECT id, address, COALESCE(batch_id, 0), state, purpose,
		       COALESCE(assigned_at, ''), COALESCE(retired_at, '')
		FROM addresses WHERE state = ? ORDER BY id DESC LIMIT ?`,
		state, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list addresses in state %q: %w", state, err)
	}
	defer rows.Close()

	var addrs []Address
	for rows.Next() {
		var a Address
		if err := rows.Scan(&a.ID, &a.Address, &a.BatchID, &a.State, &a.Purpose, &a.AssignedAt, &a.RetiredAt); err != nil {
			return nil, fmt.Errorf("failed to scan address: %w", err)
		}
		addrs = append(addrs, a)
	}
	return addrs, rows.Err()
}

// AddressBatch is a projection of an address_batches row for the admin
// batch audit view.
type AddressBatch struct {
	ID           int64
	SourceNote   string
	AddressCount int
	LoadedAt     string
}

// ListBatches returns all address batches, most recent first.
func (svc *PoolService) ListBatches(ctx context.Context) ([]AddressBatch, error) {
	rows, err := svc.store.DB().QueryContext(ctx,
		"SELECT id, COALESCE(source_note, ''), address_count, loaded_at FROM address_batches ORDER BY id DESC",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list address batches: %w", err)
	}
	defer rows.Close()

	var batches []AddressBatch
	for rows.Next() {
		var b AddressBatch
		if err := rows.Scan(&b.ID, &b.SourceNote, &b.AddressCount, &b.LoadedAt); err != nil {
			return nil, fmt.Errorf("failed to scan address batch: %w", err)
		}
		batches = append(batches, b)
	}
	return batches, rows.Err()
}

// CheckLowWater reads the pool count (state='pool') and compares it against
// pool_warn_threshold and pool_urgent_threshold settings. If the pool is below
// either threshold, an alert is inserted (kind="pool_low_warn" or "pool_low_urgent").
// To avoid spam, CheckLowWater only inserts an alert if an unacked alert of the
// same kind does not already exist.
func (svc *PoolService) CheckLowWater(ctx context.Context) error {
	// Get thresholds
	warnThreshold, err := svc.settings.GetPoolWarnThreshold(ctx)
	if err != nil {
		return fmt.Errorf("failed to get warn threshold: %w", err)
	}

	urgentThreshold, err := svc.settings.GetPoolUrgentThreshold(ctx)
	if err != nil {
		return fmt.Errorf("failed to get urgent threshold: %w", err)
	}

	// Count pool addresses
	var poolCount int
	err = svc.store.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM addresses WHERE state = 'pool'",
	).Scan(&poolCount)
	if err != nil {
		return fmt.Errorf("failed to count pool addresses: %w", err)
	}

	// Determine if an alert is needed
	var alertKind string
	var message string
	if poolCount < urgentThreshold {
		alertKind = "pool_low_urgent"
		message = fmt.Sprintf("Pool count is %d (below threshold %d)", poolCount, urgentThreshold)
	} else if poolCount < warnThreshold {
		alertKind = "pool_low_warn"
		message = fmt.Sprintf("Pool count is %d (below threshold %d)", poolCount, warnThreshold)
	} else {
		// Above both thresholds; no alert
		return nil
	}

	// Use the shared alert dedup helper
	return InsertAlertIfNotExists(ctx, svc.store.DB(), alertKind, message)
}
