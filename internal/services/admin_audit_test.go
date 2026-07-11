package services

import (
	"context"
	"testing"
)

func TestAdminAuditServiceLogAndList(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	userID := seedUser(t, ctx, s, "admin-subject")

	svc := NewAdminAuditService(s)
	if err := svc.Log(ctx, userID, "machine.create", "machine:1", "slug=pinball-1"); err != nil {
		t.Fatalf("Log failed: %v", err)
	}
	if err := svc.Log(ctx, userID, "machine.set_active", "machine:1", "active=false"); err != nil {
		t.Fatalf("Log failed: %v", err)
	}

	entries, err := svc.List(ctx, 10)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Action != "machine.set_active" {
		t.Errorf("expected most recent entry first, got %+v", entries[0])
	}
}
