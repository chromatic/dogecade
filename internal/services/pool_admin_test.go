package services

import (
	"context"
	"testing"
)

func TestPoolServiceListByStateAndBatches(t *testing.T) {
	s := newTestStore(t)
	settings := NewSettingsService(s)
	svc := NewPoolService(s, settings)
	ctx := context.Background()

	batchID := seedPoolAddresses(t, ctx, s, 3)

	pool, err := svc.ListByState(ctx, "pool", 10)
	if err != nil {
		t.Fatalf("ListByState failed: %v", err)
	}
	if len(pool) != 3 {
		t.Fatalf("expected 3 pool addresses, got %d", len(pool))
	}
	if pool[0].BatchID != batchID {
		t.Errorf("expected batch id %d, got %d", batchID, pool[0].BatchID)
	}

	if _, _, err := svc.Assign(ctx, "token_deposit"); err != nil {
		t.Fatalf("Assign failed: %v", err)
	}
	pool, err = svc.ListByState(ctx, "pool", 10)
	if err != nil {
		t.Fatalf("ListByState failed: %v", err)
	}
	if len(pool) != 2 {
		t.Fatalf("expected 2 pool addresses after assign, got %d", len(pool))
	}

	assigned, err := svc.ListByState(ctx, "assigned", 10)
	if err != nil {
		t.Fatalf("ListByState(assigned) failed: %v", err)
	}
	if len(assigned) != 1 {
		t.Fatalf("expected 1 assigned address, got %d", len(assigned))
	}

	batches, err := svc.ListBatches(ctx)
	if err != nil {
		t.Fatalf("ListBatches failed: %v", err)
	}
	if len(batches) != 1 || batches[0].AddressCount != 3 {
		t.Fatalf("expected 1 batch with 3 addresses, got %+v", batches)
	}
}
