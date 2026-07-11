package services

import (
	"context"
	"errors"
	"testing"
)

func TestMachinesServiceCreateListToggle(t *testing.T) {
	s := newTestStore(t)
	svc := NewMachinesService(s)
	ctx := context.Background()

	id, err := svc.Create(ctx, "pinball-1", "Pinball One")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	_, err = svc.Create(ctx, "pinball-1", "Duplicate")
	if !errors.Is(err, ErrMachineSlugTaken) {
		t.Fatalf("expected ErrMachineSlugTaken, got %v", err)
	}

	all, err := svc.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll failed: %v", err)
	}
	if len(all) != 1 || !all[0].IsActive {
		t.Fatalf("expected one active machine, got %+v", all)
	}

	if err := svc.SetActive(ctx, id, false); err != nil {
		t.Fatalf("SetActive failed: %v", err)
	}
	active, err := svc.ListActive(ctx)
	if err != nil {
		t.Fatalf("ListActive failed: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("expected zero active machines after disabling, got %+v", active)
	}
}

func TestMachinesServiceSetDirectPay(t *testing.T) {
	s := newTestStore(t)
	svc := NewMachinesService(s)
	ctx := context.Background()

	id, err := svc.Create(ctx, "direct-pay-1", "Direct Pay One")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if err := svc.SetDirectPay(ctx, id, true, 100000000); err != nil {
		t.Fatalf("SetDirectPay failed: %v", err)
	}
	m, err := svc.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID failed: %v", err)
	}
	if !m.DirectPayEnabled || m.DirectPlayPriceKoinu != 100000000 {
		t.Fatalf("expected direct pay enabled with price 100000000, got %+v", m)
	}

	if err := svc.SetDirectPay(ctx, id, false, 0); err != nil {
		t.Fatalf("SetDirectPay(disable) failed: %v", err)
	}
	m, err = svc.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID failed: %v", err)
	}
	if m.DirectPayEnabled {
		t.Fatalf("expected direct pay disabled, got %+v", m)
	}
	if m.DirectPlayPriceKoinu != 100000000 {
		t.Fatalf("expected price to be preserved (not cleared) after disabling, got %+v", m)
	}

	if err := svc.SetDirectPay(ctx, id, true, 250000000); err != nil {
		t.Fatalf("SetDirectPay(re-enable) failed: %v", err)
	}
	m, err = svc.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID failed: %v", err)
	}
	if !m.DirectPayEnabled || m.DirectPlayPriceKoinu != 250000000 {
		t.Fatalf("expected re-enabling with a new price to take effect, got %+v", m)
	}
}
