package services

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromatic/dogecade/internal/store"
)

// seedCounter guarantees unique addresses/txids across seed helper calls
// within a test binary run.
var seedCounter atomic.Int64

// newTestStore creates a temporary SQLite database for testing.
// It automatically runs migrations and cleans up via t.Cleanup.
func newTestStore(t *testing.T) *store.Store {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// seedPoolAddress adds a single test address to the pool in a new batch.
// Returns the address ID for further use.
func seedPoolAddress(t *testing.T, ctx context.Context, s *store.Store, addr string, purpose string) int64 {
	// Create a batch first
	var batchID int64
	now := time.Now().UTC().Format(time.RFC3339)
	err := s.DB().QueryRowContext(ctx,
		"INSERT INTO address_batches (source_note, address_count, loaded_at) VALUES (?, ?, ?) RETURNING id",
		"test batch", 1, now,
	).Scan(&batchID)
	if err != nil {
		t.Fatalf("failed to insert batch: %v", err)
	}

	// Insert address in pool state
	var addressID int64
	err = s.DB().QueryRowContext(ctx,
		"INSERT INTO addresses (address, batch_id, state, purpose) VALUES (?, ?, ?, ?) RETURNING id",
		addr, batchID, "pool", purpose,
	).Scan(&addressID)
	if err != nil {
		t.Fatalf("failed to insert address: %v", err)
	}

	return addressID
}

// seedPoolAddresses inserts multiple test addresses directly into the database
// for testing pool operations. Returns a batch ID for reference.
func seedPoolAddresses(t *testing.T, ctx context.Context, s *store.Store, count int) int64 {
	// Create a batch
	var batchID int64
	now := time.Now().UTC().Format(time.RFC3339)
	err := s.DB().QueryRowContext(ctx,
		"INSERT INTO address_batches (source_note, address_count, loaded_at) VALUES (?, ?, ?) RETURNING id",
		"test batch", count, now,
	).Scan(&batchID)
	if err != nil {
		t.Fatalf("failed to insert batch: %v", err)
	}

	// Insert addresses
	for i := 0; i < count; i++ {
		addr := "DPoolTestAddr" + string(rune(65+i)) // A, B, C, ...
		_, err := s.DB().ExecContext(
			ctx,
			`INSERT INTO addresses (address, batch_id, state, purpose) VALUES (?, ?, 'pool', 'token_deposit')`,
			addr, batchID,
		)
		if err != nil {
			t.Fatalf("failed to seed address %d: %v", i, err)
		}
	}

	return batchID
}

// seedUser inserts a test user directly into the database and returns its ID.
func seedUser(t *testing.T, ctx context.Context, s *store.Store, subjectHash string) int64 {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	var id int64
	err := s.DB().QueryRowContext(ctx,
		"INSERT INTO users (subject_hash, created_at) VALUES (?, ?) RETURNING id",
		subjectHash, now,
	).Scan(&id)
	if err != nil {
		t.Fatalf("failed to seed user: %v", err)
	}
	return id
}

// seedMachine inserts a test machine directly into the database and returns
// its ID. isActive controls whether the machine can be redeemed against.
func seedMachine(t *testing.T, ctx context.Context, s *store.Store, slug string, isActive bool) int64 {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	activeInt := 0
	if isActive {
		activeInt = 1
	}
	var id int64
	err := s.DB().QueryRowContext(ctx,
		"INSERT INTO machines (slug, name, is_active, created_at) VALUES (?, ?, ?, ?) RETURNING id",
		slug, slug, activeInt, now,
	).Scan(&id)
	if err != nil {
		t.Fatalf("failed to seed machine: %v", err)
	}
	return id
}

// seedDeposit creates a token_deposit address assigned to userID and a
// 'seen'-state deposit against it, returning the new deposit's ID. Useful
// for tests that need a deposit_id to reference from the ledger without
// exercising the full deposit pipeline state machine.
func seedDeposit(t *testing.T, ctx context.Context, s *store.Store, userID int64) int64 {
	t.Helper()
	n := seedCounter.Add(1)
	addr := fmt.Sprintf("DSeedDepositAddr%d", n)
	addressID := seedPoolAddress(t, ctx, s, addr, "token_deposit")

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := s.DB().ExecContext(ctx,
		"UPDATE addresses SET state = 'assigned', assigned_at = ?, user_id = ? WHERE id = ?",
		now, userID, addressID,
	); err != nil {
		t.Fatalf("failed to assign seeded address to user: %v", err)
	}

	var depositID int64
	err := s.DB().QueryRowContext(ctx,
		`INSERT INTO deposits (address_id, txid, vout, amount_koinu, confirmations, state, created_at)
		 VALUES (?, ?, 0, 100000000, 1, 'seen', ?) RETURNING id`,
		addressID, fmt.Sprintf("txid-seed-%d", n), now,
	).Scan(&depositID)
	if err != nil {
		t.Fatalf("failed to seed deposit: %v", err)
	}
	return depositID
}
