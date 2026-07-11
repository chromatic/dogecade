package services

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

func newSilentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRotationJobRotatesAfterUseCountThreshold(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	settings := NewSettingsService(s)
	direct := NewDirectPayService(s, settings)
	job := NewRotationJob(s, settings, direct, newSilentLogger())

	machineID := seedMachine(t, ctx, s, "rotation-uses", true)
	if _, err := s.DB().ExecContext(ctx, "UPDATE machines SET direct_pay_enabled = 1 WHERE id = ?", machineID); err != nil {
		t.Fatalf("failed to enable direct pay: %v", err)
	}
	if err := settings.SetDirectPayRotateAfterUses(ctx, 2); err != nil {
		t.Fatalf("SetDirectPayRotateAfterUses failed: %v", err)
	}

	seedPoolAddress(t, ctx, s, "DRotUsesOld", "machine_direct")
	oldAddr, err := direct.Activate(ctx, machineID)
	if err != nil {
		t.Fatalf("Activate failed: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx, "UPDATE addresses SET use_count = 2 WHERE address = ?", oldAddr); err != nil {
		t.Fatalf("failed to set use_count: %v", err)
	}
	seedPoolAddress(t, ctx, s, "DRotUsesNew", "machine_direct")

	if err := job.CheckAll(ctx); err != nil {
		t.Fatalf("CheckAll failed: %v", err)
	}

	active, ok, err := direct.ActiveAddress(ctx, machineID)
	if err != nil || !ok {
		t.Fatalf("expected an active address, got ok=%v err=%v", ok, err)
	}
	if active.Address != "DRotUsesNew" {
		t.Fatalf("expected rotation to DRotUsesNew, got %q", active.Address)
	}
}

func TestRotationJobRotatesAfterInterval(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	settings := NewSettingsService(s)
	direct := NewDirectPayService(s, settings)
	job := NewRotationJob(s, settings, direct, newSilentLogger())

	machineID := seedMachine(t, ctx, s, "rotation-interval", true)
	if _, err := s.DB().ExecContext(ctx, "UPDATE machines SET direct_pay_enabled = 1 WHERE id = ?", machineID); err != nil {
		t.Fatalf("failed to enable direct pay: %v", err)
	}
	if err := settings.SetDirectPayRotateIntervalHours(ctx, 1); err != nil {
		t.Fatalf("SetDirectPayRotateIntervalHours failed: %v", err)
	}

	seedPoolAddress(t, ctx, s, "DRotIntervalOld", "machine_direct")
	oldAddr, err := direct.Activate(ctx, machineID)
	if err != nil {
		t.Fatalf("Activate failed: %v", err)
	}
	staleAssignedAt := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	if _, err := s.DB().ExecContext(ctx, "UPDATE addresses SET assigned_at = ? WHERE address = ?", staleAssignedAt, oldAddr); err != nil {
		t.Fatalf("failed to backdate assigned_at: %v", err)
	}
	seedPoolAddress(t, ctx, s, "DRotIntervalNew", "machine_direct")

	if err := job.CheckAll(ctx); err != nil {
		t.Fatalf("CheckAll failed: %v", err)
	}

	active, ok, err := direct.ActiveAddress(ctx, machineID)
	if err != nil || !ok {
		t.Fatalf("expected an active address, got ok=%v err=%v", ok, err)
	}
	if active.Address != "DRotIntervalNew" {
		t.Fatalf("expected rotation to DRotIntervalNew, got %q", active.Address)
	}
}

func TestRotationJobNoopWhenBothThresholdsDisabled(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	settings := NewSettingsService(s)
	direct := NewDirectPayService(s, settings)
	job := NewRotationJob(s, settings, direct, newSilentLogger())

	machineID := seedMachine(t, ctx, s, "rotation-disabled", true)
	if _, err := s.DB().ExecContext(ctx, "UPDATE machines SET direct_pay_enabled = 1 WHERE id = ?", machineID); err != nil {
		t.Fatalf("failed to enable direct pay: %v", err)
	}

	seedPoolAddress(t, ctx, s, "DRotDisabledOld", "machine_direct")
	oldAddr, err := direct.Activate(ctx, machineID)
	if err != nil {
		t.Fatalf("Activate failed: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx, "UPDATE addresses SET use_count = 999 WHERE address = ?", oldAddr); err != nil {
		t.Fatalf("failed to set use_count: %v", err)
	}
	seedPoolAddress(t, ctx, s, "DRotDisabledNew", "machine_direct")

	if err := job.CheckAll(ctx); err != nil {
		t.Fatalf("CheckAll failed: %v", err)
	}

	active, ok, err := direct.ActiveAddress(ctx, machineID)
	if err != nil || !ok || active.Address != oldAddr {
		t.Fatalf("expected no rotation to occur, got %+v ok=%v err=%v", active, ok, err)
	}
}
