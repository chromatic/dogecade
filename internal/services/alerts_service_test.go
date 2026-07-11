package services

import (
	"context"
	"testing"
)

func TestAlertsServiceListAndAck(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := InsertAlertIfNotExists(ctx, s.DB(), "pool_low_warn", "pool is low"); err != nil {
		t.Fatalf("InsertAlertIfNotExists failed: %v", err)
	}

	svc := NewAlertsService(s)
	alerts, err := svc.ListUnacked(ctx)
	if err != nil {
		t.Fatalf("ListUnacked failed: %v", err)
	}
	if len(alerts) != 1 || alerts[0].Kind != "pool_low_warn" {
		t.Fatalf("expected one unacked pool_low_warn alert, got %+v", alerts)
	}

	if err := svc.Ack(ctx, alerts[0].ID); err != nil {
		t.Fatalf("Ack failed: %v", err)
	}

	alerts, err = svc.ListUnacked(ctx)
	if err != nil {
		t.Fatalf("ListUnacked failed: %v", err)
	}
	if len(alerts) != 0 {
		t.Fatalf("expected no unacked alerts after Ack, got %+v", alerts)
	}

	if err := svc.Ack(ctx, 99999); err == nil {
		t.Fatalf("expected error acking a nonexistent alert")
	}
}
