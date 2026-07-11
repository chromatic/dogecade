package services

import (
	"context"
	"errors"
	"testing"
)

func TestUsersServiceSearchByDisplayName(t *testing.T) {
	s := newTestStore(t)
	svc := NewUsersService(s)
	ctx := context.Background()

	if _, err := svc.GetOrCreateBySubjectHash(ctx, "subj-1", "Alice Arcade", false); err != nil {
		t.Fatalf("failed to seed user: %v", err)
	}
	if _, err := svc.GetOrCreateBySubjectHash(ctx, "subj-2", "Bob Pinball", false); err != nil {
		t.Fatalf("failed to seed user: %v", err)
	}

	results, err := svc.SearchByDisplayName(ctx, "Arcade", 10)
	if err != nil {
		t.Fatalf("SearchByDisplayName failed: %v", err)
	}
	if len(results) != 1 || results[0].DisplayName != "Alice Arcade" {
		t.Fatalf("expected one match for 'Arcade', got %+v", results)
	}

	none, err := svc.SearchByDisplayName(ctx, "Nonexistent", 10)
	if err != nil {
		t.Fatalf("SearchByDisplayName failed: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("expected no matches, got %+v", none)
	}
}

func TestUsersServiceMerge(t *testing.T) {
	s := newTestStore(t)
	usersSvc := NewUsersService(s)
	ledgerSvc := NewLedgerService(s)
	ctx := context.Background()

	fromID := seedUser(t, ctx, s, "from-subject")
	toID := seedUser(t, ctx, s, "to-subject")

	depositID := seedDeposit(t, ctx, s, fromID)
	if err := ledgerSvc.CreditPurchase(ctx, fromID, depositID, 3); err != nil {
		t.Fatalf("CreditPurchase failed: %v", err)
	}

	if err := usersSvc.Merge(ctx, fromID, toID); err != nil {
		t.Fatalf("Merge failed: %v", err)
	}

	toBalance, err := ledgerSvc.Balance(ctx, toID)
	if err != nil {
		t.Fatalf("Balance failed: %v", err)
	}
	if toBalance != 3 {
		t.Fatalf("expected merged balance of 3, got %d", toBalance)
	}

	if _, err := usersSvc.GetByID(ctx, fromID); err == nil {
		t.Fatalf("expected merged-from user to no longer exist")
	}

	if err := usersSvc.Merge(ctx, toID, toID); !errors.Is(err, ErrCannotMergeSameUser) {
		t.Fatalf("expected ErrCannotMergeSameUser, got %v", err)
	}
}
