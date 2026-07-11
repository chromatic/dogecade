package services

import (
	"context"
	"errors"
	"testing"
)

func TestDirectPayActivateAndActiveAddress(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	settings := NewSettingsService(s)
	direct := NewDirectPayService(s, settings)
	machineID := seedMachine(t, ctx, s, "pinball-1", true)

	if _, ok, err := direct.ActiveAddress(ctx, machineID); err != nil || ok {
		t.Fatalf("expected no active address yet, got ok=%v err=%v", ok, err)
	}

	seedPoolAddress(t, ctx, s, "DDirectAddr1", "machine_direct")

	addr, err := direct.Activate(ctx, machineID)
	if err != nil {
		t.Fatalf("Activate failed: %v", err)
	}
	if addr != "DDirectAddr1" {
		t.Fatalf("expected DDirectAddr1, got %q", addr)
	}

	active, ok, err := direct.ActiveAddress(ctx, machineID)
	if err != nil || !ok {
		t.Fatalf("expected active address, got ok=%v err=%v", ok, err)
	}
	if active.Address != "DDirectAddr1" || active.UseCount != 0 {
		t.Fatalf("unexpected active address: %+v", active)
	}
}

func TestDirectPayActivatePoolEmpty(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	settings := NewSettingsService(s)
	direct := NewDirectPayService(s, settings)
	machineID := seedMachine(t, ctx, s, "pinball-1", true)

	if _, err := direct.Activate(ctx, machineID); !errors.Is(err, ErrPoolEmpty) {
		t.Fatalf("expected ErrPoolEmpty, got %v", err)
	}
}

func TestDirectPayRotateReplacesAddress(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	settings := NewSettingsService(s)
	direct := NewDirectPayService(s, settings)
	machineID := seedMachine(t, ctx, s, "pinball-1", true)

	seedPoolAddress(t, ctx, s, "DDirectAddrOld", "machine_direct")
	oldAddr, err := direct.Activate(ctx, machineID)
	if err != nil {
		t.Fatalf("Activate failed: %v", err)
	}

	seedPoolAddress(t, ctx, s, "DDirectAddrNew", "machine_direct")
	newAddr, err := direct.Rotate(ctx, machineID)
	if err != nil {
		t.Fatalf("Rotate failed: %v", err)
	}
	if newAddr != "DDirectAddrNew" {
		t.Fatalf("expected DDirectAddrNew, got %q", newAddr)
	}

	var oldState string
	if err := s.DB().QueryRowContext(ctx, "SELECT state FROM addresses WHERE address = ?", oldAddr).Scan(&oldState); err != nil {
		t.Fatalf("failed to query old address state: %v", err)
	}
	if oldState != "retired" {
		t.Fatalf("expected old address retired, got %q", oldState)
	}

	active, ok, err := direct.ActiveAddress(ctx, machineID)
	if err != nil || !ok || active.Address != "DDirectAddrNew" {
		t.Fatalf("expected new address active, got %+v ok=%v err=%v", active, ok, err)
	}
}

func TestDirectPayRotateKeepsOldAddressWhenPoolEmpty(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	settings := NewSettingsService(s)
	direct := NewDirectPayService(s, settings)
	alerts := NewAlertsService(s)
	machineID := seedMachine(t, ctx, s, "pinball-1", true)

	seedPoolAddress(t, ctx, s, "DDirectAddrOnly", "machine_direct")
	oldAddr, err := direct.Activate(ctx, machineID)
	if err != nil {
		t.Fatalf("Activate failed: %v", err)
	}

	if _, err := direct.Rotate(ctx, machineID); !errors.Is(err, ErrPoolEmpty) {
		t.Fatalf("expected ErrPoolEmpty, got %v", err)
	}

	active, ok, err := direct.ActiveAddress(ctx, machineID)
	if err != nil || !ok || active.Address != oldAddr {
		t.Fatalf("expected old address to remain active, got %+v ok=%v err=%v", active, ok, err)
	}

	unacked, err := alerts.ListUnacked(ctx)
	if err != nil {
		t.Fatalf("ListUnacked failed: %v", err)
	}
	found := false
	for _, a := range unacked {
		if a.Kind == "direct_pay_pool_empty" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a direct_pay_pool_empty alert, got %+v", unacked)
	}
}

func TestDirectPayCreditDepositQueuesPulsesAndCapsCredits(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	settings := NewSettingsService(s)
	direct := NewDirectPayService(s, settings)
	machineID := seedMachine(t, ctx, s, "pinball-1", true)

	if err := settings.SetDirectPayMaxCreditsPerTx(ctx, 3); err != nil {
		t.Fatalf("SetDirectPayMaxCreditsPerTx failed: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx, "UPDATE machines SET direct_play_price_koinu = 100000000 WHERE id = ?", machineID); err != nil {
		t.Fatalf("failed to set machine price: %v", err)
	}

	addressID := seedPoolAddress(t, ctx, s, "DDirectAddrCredit", "machine_direct")
	if _, err := s.DB().ExecContext(ctx,
		"UPDATE addresses SET state = 'assigned', machine_id = ? WHERE id = ?", machineID, addressID,
	); err != nil {
		t.Fatalf("failed to bind address to machine: %v", err)
	}

	var depositID int64
	if err := s.DB().QueryRowContext(ctx,
		`INSERT INTO deposits (address_id, txid, vout, amount_koinu, confirmations, state, created_at)
		 VALUES (?, 'txid-direct-1', 0, 550000000, 1, 'confirmed', '2024-01-01T00:00:00Z') RETURNING id`,
		addressID,
	).Scan(&depositID); err != nil {
		t.Fatalf("failed to seed deposit: %v", err)
	}

	if err := direct.CreditDeposit(ctx, depositID, addressID, machineID); err != nil {
		t.Fatalf("CreditDeposit failed: %v", err)
	}

	// 550000000 koinu / 100000000 koinu per credit = 5 credits, capped to 3.
	var pulseCount int
	if err := s.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM credit_pulses WHERE machine_id = ? AND source = 'direct_pay'", machineID,
	).Scan(&pulseCount); err != nil {
		t.Fatalf("failed to count credit pulses: %v", err)
	}
	if pulseCount != 3 {
		t.Fatalf("expected 3 capped credit pulses, got %d", pulseCount)
	}

	var remainder int64
	if err := s.DB().QueryRowContext(ctx, "SELECT remainder_koinu FROM deposits WHERE id = ?", depositID).Scan(&remainder); err != nil {
		t.Fatalf("failed to query remainder: %v", err)
	}
	if remainder != 50000000 {
		t.Fatalf("expected remainder 50000000, got %d", remainder)
	}

	var useCount int
	if err := s.DB().QueryRowContext(ctx, "SELECT use_count FROM addresses WHERE id = ?", addressID).Scan(&useCount); err != nil {
		t.Fatalf("failed to query use count: %v", err)
	}
	if useCount != 1 {
		t.Fatalf("expected use_count 1, got %d", useCount)
	}
}

func TestDirectPayCreditDepositNoopsWithoutPrice(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	settings := NewSettingsService(s)
	direct := NewDirectPayService(s, settings)
	machineID := seedMachine(t, ctx, s, "pinball-1", true) // no price set

	addressID := seedPoolAddress(t, ctx, s, "DDirectAddrNoPrice", "machine_direct")
	var depositID int64
	if err := s.DB().QueryRowContext(ctx,
		`INSERT INTO deposits (address_id, txid, vout, amount_koinu, confirmations, state, created_at)
		 VALUES (?, 'txid-direct-2', 0, 100000000, 1, 'confirmed', '2024-01-01T00:00:00Z') RETURNING id`,
		addressID,
	).Scan(&depositID); err != nil {
		t.Fatalf("failed to seed deposit: %v", err)
	}

	if err := direct.CreditDeposit(ctx, depositID, addressID, machineID); err != nil {
		t.Fatalf("CreditDeposit failed: %v", err)
	}

	var pulseCount int
	if err := s.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM credit_pulses WHERE machine_id = ?", machineID,
	).Scan(&pulseCount); err != nil {
		t.Fatalf("failed to count credit pulses: %v", err)
	}
	if pulseCount != 0 {
		t.Fatalf("expected no credit pulses without a configured price, got %d", pulseCount)
	}
}

func TestDirectPayAwareCreditHookRoutesByAddressPurpose(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	settings := NewSettingsService(s)
	ledger := NewLedgerService(s)
	direct := NewDirectPayService(s, settings)
	hook := NewDirectPayAwareCreditHook(s, settings, ledger, direct)

	t.Run("token_deposit address credits the bound user", func(t *testing.T) {
		userID := seedUser(t, ctx, s, "hook-user")
		addressID := seedPoolAddress(t, ctx, s, "DHookTokenAddr", "token_deposit")
		if _, err := s.DB().ExecContext(ctx,
			"UPDATE addresses SET state = 'assigned', user_id = ? WHERE id = ?", userID, addressID,
		); err != nil {
			t.Fatalf("failed to bind address to user: %v", err)
		}
		var depositID int64
		if err := s.DB().QueryRowContext(ctx,
			`INSERT INTO deposits (address_id, txid, vout, amount_koinu, confirmations, state, created_at)
			 VALUES (?, 'txid-hook-1', 0, 100000000, 1, 'confirmed', '2024-01-01T00:00:00Z') RETURNING id`,
			addressID,
		).Scan(&depositID); err != nil {
			t.Fatalf("failed to seed deposit: %v", err)
		}

		if err := hook(ctx, depositID); err != nil {
			t.Fatalf("hook failed: %v", err)
		}
		balance, err := ledger.Balance(ctx, userID)
		if err != nil || balance != 1 {
			t.Fatalf("expected balance 1, got %d (err %v)", balance, err)
		}
	})

	t.Run("machine_direct address queues pulses instead", func(t *testing.T) {
		machineID := seedMachine(t, ctx, s, "hook-machine", true)
		if _, err := s.DB().ExecContext(ctx, "UPDATE machines SET direct_play_price_koinu = 100000000 WHERE id = ?", machineID); err != nil {
			t.Fatalf("failed to set machine price: %v", err)
		}
		addressID := seedPoolAddress(t, ctx, s, "DHookMachineAddr", "machine_direct")
		if _, err := s.DB().ExecContext(ctx,
			"UPDATE addresses SET state = 'assigned', machine_id = ? WHERE id = ?", machineID, addressID,
		); err != nil {
			t.Fatalf("failed to bind address to machine: %v", err)
		}
		var depositID int64
		if err := s.DB().QueryRowContext(ctx,
			`INSERT INTO deposits (address_id, txid, vout, amount_koinu, confirmations, state, created_at)
			 VALUES (?, 'txid-hook-2', 0, 100000000, 1, 'confirmed', '2024-01-01T00:00:00Z') RETURNING id`,
			addressID,
		).Scan(&depositID); err != nil {
			t.Fatalf("failed to seed deposit: %v", err)
		}

		if err := hook(ctx, depositID); err != nil {
			t.Fatalf("hook failed: %v", err)
		}
		var pulseCount int
		if err := s.DB().QueryRowContext(ctx,
			"SELECT COUNT(*) FROM credit_pulses WHERE machine_id = ? AND source = 'direct_pay'", machineID,
		).Scan(&pulseCount); err != nil {
			t.Fatalf("failed to count credit pulses: %v", err)
		}
		if pulseCount != 1 {
			t.Fatalf("expected 1 credit pulse, got %d", pulseCount)
		}
	})
}
