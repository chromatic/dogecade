package services

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestBalanceZeroForNewUser(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := NewLedgerService(s)
	userID := seedUser(t, ctx, s, "subject-1")

	balance, err := ledger.Balance(ctx, userID)
	if err != nil {
		t.Fatalf("Balance failed: %v", err)
	}
	if balance != 0 {
		t.Errorf("expected balance 0 for new user, got %d", balance)
	}
}

func TestCreditPurchaseIncreasesBalance(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := NewLedgerService(s)
	userID := seedUser(t, ctx, s, "subject-1")
	depositID := seedDeposit(t, ctx, s, userID)

	if err := ledger.CreditPurchase(ctx, userID, depositID, 5); err != nil {
		t.Fatalf("CreditPurchase failed: %v", err)
	}

	balance, err := ledger.Balance(ctx, userID)
	if err != nil {
		t.Fatalf("Balance failed: %v", err)
	}
	if balance != 5 {
		t.Errorf("expected balance 5, got %d", balance)
	}
}

func TestCreditPurchaseRejectsNonPositiveTokens(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := NewLedgerService(s)
	userID := seedUser(t, ctx, s, "subject-1")
	depositID := seedDeposit(t, ctx, s, userID)

	if err := ledger.CreditPurchase(ctx, userID, depositID, 0); err == nil {
		t.Error("expected error crediting 0 tokens, got nil")
	}
	if err := ledger.CreditPurchase(ctx, userID, depositID, -1); err == nil {
		t.Error("expected error crediting negative tokens, got nil")
	}
}

// TestBalanceNeverNegative is the core ledger invariant: a debit must not be
// allowed to push the balance below zero, even under concurrent attempts.
func TestBalanceNeverNegative(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := NewLedgerService(s)
	redemption := NewRedemptionService(s, ledger)
	userID := seedUser(t, ctx, s, "subject-1")
	machineID := seedMachine(t, ctx, s, "machine-1", true)
	depositID := seedDeposit(t, ctx, s, userID)

	if err := ledger.CreditPurchase(ctx, userID, depositID, 1); err != nil {
		t.Fatalf("CreditPurchase failed: %v", err)
	}

	const attempts = 10
	var wg sync.WaitGroup
	successes := make([]bool, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := redemption.Redeem(ctx, userID, machineID)
			successes[i] = err == nil
		}(i)
	}
	wg.Wait()

	successCount := 0
	for _, ok := range successes {
		if ok {
			successCount++
		}
	}
	if successCount != 1 {
		t.Errorf("expected exactly 1 successful redemption of a 1-token balance, got %d", successCount)
	}

	balance, err := ledger.Balance(ctx, userID)
	if err != nil {
		t.Fatalf("Balance failed: %v", err)
	}
	if balance != 0 {
		t.Errorf("expected balance 0 after redeeming the only token, got %d", balance)
	}
	if balance < 0 {
		t.Fatalf("balance went negative: %d", balance)
	}
}

func TestDebitRedemptionEveryDebitReferencesAPulse(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := NewLedgerService(s)
	redemption := NewRedemptionService(s, ledger)
	userID := seedUser(t, ctx, s, "subject-1")
	machineID := seedMachine(t, ctx, s, "machine-1", true)
	depositID := seedDeposit(t, ctx, s, userID)

	if err := ledger.CreditPurchase(ctx, userID, depositID, 1); err != nil {
		t.Fatalf("CreditPurchase failed: %v", err)
	}
	pulseID, err := redemption.Redeem(ctx, userID, machineID)
	if err != nil {
		t.Fatalf("Redeem failed: %v", err)
	}

	var referencedPulseID int64
	err = s.DB().QueryRowContext(ctx,
		"SELECT pulse_id FROM token_ledger WHERE kind = 'redemption' AND user_id = ?",
		userID,
	).Scan(&referencedPulseID)
	if err != nil {
		t.Fatalf("failed to query redemption ledger row: %v", err)
	}
	if referencedPulseID != pulseID {
		t.Errorf("expected redemption ledger row to reference pulse %d, got %d", pulseID, referencedPulseID)
	}
}

func TestEveryPurchaseCreditReferencesADeposit(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := NewLedgerService(s)
	userID := seedUser(t, ctx, s, "subject-1")
	depositID := seedDeposit(t, ctx, s, userID)

	if err := ledger.CreditPurchase(ctx, userID, depositID, 3); err != nil {
		t.Fatalf("CreditPurchase failed: %v", err)
	}

	var referencedDepositID int64
	err := s.DB().QueryRowContext(ctx,
		"SELECT deposit_id FROM token_ledger WHERE kind = 'purchase' AND user_id = ?",
		userID,
	).Scan(&referencedDepositID)
	if err != nil {
		t.Fatalf("failed to query purchase ledger row: %v", err)
	}
	if referencedDepositID != depositID {
		t.Errorf("expected purchase ledger row to reference deposit %d, got %d", depositID, referencedDepositID)
	}
}

func TestDebitRedemptionInsufficientBalance(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := NewLedgerService(s)
	userID := seedUser(t, ctx, s, "subject-1")

	err := ledger.DebitRedemption(ctx, userID, 0)
	if !errors.Is(err, ErrInsufficientBalance) {
		t.Errorf("expected ErrInsufficientBalance, got %v", err)
	}

	balance, err := ledger.Balance(ctx, userID)
	if err != nil {
		t.Fatalf("Balance failed: %v", err)
	}
	if balance != 0 {
		t.Errorf("expected balance unchanged at 0 after failed debit, got %d", balance)
	}
}

func TestRefundRestoresBalance(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := NewLedgerService(s)
	redemption := NewRedemptionService(s, ledger)
	userID := seedUser(t, ctx, s, "subject-1")
	machineID := seedMachine(t, ctx, s, "machine-1", true)
	depositID := seedDeposit(t, ctx, s, userID)

	if err := ledger.CreditPurchase(ctx, userID, depositID, 1); err != nil {
		t.Fatalf("CreditPurchase failed: %v", err)
	}
	pulseID, err := redemption.Redeem(ctx, userID, machineID)
	if err != nil {
		t.Fatalf("Redeem failed: %v", err)
	}

	if err := ledger.Refund(ctx, userID, pulseID, "dispatch failed after retries"); err != nil {
		t.Fatalf("Refund failed: %v", err)
	}

	balance, err := ledger.Balance(ctx, userID)
	if err != nil {
		t.Fatalf("Balance failed: %v", err)
	}
	if balance != 1 {
		t.Errorf("expected balance 1 after refund, got %d", balance)
	}
}

func TestAdminAdjustRequiresNote(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := NewLedgerService(s)
	userID := seedUser(t, ctx, s, "subject-1")

	if err := ledger.AdminAdjust(ctx, userID, 10, ""); err == nil {
		t.Error("expected error for admin adjustment with empty note, got nil")
	}

	if err := ledger.AdminAdjust(ctx, userID, 10, "goodwill credit"); err != nil {
		t.Fatalf("AdminAdjust failed: %v", err)
	}
	balance, err := ledger.Balance(ctx, userID)
	if err != nil {
		t.Fatalf("Balance failed: %v", err)
	}
	if balance != 10 {
		t.Errorf("expected balance 10 after admin adjust, got %d", balance)
	}
}
