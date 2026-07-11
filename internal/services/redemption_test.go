package services

import (
	"context"
	"errors"
	"testing"
)

func TestRedeemInsertsPendingPulse(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := NewLedgerService(s)
	redemption := NewRedemptionService(s, ledger)
	userID := seedUser(t, ctx, s, "subject-1")
	machineID := seedMachine(t, ctx, s, "machine-1", true)
	depositID := seedDeposit(t, ctx, s, userID)

	if err := ledger.CreditPurchase(ctx, userID, depositID, 2); err != nil {
		t.Fatalf("CreditPurchase failed: %v", err)
	}

	pulseID, err := redemption.Redeem(ctx, userID, machineID)
	if err != nil {
		t.Fatalf("Redeem failed: %v", err)
	}

	var gotMachineID, gotUserID int64
	var source, state string
	err = s.DB().QueryRowContext(ctx,
		"SELECT machine_id, user_id, source, state FROM credit_pulses WHERE id = ?",
		pulseID,
	).Scan(&gotMachineID, &gotUserID, &source, &state)
	if err != nil {
		t.Fatalf("failed to query pulse: %v", err)
	}
	if gotMachineID != machineID {
		t.Errorf("expected pulse machine_id %d, got %d", machineID, gotMachineID)
	}
	if gotUserID != userID {
		t.Errorf("expected pulse user_id %d, got %d", userID, gotUserID)
	}
	if source != "token_redemption" {
		t.Errorf("expected source 'token_redemption', got %q", source)
	}
	if state != "pending" {
		t.Errorf("expected state 'pending', got %q", state)
	}

	balance, err := ledger.Balance(ctx, userID)
	if err != nil {
		t.Fatalf("Balance failed: %v", err)
	}
	if balance != 1 {
		t.Errorf("expected balance 1 after redeeming 1 of 2 tokens, got %d", balance)
	}
}

func TestRedeemUnknownMachineReturnsError(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := NewLedgerService(s)
	redemption := NewRedemptionService(s, ledger)
	userID := seedUser(t, ctx, s, "subject-1")
	depositID := seedDeposit(t, ctx, s, userID)

	if err := ledger.CreditPurchase(ctx, userID, depositID, 1); err != nil {
		t.Fatalf("CreditPurchase failed: %v", err)
	}

	_, err := redemption.Redeem(ctx, userID, 9999)
	if !errors.Is(err, ErrMachineNotFound) {
		t.Errorf("expected ErrMachineNotFound, got %v", err)
	}

	balance, err := ledger.Balance(ctx, userID)
	if err != nil {
		t.Fatalf("Balance failed: %v", err)
	}
	if balance != 1 {
		t.Errorf("expected balance untouched at 1 after redeeming against unknown machine, got %d", balance)
	}
}

func TestRedeemInactiveMachineReturnsError(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := NewLedgerService(s)
	redemption := NewRedemptionService(s, ledger)
	userID := seedUser(t, ctx, s, "subject-1")
	machineID := seedMachine(t, ctx, s, "machine-1", false)
	depositID := seedDeposit(t, ctx, s, userID)

	if err := ledger.CreditPurchase(ctx, userID, depositID, 1); err != nil {
		t.Fatalf("CreditPurchase failed: %v", err)
	}

	_, err := redemption.Redeem(ctx, userID, machineID)
	if !errors.Is(err, ErrMachineNotActive) {
		t.Errorf("expected ErrMachineNotActive, got %v", err)
	}

	balance, err := ledger.Balance(ctx, userID)
	if err != nil {
		t.Fatalf("Balance failed: %v", err)
	}
	if balance != 1 {
		t.Errorf("expected balance untouched at 1 after redeeming against inactive machine, got %d", balance)
	}

	var pulseCount int
	if err := s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM credit_pulses WHERE machine_id = ?", machineID).Scan(&pulseCount); err != nil {
		t.Fatalf("failed to count pulses: %v", err)
	}
	if pulseCount != 0 {
		t.Errorf("expected no pulse row inserted for a rejected redemption, got %d", pulseCount)
	}
}

func TestRedeemInsufficientBalanceLeavesNoPulse(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ledger := NewLedgerService(s)
	redemption := NewRedemptionService(s, ledger)
	userID := seedUser(t, ctx, s, "subject-1")
	machineID := seedMachine(t, ctx, s, "machine-1", true)

	_, err := redemption.Redeem(ctx, userID, machineID)
	if !errors.Is(err, ErrInsufficientBalance) {
		t.Errorf("expected ErrInsufficientBalance, got %v", err)
	}

	var pulseCount int
	if err := s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM credit_pulses WHERE machine_id = ?", machineID).Scan(&pulseCount); err != nil {
		t.Fatalf("failed to count pulses: %v", err)
	}
	if pulseCount != 0 {
		t.Errorf("expected the pulse insert to be rolled back when the debit fails, got %d pulse rows", pulseCount)
	}
}
