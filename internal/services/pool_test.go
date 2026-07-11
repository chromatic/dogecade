package services

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestAssignClaimsOldestPoolRow(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	svc := NewPoolService(s, NewSettingsService(s))

	// Seed 3 pool addresses
	seedPoolAddresses(t, ctx, s, 3)

	// First assign should return lowest ID
	address1, id1, err := svc.Assign(ctx, "token_deposit")
	if err != nil {
		t.Fatalf("first Assign failed: %v", err)
	}
	if address1 == "" {
		t.Fatal("Assign returned empty address")
	}
	if id1 <= 0 {
		t.Fatal("Assign returned invalid ID")
	}

	// Verify the address is now assigned
	var state string
	err = s.DB().QueryRowContext(ctx,
		"SELECT state FROM addresses WHERE id = ?",
		id1,
	).Scan(&state)
	if err != nil {
		t.Fatalf("failed to query address state: %v", err)
	}
	if state != "assigned" {
		t.Errorf("expected state 'assigned', got %q", state)
	}

	// Second assign should return next lowest ID
	address2, id2, err := svc.Assign(ctx, "token_deposit")
	if err != nil {
		t.Fatalf("second Assign failed: %v", err)
	}
	if id2 <= id1 {
		t.Errorf("second Assign did not claim lower ID: first=%d, second=%d", id1, id2)
	}
	if address2 == address1 {
		t.Fatal("second Assign returned same address as first")
	}
}

func TestAssignEmptyPoolReturnsError(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	svc := NewPoolService(s, NewSettingsService(s))

	// No addresses seeded; pool is empty
	address, id, err := svc.Assign(ctx, "token_deposit")
	if err == nil {
		t.Fatal("expected Assign to return error on empty pool")
	}
	if !errors.Is(err, ErrPoolEmpty) {
		t.Errorf("expected ErrPoolEmpty, got %v", err)
	}
	if address != "" {
		t.Errorf("expected empty address on error, got %q", address)
	}
	if id != 0 {
		t.Errorf("expected zero ID on error, got %d", id)
	}
}

func TestAssignIsExclusive(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	svc := NewPoolService(s, NewSettingsService(s))

	// Seed 2 pool addresses
	seedPoolAddresses(t, ctx, s, 2)

	// Assign both
	_, _, err := svc.Assign(ctx, "token_deposit")
	if err != nil {
		t.Fatalf("first Assign failed: %v", err)
	}

	_, _, err = svc.Assign(ctx, "token_deposit")
	if err != nil {
		t.Fatalf("second Assign failed: %v", err)
	}

	// Third assign on empty pool should fail
	_, _, err = svc.Assign(ctx, "token_deposit")
	if err == nil {
		t.Fatal("third Assign should fail on empty pool")
	}
	if !errors.Is(err, ErrPoolEmpty) {
		t.Errorf("expected ErrPoolEmpty, got %v", err)
	}
}

func TestReleaseFlipsAssignedBackToPool(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	svc := NewPoolService(s, NewSettingsService(s))

	// Seed and assign
	seedPoolAddresses(t, ctx, s, 1)
	_, id, err := svc.Assign(ctx, "token_deposit")
	if err != nil {
		t.Fatalf("Assign failed: %v", err)
	}

	// Release it
	err = svc.Release(ctx, id)
	if err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	// Verify it's back to pool state with cleared assigned_at
	var state string
	var assignedAt sql.NullString
	err = s.DB().QueryRowContext(ctx,
		"SELECT state, assigned_at FROM addresses WHERE id = ?",
		id,
	).Scan(&state, &assignedAt)
	if err != nil {
		t.Fatalf("failed to query address: %v", err)
	}
	if state != "pool" {
		t.Errorf("expected state 'pool' after release, got %q", state)
	}
	if assignedAt.Valid {
		t.Errorf("expected assigned_at to be NULL after release, got %q", assignedAt.String)
	}
}

func TestRetireFlipsToRetired(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	svc := NewPoolService(s, NewSettingsService(s))

	// Seed and assign
	seedPoolAddresses(t, ctx, s, 1)
	_, id, err := svc.Assign(ctx, "token_deposit")
	if err != nil {
		t.Fatalf("Assign failed: %v", err)
	}

	// Retire it
	err = svc.Retire(ctx, id)
	if err != nil {
		t.Fatalf("Retire failed: %v", err)
	}

	// Verify state is retired and retired_at is set
	var state string
	var retiredAt sql.NullString
	err = s.DB().QueryRowContext(ctx,
		"SELECT state, retired_at FROM addresses WHERE id = ?",
		id,
	).Scan(&state, &retiredAt)
	if err != nil {
		t.Fatalf("failed to query address: %v", err)
	}
	if state != "retired" {
		t.Errorf("expected state 'retired' after retire, got %q", state)
	}
	if !retiredAt.Valid || retiredAt.String == "" {
		t.Fatal("expected retired_at to be set after retire")
	}
}

func TestCountsByStateReflectsAllStates(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	svc := NewPoolService(s, NewSettingsService(s))

	// Seed 4 addresses
	seedPoolAddresses(t, ctx, s, 4)

	// Assign and retire some
	_, id1, _ := svc.Assign(ctx, "token_deposit")
	_, _, _ = svc.Assign(ctx, "token_deposit")
	svc.Retire(ctx, id1)

	// Now we have: 2 pool, 1 assigned, 1 retired
	counts, err := svc.CountsByState(ctx)
	if err != nil {
		t.Fatalf("CountsByState failed: %v", err)
	}

	if counts["pool"] != 2 {
		t.Errorf("expected pool=2, got %d", counts["pool"])
	}
	if counts["assigned"] != 1 {
		t.Errorf("expected assigned=1, got %d", counts["assigned"])
	}
	if counts["retired"] != 1 {
		t.Errorf("expected retired=1, got %d", counts["retired"])
	}
}

func TestCheckLowWaterBelowUrgentThreshold(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	settingsSvc := NewSettingsService(s)
	poolSvc := NewPoolService(s, settingsSvc)

	// Set thresholds (warn=25, urgent=10)
	if err := settingsSvc.SetPoolWarnThreshold(ctx, 25); err != nil {
		t.Fatalf("SetPoolWarnThreshold failed: %v", err)
	}
	if err := settingsSvc.SetPoolUrgentThreshold(ctx, 10); err != nil {
		t.Fatalf("SetPoolUrgentThreshold failed: %v", err)
	}

	// Seed 5 addresses (below both thresholds)
	seedPoolAddresses(t, ctx, s, 5)

	// CheckLowWater should insert an urgent alert
	err := poolSvc.CheckLowWater(ctx)
	if err != nil {
		t.Fatalf("CheckLowWater failed: %v", err)
	}

	// Verify alert was created
	var count int
	var kind string
	err = s.DB().QueryRowContext(ctx,
		"SELECT COUNT(*), kind FROM alerts WHERE acked_at IS NULL GROUP BY kind",
	).Scan(&count, &kind)
	if err == sql.ErrNoRows {
		t.Fatal("expected alert to be created")
	}
	if err != nil {
		t.Fatalf("failed to query alert: %v", err)
	}
	if kind != "pool_low_urgent" {
		t.Errorf("expected alert kind 'pool_low_urgent', got %q", kind)
	}
	if count != 1 {
		t.Errorf("expected 1 alert, got %d", count)
	}
}

func TestCheckLowWaterBelowWarnThreshold(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	settingsSvc := NewSettingsService(s)
	poolSvc := NewPoolService(s, settingsSvc)

	// Set thresholds (warn=25, urgent=10)
	if err := settingsSvc.SetPoolWarnThreshold(ctx, 25); err != nil {
		t.Fatalf("SetPoolWarnThreshold failed: %v", err)
	}
	if err := settingsSvc.SetPoolUrgentThreshold(ctx, 10); err != nil {
		t.Fatalf("SetPoolUrgentThreshold failed: %v", err)
	}

	// Seed 15 addresses (below warn threshold but above urgent)
	seedPoolAddresses(t, ctx, s, 15)

	// CheckLowWater should insert a warn alert
	err := poolSvc.CheckLowWater(ctx)
	if err != nil {
		t.Fatalf("CheckLowWater failed: %v", err)
	}

	// Verify warn alert was created
	var count int
	var kind string
	err = s.DB().QueryRowContext(ctx,
		"SELECT COUNT(*), kind FROM alerts WHERE acked_at IS NULL GROUP BY kind",
	).Scan(&count, &kind)
	if err == sql.ErrNoRows {
		t.Fatal("expected alert to be created")
	}
	if err != nil {
		t.Fatalf("failed to query alert: %v", err)
	}
	if kind != "pool_low_warn" {
		t.Errorf("expected alert kind 'pool_low_warn', got %q", kind)
	}
}

func TestCheckLowWaterAboveThresholds(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	settingsSvc := NewSettingsService(s)
	poolSvc := NewPoolService(s, settingsSvc)

	// Set thresholds
	if err := settingsSvc.SetPoolWarnThreshold(ctx, 25); err != nil {
		t.Fatalf("SetPoolWarnThreshold failed: %v", err)
	}
	if err := settingsSvc.SetPoolUrgentThreshold(ctx, 10); err != nil {
		t.Fatalf("SetPoolUrgentThreshold failed: %v", err)
	}

	// Seed 50 addresses (above both thresholds)
	seedPoolAddresses(t, ctx, s, 50)

	// CheckLowWater should not insert any alert
	err := poolSvc.CheckLowWater(ctx)
	if err != nil {
		t.Fatalf("CheckLowWater failed: %v", err)
	}

	// Verify no alerts were created
	var count int
	err = s.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM alerts WHERE acked_at IS NULL",
	).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query alerts: %v", err)
	}
	if count != 0 {
		t.Errorf("expected no alerts, got %d", count)
	}
}

func TestCheckLowWaterDoesNotDuplicateUnakedAlerts(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	settingsSvc := NewSettingsService(s)
	poolSvc := NewPoolService(s, settingsSvc)

	// Set thresholds
	if err := settingsSvc.SetPoolWarnThreshold(ctx, 25); err != nil {
		t.Fatalf("SetPoolWarnThreshold failed: %v", err)
	}
	if err := settingsSvc.SetPoolUrgentThreshold(ctx, 10); err != nil {
		t.Fatalf("SetPoolUrgentThreshold failed: %v", err)
	}

	// Seed 5 addresses (below both thresholds, triggers urgent)
	seedPoolAddresses(t, ctx, s, 5)

	// First CheckLowWater should create alert
	err := poolSvc.CheckLowWater(ctx)
	if err != nil {
		t.Fatalf("first CheckLowWater failed: %v", err)
	}

	// Second CheckLowWater should NOT create a duplicate
	err = poolSvc.CheckLowWater(ctx)
	if err != nil {
		t.Fatalf("second CheckLowWater failed: %v", err)
	}

	// Verify only one alert exists
	var count int
	err = s.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM alerts WHERE kind = 'pool_low_urgent' AND acked_at IS NULL",
	).Scan(&count)
	if err != nil {
		t.Fatalf("failed to count alerts: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 alert after two CheckLowWater calls, got %d", count)
	}
}

func TestReleaseNonexistentAddressReturnsError(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	svc := NewPoolService(s, NewSettingsService(s))

	// Try to release an address ID that doesn't exist
	err := svc.Release(ctx, 999)
	if err == nil {
		t.Fatal("expected Release to return error for non-existent address")
	}
	if !errors.Is(err, errors.New("no address found with id 999")) {
		t.Logf("Release returned expected error: %v", err)
	}
}

func TestRetireNonexistentAddressReturnsError(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	svc := NewPoolService(s, NewSettingsService(s))

	// Try to retire an address ID that doesn't exist
	err := svc.Retire(ctx, 999)
	if err == nil {
		t.Fatal("expected Retire to return error for non-existent address")
	}
	if !errors.Is(err, errors.New("no address found with id 999")) {
		t.Logf("Retire returned expected error: %v", err)
	}
}

func TestCheckLowWaterAlertRefiresAfterAck(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	settingsSvc := NewSettingsService(s)
	poolSvc := NewPoolService(s, settingsSvc)

	// Set thresholds
	if err := settingsSvc.SetPoolWarnThreshold(ctx, 25); err != nil {
		t.Fatalf("SetPoolWarnThreshold failed: %v", err)
	}
	if err := settingsSvc.SetPoolUrgentThreshold(ctx, 10); err != nil {
		t.Fatalf("SetPoolUrgentThreshold failed: %v", err)
	}

	// Seed 5 addresses (below urgent threshold)
	seedPoolAddresses(t, ctx, s, 5)

	// First CheckLowWater creates alert
	if err := poolSvc.CheckLowWater(ctx); err != nil {
		t.Fatalf("first CheckLowWater failed: %v", err)
	}

	var alertID int64
	err := s.DB().QueryRowContext(ctx,
		"SELECT id FROM alerts WHERE kind = 'pool_low_urgent' AND acked_at IS NULL",
	).Scan(&alertID)
	if err != nil {
		t.Fatalf("failed to find alert: %v", err)
	}

	// Ack the alert
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.DB().ExecContext(ctx,
		"UPDATE alerts SET acked_at = ? WHERE id = ?",
		now, alertID,
	)
	if err != nil {
		t.Fatalf("failed to ack alert: %v", err)
	}

	// Pool is still low; CheckLowWater should create a new alert
	if err := poolSvc.CheckLowWater(ctx); err != nil {
		t.Fatalf("second CheckLowWater failed: %v", err)
	}

	// Verify a new unacked alert exists
	var count int
	err = s.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM alerts WHERE kind = 'pool_low_urgent' AND acked_at IS NULL",
	).Scan(&count)
	if err != nil {
		t.Fatalf("failed to count alerts: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 unacked alert after ack+recheck, got %d", count)
	}
}

func TestAssignConcurrency(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	svc := NewPoolService(s, NewSettingsService(s))

	// Seed a small pool of addresses
	poolSize := 10
	seedPoolAddresses(t, ctx, s, poolSize)

	// Try to assign more addresses than exist concurrently
	errChan := make(chan error, poolSize+5)

	// Launch goroutines to assign addresses concurrently
	for i := 0; i < poolSize+5; i++ {
		go func() {
			_, _, err := svc.Assign(ctx, "token_deposit")
			errChan <- err
		}()
	}

	// Collect results
	successCount := 0
	failureCount := 0
	for i := 0; i < poolSize+5; i++ {
		err := <-errChan
		if err == nil {
			successCount++
		} else if errors.Is(err, ErrPoolEmpty) {
			failureCount++
		} else {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	// Verify behavior: exactly poolSize successes, rest failures
	if successCount != poolSize {
		t.Errorf("expected %d successful assigns, got %d", poolSize, successCount)
	}
	if failureCount != 5 {
		t.Errorf("expected 5 ErrPoolEmpty failures, got %d", failureCount)
	}

	// Verify no address was assigned twice (count unique assignments in DB)
	var assignedCount int
	dbErr := s.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM addresses WHERE state = 'assigned'",
	).Scan(&assignedCount)
	if dbErr != nil {
		t.Fatalf("failed to count assigned addresses: %v", dbErr)
	}
	if assignedCount != poolSize {
		t.Errorf("expected %d assigned addresses in DB, got %d", poolSize, assignedCount)
	}
}
