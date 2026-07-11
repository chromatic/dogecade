package services

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestStartPurchaseClaimsPoolAddress(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	purchase := NewPurchaseService(s)
	userID := seedUser(t, ctx, s, "subject-1")
	seedPoolAddresses(t, ctx, s, 3)

	addr, err := purchase.StartPurchase(ctx, userID)
	if err != nil {
		t.Fatalf("StartPurchase failed: %v", err)
	}
	if addr == "" {
		t.Fatal("StartPurchase returned empty address")
	}

	var state string
	var gotUserID int64
	err = s.DB().QueryRowContext(ctx,
		"SELECT state, user_id FROM addresses WHERE address = ?",
		addr,
	).Scan(&state, &gotUserID)
	if err != nil {
		t.Fatalf("failed to query assigned address: %v", err)
	}
	if state != "assigned" {
		t.Errorf("expected state 'assigned', got %q", state)
	}
	if gotUserID != userID {
		t.Errorf("expected address bound to user %d, got %d", userID, gotUserID)
	}
}

func TestStartPurchaseReusesExistingAssignedAddress(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	purchase := NewPurchaseService(s)
	userID := seedUser(t, ctx, s, "subject-1")
	seedPoolAddresses(t, ctx, s, 3)

	addr1, err := purchase.StartPurchase(ctx, userID)
	if err != nil {
		t.Fatalf("first StartPurchase failed: %v", err)
	}
	addr2, err := purchase.StartPurchase(ctx, userID)
	if err != nil {
		t.Fatalf("second StartPurchase failed: %v", err)
	}
	if addr1 != addr2 {
		t.Errorf("expected tapping buy twice to reuse the same address: got %q then %q", addr1, addr2)
	}

	var assignedCount int
	err = s.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM addresses WHERE user_id = ? AND state = 'assigned'",
		userID,
	).Scan(&assignedCount)
	if err != nil {
		t.Fatalf("failed to count assigned addresses: %v", err)
	}
	if assignedCount != 1 {
		t.Errorf("expected exactly 1 assigned address for user after two StartPurchase calls, got %d", assignedCount)
	}
}

func TestStartPurchaseEmptyPoolReturnsError(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	purchase := NewPurchaseService(s)
	userID := seedUser(t, ctx, s, "subject-1")

	_, err := purchase.StartPurchase(ctx, userID)
	if !errors.Is(err, ErrPoolEmpty) {
		t.Errorf("expected ErrPoolEmpty, got %v", err)
	}
}

// TestStartPurchaseConcurrentDoubleTapNeverAssignsTwoAddresses exercises the
// "tap buy twice" race directly: many concurrent StartPurchase calls for the
// same user must settle on exactly one assigned address, never two, backed
// by the idx_addresses_one_assigned_per_user partial unique index.
func TestStartPurchaseConcurrentDoubleTapNeverAssignsTwoAddresses(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	purchase := NewPurchaseService(s)
	userID := seedUser(t, ctx, s, "subject-1")
	seedPoolAddresses(t, ctx, s, 5)

	const attempts = 8
	var wg sync.WaitGroup
	addrs := make([]string, attempts)
	errs := make([]error, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			addrs[i], errs[i] = purchase.StartPurchase(ctx, userID)
		}(i)
	}
	wg.Wait()

	seen := make(map[string]bool)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("StartPurchase call %d failed: %v", i, err)
		}
		seen[addrs[i]] = true
	}
	if len(seen) != 1 {
		t.Errorf("expected all concurrent StartPurchase calls to converge on 1 address, got %d distinct: %v", len(seen), seen)
	}

	var assignedCount int
	err := s.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM addresses WHERE user_id = ? AND state = 'assigned'",
		userID,
	).Scan(&assignedCount)
	if err != nil {
		t.Fatalf("failed to count assigned addresses: %v", err)
	}
	if assignedCount != 1 {
		t.Errorf("expected exactly 1 assigned address for user, got %d", assignedCount)
	}
}

func TestPurchaseCreditHookCreditsFlooredTokensAndRecordsRemainder(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	settings := NewSettingsService(s)
	ledger := NewLedgerService(s)
	userID := seedUser(t, ctx, s, "subject-1")

	if err := settings.SetTokenPriceKoinu(ctx, 30); err != nil {
		t.Fatalf("SetTokenPriceKoinu failed: %v", err)
	}

	addressID := seedPoolAddress(t, ctx, s, "DHookTestAddr1", "token_deposit")
	if _, err := s.DB().ExecContext(ctx,
		"UPDATE addresses SET state = 'assigned', user_id = ? WHERE id = ?",
		userID, addressID,
	); err != nil {
		t.Fatalf("failed to bind address to user: %v", err)
	}

	var depositID int64
	err := s.DB().QueryRowContext(ctx,
		`INSERT INTO deposits (address_id, txid, vout, amount_koinu, confirmations, state, created_at)
		 VALUES (?, 'txid-hook-1', 0, 100, 1, 'confirmed', '2026-01-01T00:00:00Z') RETURNING id`,
		addressID,
	).Scan(&depositID)
	if err != nil {
		t.Fatalf("failed to insert deposit: %v", err)
	}

	hook := NewPurchaseCreditHook(s, settings, ledger)
	if err := hook(ctx, depositID); err != nil {
		t.Fatalf("credit hook failed: %v", err)
	}

	balance, err := ledger.Balance(ctx, userID)
	if err != nil {
		t.Fatalf("Balance failed: %v", err)
	}
	if balance != 3 { // floor(100/30) = 3
		t.Errorf("expected balance 3, got %d", balance)
	}

	var remainder int64
	err = s.DB().QueryRowContext(ctx, "SELECT remainder_koinu FROM deposits WHERE id = ?", depositID).Scan(&remainder)
	if err != nil {
		t.Fatalf("failed to query remainder: %v", err)
	}
	if remainder != 10 { // 100 - 3*30 = 10
		t.Errorf("expected remainder_koinu 10, got %d", remainder)
	}
}

func TestPurchaseCreditHookSkipsCreditWhenUnderpaid(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	settings := NewSettingsService(s)
	ledger := NewLedgerService(s)
	userID := seedUser(t, ctx, s, "subject-1")

	if err := settings.SetTokenPriceKoinu(ctx, 1000); err != nil {
		t.Fatalf("SetTokenPriceKoinu failed: %v", err)
	}

	addressID := seedPoolAddress(t, ctx, s, "DHookTestAddr2", "token_deposit")
	if _, err := s.DB().ExecContext(ctx,
		"UPDATE addresses SET state = 'assigned', user_id = ? WHERE id = ?",
		userID, addressID,
	); err != nil {
		t.Fatalf("failed to bind address to user: %v", err)
	}

	var depositID int64
	err := s.DB().QueryRowContext(ctx,
		`INSERT INTO deposits (address_id, txid, vout, amount_koinu, confirmations, state, created_at)
		 VALUES (?, 'txid-hook-2', 0, 999, 1, 'confirmed', '2026-01-01T00:00:00Z') RETURNING id`,
		addressID,
	).Scan(&depositID)
	if err != nil {
		t.Fatalf("failed to insert deposit: %v", err)
	}

	hook := NewPurchaseCreditHook(s, settings, ledger)
	if err := hook(ctx, depositID); err != nil {
		t.Fatalf("credit hook failed: %v", err)
	}

	balance, err := ledger.Balance(ctx, userID)
	if err != nil {
		t.Fatalf("Balance failed: %v", err)
	}
	if balance != 0 {
		t.Errorf("expected balance 0 for an underpayment (999 < price 1000), got %d", balance)
	}

	var remainder int64
	if err := s.DB().QueryRowContext(ctx, "SELECT remainder_koinu FROM deposits WHERE id = ?", depositID).Scan(&remainder); err != nil {
		t.Fatalf("failed to query remainder: %v", err)
	}
	if remainder != 999 {
		t.Errorf("expected the whole underpayment recorded as remainder, got %d", remainder)
	}
}

// TestPurchaseCreditHookNoBoundUserIsNoop covers a deposit whose address has
// no user_id (e.g. a machine_direct address, or one released back to the
// pool): there's no one to credit, and the hook must not error.
func TestPurchaseCreditHookNoBoundUserIsNoop(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	settings := NewSettingsService(s)
	ledger := NewLedgerService(s)

	addressID := seedPoolAddress(t, ctx, s, "DHookTestAddr3", "token_deposit")

	var depositID int64
	err := s.DB().QueryRowContext(ctx,
		`INSERT INTO deposits (address_id, txid, vout, amount_koinu, confirmations, state, created_at)
		 VALUES (?, 'txid-hook-3', 0, 100, 1, 'confirmed', '2026-01-01T00:00:00Z') RETURNING id`,
		addressID,
	).Scan(&depositID)
	if err != nil {
		t.Fatalf("failed to insert deposit: %v", err)
	}

	hook := NewPurchaseCreditHook(s, settings, ledger)
	if err := hook(ctx, depositID); err != nil {
		t.Fatalf("expected hook to no-op cleanly for an unbound address, got error: %v", err)
	}
}

// TestPurchaseCreditHookLatePaymentCreditsRetiredAddressOwner is the 4.5
// late-payment rule: a payment landing on an address after it's been
// retired (but not released back to the pool, which is what clears
// user_id) still credits the user the address was originally assigned to.
func TestPurchaseCreditHookLatePaymentCreditsRetiredAddressOwner(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	settings := NewSettingsService(s)
	ledger := NewLedgerService(s)
	pool := NewPoolService(s, settings)
	userID := seedUser(t, ctx, s, "subject-1")

	if err := settings.SetTokenPriceKoinu(ctx, 100_000_000); err != nil {
		t.Fatalf("SetTokenPriceKoinu failed: %v", err)
	}

	addressID := seedPoolAddress(t, ctx, s, "DHookTestAddr4", "token_deposit")
	if _, err := s.DB().ExecContext(ctx,
		"UPDATE addresses SET state = 'assigned', user_id = ? WHERE id = ?",
		userID, addressID,
	); err != nil {
		t.Fatalf("failed to bind address to user: %v", err)
	}

	// Retire the address (e.g. rotation policy) without releasing it, so
	// user_id survives per Retire()'s documented behavior.
	if err := pool.Retire(ctx, addressID); err != nil {
		t.Fatalf("Retire failed: %v", err)
	}

	var state string
	var gotUserID int64
	if err := s.DB().QueryRowContext(ctx,
		"SELECT state, user_id FROM addresses WHERE id = ?", addressID,
	).Scan(&state, &gotUserID); err != nil {
		t.Fatalf("failed to query retired address: %v", err)
	}
	if state != "retired" || gotUserID != userID {
		t.Fatalf("expected retired address to keep user_id=%d, got state=%q user_id=%d", userID, state, gotUserID)
	}

	// A late payment arrives on the now-retired address.
	var depositID int64
	err := s.DB().QueryRowContext(ctx,
		`INSERT INTO deposits (address_id, txid, vout, amount_koinu, confirmations, state, created_at)
		 VALUES (?, 'txid-late-payment', 0, 100000000, 1, 'confirmed', '2026-01-01T00:00:00Z') RETURNING id`,
		addressID,
	).Scan(&depositID)
	if err != nil {
		t.Fatalf("failed to insert late-payment deposit: %v", err)
	}

	hook := NewPurchaseCreditHook(s, settings, ledger)
	if err := hook(ctx, depositID); err != nil {
		t.Fatalf("credit hook failed on late payment: %v", err)
	}

	balance, err := ledger.Balance(ctx, userID)
	if err != nil {
		t.Fatalf("Balance failed: %v", err)
	}
	if balance != 1 {
		t.Errorf("expected the retired address's original owner to be credited 1 token, got balance %d", balance)
	}
}
