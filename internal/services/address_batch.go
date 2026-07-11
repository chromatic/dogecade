package services

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/chromatic/dogecade/internal/keyring"
	"github.com/chromatic/dogecade/internal/store"
)

// NodeImporter abstracts the node registration interface for testability.
// It represents the minimal interface needed to register watch-only addresses.
type NodeImporter interface {
	ImportAddress(ctx context.Context, addr, label string, rescan bool) error
}

// AddressBatchService handles batch import of Dogecoin addresses.
type AddressBatchService struct {
	store *store.Store
}

// NewAddressBatchService creates a new AddressBatchService wrapping the given Store.
func NewAddressBatchService(s *store.Store) *AddressBatchService {
	return &AddressBatchService{store: s}
}

// ImportBatch validates and imports a batch of addresses with all-or-nothing
// semantics. If any address is invalid or a duplicate is detected, the entire
// batch is rejected and no rows are inserted (transaction is rolled back).
//
// purpose tags every address in the batch for its intended use ("token_deposit"
// or "machine_direct" — see the addresses table's CHECK constraint); an
// operator loads separate batches for the customer token-purchase pool and
// the machine-direct-pay pool.
//
// After the database transaction commits, if node is not nil, ImportBatch
// attempts to register each address as watch-only with the node via
// ImportAddress. If registration succeeds, node_registered_at is set to the
// current timestamp; if it fails, node_registered_at is left NULL (pending
// import) and import continues without aborting the batch.
//
// Returns the batch ID on success, or an error with zero rows inserted on failure.
func (svc *AddressBatchService) ImportBatch(
	ctx context.Context,
	sourceNote string,
	addrs []string,
	node NodeImporter,
	purpose string,
) (int64, error) {
	if len(addrs) == 0 {
		return 0, fmt.Errorf("batch cannot be empty")
	}
	if purpose != "token_deposit" && purpose != "machine_direct" {
		return 0, fmt.Errorf("invalid purpose %q", purpose)
	}

	// Phase 1: Validate all addresses (offline validation via Base58Check)
	validAddrs := make(map[string]bool)
	for _, addr := range addrs {
		valid, err := keyring.ValidateAddress(addr)
		if err != nil {
			return 0, fmt.Errorf("error validating address %q: %w", addr, err)
		}
		if !valid {
			return 0, fmt.Errorf("invalid address: %q", addr)
		}

		// Check for duplicates within the batch
		if validAddrs[addr] {
			return 0, fmt.Errorf("duplicate address in batch: %q", addr)
		}
		validAddrs[addr] = true
	}

	// Phase 2: Check for existing addresses in the database
	for addr := range validAddrs {
		var exists bool
		err := svc.store.DB().QueryRowContext(ctx,
			"SELECT COUNT(*) > 0 FROM addresses WHERE address = ?",
			addr,
		).Scan(&exists)
		if err != nil {
			return 0, fmt.Errorf("error checking for existing address: %w", err)
		}
		if exists {
			return 0, fmt.Errorf("address already imported: %q", addr)
		}
	}

	// Phase 3: Insert batch and addresses in a transaction (all-or-nothing)
	tx, err := svc.store.DB().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback() // Rollback is a no-op if already committed

	// Insert batch row
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := tx.ExecContext(ctx,
		"INSERT INTO address_batches (source_note, address_count, loaded_at) VALUES (?, ?, ?)",
		sourceNote, len(addrs), now,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to insert batch: %w", err)
	}

	batchID, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get batch ID: %w", err)
	}

	// Insert address rows
	for addr := range validAddrs {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO addresses (address, batch_id, state, purpose)
			 VALUES (?, ?, 'pool', ?)`,
			addr, batchID, purpose,
		)
		if err != nil {
			return 0, fmt.Errorf("failed to insert address %q: %w", addr, err)
		}
	}

	// Commit transaction
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Phase 4: Register addresses with node (if configured), post-commit
	// Node registration failures do not roll back the batch; they leave
	// node_registered_at NULL for later reconciliation.
	if node != nil {
		for addr := range validAddrs {
			// Use the address itself as the label for now
			err := node.ImportAddress(ctx, addr, addr, false)
			if err != nil {
				// Log and continue (don't fail the batch)
				// In a real scenario, this would be logged appropriately
				continue
			}

			// Registration succeeded; update node_registered_at
			now := time.Now().UTC().Format(time.RFC3339)
			_, err = svc.store.DB().ExecContext(ctx,
				"UPDATE addresses SET node_registered_at = ? WHERE address = ? AND batch_id = ?",
				now, addr, batchID,
			)
			if err != nil {
				// Log but don't fail the batch
				// The address is already in the pool; the registration timestamp
				// is a side-effect we can retry or ignore
				continue
			}
		}
	}

	return batchID, nil
}
