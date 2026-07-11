package services

import (
	"context"
	"testing"
)

func TestDepositsServiceListFiltersByState(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	userID := seedUser(t, ctx, s, "subject-1")
	seedDeposit(t, ctx, s, userID) // state = 'seen'

	svc := NewDepositsService(s)

	all, err := svc.List(ctx, "", 10)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 deposit, got %d", len(all))
	}

	seen, err := svc.List(ctx, "seen", 10)
	if err != nil {
		t.Fatalf("List(seen) failed: %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("expected 1 seen deposit, got %d", len(seen))
	}

	credited, err := svc.List(ctx, "credited", 10)
	if err != nil {
		t.Fatalf("List(credited) failed: %v", err)
	}
	if len(credited) != 0 {
		t.Fatalf("expected 0 credited deposits, got %d", len(credited))
	}
}
