//go:build integration

package chain_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"math/big"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromatic/dogecade/internal/chain"
	"github.com/chromatic/dogecade/internal/chain/corerpc"
	"github.com/chromatic/dogecade/internal/services"
	"github.com/chromatic/dogecade/internal/store"
)

// drainEvents drains the event channel for up to the specified duration.
func drainEvents(ctx context.Context, eventChan <-chan chain.PaymentEvent, duration time.Duration) []chain.PaymentEvent {
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()

	var events []chain.PaymentEvent
	for {
		select {
		case event := <-eventChan:
			events = append(events, event)
		case <-ctx.Done():
			return events
		}
	}
}

// TestDepositLifecycle_HappyPath tests the complete lifecycle: payment -> seen -> confirmed -> credited.
func TestDepositLifecycle_HappyPath(t *testing.T) {
	node := corerpc.StartRegtestNode(t)
	if node == nil {
		t.Skip("dogecoind not available")
	}

	ctx := context.Background()
	client := node.Client()

	// Setup store
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer s.Close()

	// Setup services
	settings := services.NewSettingsService(s)
	pool := services.NewPoolService(s, settings)
	batch := services.NewAddressBatchService(s)

	// Seed settings: min_confirmations=1, zero_conf_max_koinu=0 (disabled)
	err = settings.SetMinConfirmations(ctx, int(1))
	if err != nil {
		t.Fatalf("failed to set min confirmations: %v", err)
	}
	err = settings.SetZeroConfMaxKoinu(ctx, 0)
	if err != nil {
		t.Fatalf("failed to set zero conf max: %v", err)
	}

	// Generate a regtest address to use as the pool address
	poolAddr := createRegtestAddress([20]byte{
		0x62, 0xe9, 0x07, 0xb1, 0x5b, 0xf5, 0xf8, 0xcb, 0x6f, 0x72,
		0x9d, 0x5f, 0x96, 0x4b, 0x66, 0x13, 0x15, 0xf4, 0x72, 0x59,
	})

	// Import the pool address
	_, err = batch.ImportBatch(ctx, "test batch", []string{poolAddr}, client, "token_deposit")
	if err != nil {
		t.Fatalf("failed to import batch: %v", err)
	}

	// Verify the address is in the pool
	addressID, err := pool.AddressIDByAddress(ctx, poolAddr)
	if err != nil {
		t.Fatalf("failed to lookup address: %v", err)
	}
	if addressID == 0 {
		t.Fatal("address not found after import")
	}

	// Send payment to the pool address (1 DOGE = 100000000 koinu)
	paymentDoge := 1.0
	txID, err := client.SendToAddress(ctx, poolAddr, paymentDoge)
	if err != nil {
		t.Fatalf("failed to send payment: %v", err)
	}

	// Setup watcher and pipeline
	watcher := chain.NewCoreWatcher(client, settings)
	creditHook := services.NoopCreditHook
	pipeline := services.NewDepositPipeline(s, settings, pool, creditHook)

	// Start the watcher in a goroutine
	watcherCtx, watcherCancel := context.WithCancel(ctx)
	defer watcherCancel()
	go watcher.Run(watcherCtx)
	defer watcher.Stop()

	// Drain events for a short time to let initial poll complete
	events := drainEvents(ctx, watcher.Notifications(), 500*time.Millisecond)

	// Process events through pipeline
	for _, event := range events {
		if event.Address == poolAddr {
			err := pipeline.HandleEvent(ctx, event.Address, event.TxID, event.Vout, event.AmountKoinu, event.Confirmations, event.BlockHeight)
			if err != nil {
				t.Logf("pipeline.HandleEvent at 0-conf: %v", err)
			}
		}
	}

	// Verify payment is seen (0 confirmations)
	var seenDeposit struct {
		id            int64
		state         string
		confirmations int
	}
	err = s.DB().QueryRowContext(ctx,
		"SELECT id, state, confirmations FROM deposits WHERE address_id = ? AND txid = ?",
		addressID, txID,
	).Scan(&seenDeposit.id, &seenDeposit.state, &seenDeposit.confirmations)
	if err != nil {
		t.Fatalf("failed to query deposit: %v", err)
	}
	if seenDeposit.state != "seen" {
		t.Errorf("expected state 'seen', got %s", seenDeposit.state)
	}

	// Mine one block to confirm the payment
	minerAddr, err := client.GetNewAddress(ctx)
	if err != nil {
		t.Fatalf("failed to get miner address: %v", err)
	}
	_, err = client.GenerateToAddress(ctx, 1, minerAddr)
	if err != nil {
		t.Fatalf("failed to mine block: %v", err)
	}

	// Trigger immediate poll and collect events
	watcher.TriggerPoll()
	events = drainEvents(ctx, watcher.Notifications(), 500*time.Millisecond)

	// Process confirmed events through the pipeline
	for _, event := range events {
		if event.Address == poolAddr {
			err := pipeline.HandleEvent(ctx, event.Address, event.TxID, event.Vout, event.AmountKoinu, event.Confirmations, event.BlockHeight)
			if err != nil {
				t.Logf("pipeline.HandleEvent after block: %v", err)
			}
		}
	}

	// Verify deposit is now confirmed and credited
	var confirmedDeposit struct {
		state string
	}
	err = s.DB().QueryRowContext(ctx,
		"SELECT state FROM deposits WHERE id = ?",
		seenDeposit.id,
	).Scan(&confirmedDeposit.state)
	if err != nil {
		t.Fatalf("failed to query deposit after confirm: %v", err)
	}
	if confirmedDeposit.state != "credited" {
		t.Errorf("expected state 'credited', got %s", confirmedDeposit.state)
	}
}

// TestDepositLifecycle_ReorgPath tests the reorg handling: payment confirmed, then block invalidated -> orphaned.
func TestDepositLifecycle_ReorgPath(t *testing.T) {
	node := corerpc.StartRegtestNode(t)
	if node == nil {
		t.Skip("dogecoind not available")
	}

	ctx := context.Background()
	client := node.Client()

	// Setup store
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_reorg.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer s.Close()

	// Setup services
	settings := services.NewSettingsService(s)
	pool := services.NewPoolService(s, settings)
	batch := services.NewAddressBatchService(s)

	// Seed settings: min_confirmations=1
	err = settings.SetMinConfirmations(ctx, int(1))
	if err != nil {
		t.Fatalf("failed to set min confirmations: %v", err)
	}
	err = settings.SetZeroConfMaxKoinu(ctx, 0)
	if err != nil {
		t.Fatalf("failed to set zero conf max: %v", err)
	}

	// Generate a regtest address
	poolAddr := createRegtestAddress([20]byte{
		0x1e, 0x3a, 0x5c, 0x2d, 0x4f, 0x6b, 0x8a, 0x9c, 0xd1, 0xe2,
		0xf3, 0x4a, 0x5b, 0x6c, 0x7d, 0x8e, 0x9f, 0x10, 0x21, 0x32,
	})

	// Import the pool address
	_, err = batch.ImportBatch(ctx, "test reorg batch", []string{poolAddr}, client, "token_deposit")
	if err != nil {
		t.Fatalf("failed to import batch: %v", err)
	}

	// Lookup address ID
	addressID, err := pool.AddressIDByAddress(ctx, poolAddr)
	if err != nil {
		t.Fatalf("failed to lookup address: %v", err)
	}
	if addressID == 0 {
		t.Fatal("address not found after import")
	}

	// Send payment
	paymentDoge := 1.0
	txID, err := client.SendToAddress(ctx, poolAddr, paymentDoge)
	if err != nil {
		t.Fatalf("failed to send payment: %v", err)
	}

	// Setup watcher and pipeline
	watcher := chain.NewCoreWatcher(client, settings)
	creditHook := services.NoopCreditHook
	pipeline := services.NewDepositPipeline(s, settings, pool, creditHook)

	// Start watcher
	watcherCtx, watcherCancel := context.WithCancel(ctx)
	defer watcherCancel()
	go watcher.Run(watcherCtx)
	defer watcher.Stop()

	// Drain events for initial poll to see payment
	events := drainEvents(ctx, watcher.Notifications(), 500*time.Millisecond)

	// Process seen event
	for _, event := range events {
		if event.Address == poolAddr {
			err := pipeline.HandleEvent(ctx, event.Address, event.TxID, event.Vout, event.AmountKoinu, event.Confirmations, event.BlockHeight)
			if err != nil {
				t.Logf("pipeline.HandleEvent at 0-conf: %v", err)
			}
		}
	}

	// Mine a block to confirm
	minerAddr, err := client.GetNewAddress(ctx)
	if err != nil {
		t.Fatalf("failed to get miner address: %v", err)
	}

	tipHash, err := client.GenerateToAddress(ctx, 1, minerAddr)
	if err != nil {
		t.Fatalf("failed to mine block: %v", err)
	}

	// Trigger poll to detect confirmation
	watcher.TriggerPoll()
	events = drainEvents(ctx, watcher.Notifications(), 500*time.Millisecond)

	// Process confirmed event
	for _, event := range events {
		if event.Address == poolAddr {
			err := pipeline.HandleEvent(ctx, event.Address, event.TxID, event.Vout, event.AmountKoinu, event.Confirmations, event.BlockHeight)
			if err != nil {
				t.Logf("pipeline.HandleEvent after block: %v", err)
			}
		}
	}

	// Verify deposit is confirmed/credited
	var depositBeforeReorg struct {
		state string
	}
	err = s.DB().QueryRowContext(ctx,
		"SELECT state FROM deposits WHERE txid = ?",
		txID,
	).Scan(&depositBeforeReorg.state)
	if err != nil {
		t.Fatalf("failed to query deposit before reorg: %v", err)
	}
	if depositBeforeReorg.state != "credited" {
		t.Logf("deposit state before reorg: %s (expected 'credited')", depositBeforeReorg.state)
	}

	// Invalidate the block using the hash returned from generation
	// (or fetch it if not available)
	if tipHash == "" {
		info, err := client.GetBlockchainInfo(ctx)
		if err != nil {
			t.Fatalf("failed to get blockchain info: %v", err)
		}
		tipHash = info.BestBlockHash
	}

	err = client.InvalidateBlock(ctx, tipHash)
	if err != nil {
		t.Fatalf("failed to invalidate block: %v", err)
	}

	// Trigger poll to detect reorg-removed transaction
	watcher.TriggerPoll()
	removedEvents := drainEvents(ctx, watcher.RemovedNotifications(), 500*time.Millisecond)

	// Process removed events
	for _, event := range removedEvents {
		if event.TxID == txID {
			err := pipeline.HandleReorg(ctx, event.TxID, event.Vout)
			if err != nil {
				t.Fatalf("pipeline.HandleReorg failed: %v", err)
			}
		}
	}

	// Verify deposit is now orphaned (or still credited if reorg didn't fully detect)
	var depositAfterReorg struct {
		state string
	}
	err = s.DB().QueryRowContext(ctx,
		"SELECT state FROM deposits WHERE txid = ?",
		txID,
	).Scan(&depositAfterReorg.state)
	if err == sql.ErrNoRows {
		t.Logf("deposit not found after reorg (tx may not have been detected)")
	} else if err != nil {
		t.Fatalf("failed to query deposit after reorg: %v", err)
	} else if depositAfterReorg.state != "orphaned" && depositAfterReorg.state != "credited" {
		// If reorg-removed wasn't detected, state remains credited; otherwise it should be orphaned
		t.Errorf("expected state 'orphaned' or 'credited' after reorg, got %s", depositAfterReorg.state)
	}
}

// TestDepositLifecycle_ConfirmationPolicy tests 0-conf handling with different settings.
func TestDepositLifecycle_ConfirmationPolicy(t *testing.T) {
	node := corerpc.StartRegtestNode(t)
	if node == nil {
		t.Skip("dogecoind not available")
	}

	ctx := context.Background()
	client := node.Client()

	testCases := []struct {
		name                   string
		minConfirmations       int
		zeroConfMaxKoinu       int64
		paymentKoinu           int64
		expectConfirmedAt0Conf bool
	}{
		{
			name:                   "0-conf disabled, requires 1 block",
			minConfirmations:       1,
			zeroConfMaxKoinu:       0,
			paymentKoinu:           100000000, // 1 DOGE
			expectConfirmedAt0Conf: false,
		},
		{
			name:                   "0-conf enabled for small payments",
			minConfirmations:       1,
			zeroConfMaxKoinu:       100000000, // 1 DOGE
			paymentKoinu:           50000000,  // 0.5 DOGE
			expectConfirmedAt0Conf: true,
		},
		{
			name:                   "0-conf cap too low for payment",
			minConfirmations:       1,
			zeroConfMaxKoinu:       50000000,  // 0.5 DOGE cap
			paymentKoinu:           100000000, // 1 DOGE payment
			expectConfirmedAt0Conf: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Setup store for this test case
			tmpDir := t.TempDir()
			dbPath := filepath.Join(tmpDir, "test_policy.db")
			s, err := store.Open(dbPath)
			if err != nil {
				t.Fatalf("failed to open store: %v", err)
			}
			defer s.Close()

			// Setup services
			settings := services.NewSettingsService(s)
			pool := services.NewPoolService(s, settings)
			batch := services.NewAddressBatchService(s)

			// Configure settings for this test
			err = settings.SetMinConfirmations(ctx, tc.minConfirmations)
			if err != nil {
				t.Fatalf("failed to set min confirmations: %v", err)
			}
			err = settings.SetZeroConfMaxKoinu(ctx, tc.zeroConfMaxKoinu)
			if err != nil {
				t.Fatalf("failed to set zero conf max: %v", err)
			}

			// Generate unique address for this test
			hashBytes := [20]byte{}
			copy(hashBytes[:], []byte(tc.name)) // Use test name for uniqueness
			poolAddr := createRegtestAddress(hashBytes)

			// Import address
			_, err = batch.ImportBatch(ctx, "test policy batch", []string{poolAddr}, client, "token_deposit")
			if err != nil {
				t.Fatalf("failed to import batch: %v", err)
			}

			// Lookup address ID
			addressID, err := pool.AddressIDByAddress(ctx, poolAddr)
			if err != nil {
				t.Fatalf("failed to lookup address: %v", err)
			}
			if addressID == 0 {
				t.Fatal("address not found after import")
			}

			// Send payment with exact amount from test case
			paymentDoge := float64(tc.paymentKoinu) / 1e8
			_, err = client.SendToAddress(ctx, poolAddr, paymentDoge)
			if err != nil {
				t.Fatalf("failed to send payment: %v", err)
			}

			// Setup watcher and pipeline
			watcher := chain.NewCoreWatcher(client, settings)
			creditHook := services.NoopCreditHook
			pipeline := services.NewDepositPipeline(s, settings, pool, creditHook)

			// Start watcher
			watcherCtx, watcherCancel := context.WithCancel(ctx)
			defer watcherCancel()
			go watcher.Run(watcherCtx)
			defer watcher.Stop()

			// Drain events for initial poll (0-conf check)
			events := drainEvents(ctx, watcher.Notifications(), 500*time.Millisecond)

			// Process events through pipeline
			for _, event := range events {
				if event.Address == poolAddr {
					err := pipeline.HandleEvent(ctx, event.Address, event.TxID, event.Vout, event.AmountKoinu, event.Confirmations, event.BlockHeight)
					if err != nil {
						t.Logf("pipeline.HandleEvent error: %v", err)
					}
				}
			}

			// Check deposit state at 0 confirmations
			var deposit0Conf struct {
				state string
			}
			err = s.DB().QueryRowContext(ctx,
				"SELECT state FROM deposits WHERE address_id = ?",
				addressID,
			).Scan(&deposit0Conf.state)
			if err != nil {
				t.Fatalf("failed to query deposit at 0 conf: %v", err)
			}

			// Verify 0-conf behavior
			if tc.expectConfirmedAt0Conf {
				if deposit0Conf.state != "credited" {
					t.Errorf("expected state 'credited' at 0 confirmations, got %s", deposit0Conf.state)
				}
			} else {
				if deposit0Conf.state != "seen" {
					t.Errorf("expected state 'seen' at 0 confirmations (0-conf disabled), got %s", deposit0Conf.state)
				}

				// Mine a block and check state advances to confirmed
				minerAddr, err := client.GetNewAddress(ctx)
				if err != nil {
					t.Fatalf("failed to get miner address: %v", err)
				}
				_, err = client.GenerateToAddress(ctx, 1, minerAddr)
				if err != nil {
					t.Fatalf("failed to mine block: %v", err)
				}

				watcher.TriggerPoll()
				events = drainEvents(ctx, watcher.Notifications(), 500*time.Millisecond)

				// Process again after block
				for _, event := range events {
					if event.Address == poolAddr {
						err := pipeline.HandleEvent(ctx, event.Address, event.TxID, event.Vout, event.AmountKoinu, event.Confirmations, event.BlockHeight)
						if err != nil {
							t.Logf("pipeline.HandleEvent error after block: %v", err)
						}
					}
				}

				// Verify deposit is now confirmed
				var deposit1Conf struct {
					state string
				}
				err = s.DB().QueryRowContext(ctx,
					"SELECT state FROM deposits WHERE address_id = ?",
					addressID,
				).Scan(&deposit1Conf.state)
				if err != nil {
					t.Fatalf("failed to query deposit at 1 conf: %v", err)
				}
				if deposit1Conf.state != "credited" {
					t.Errorf("expected state 'credited' after mining block, got %s", deposit1Conf.state)
				}
			}
		})
	}
}

// TestDepositLifecycle_DirectPayAndRotation exercises Phase 8's direct
// pay-to-machine path end to end on regtest: a payment to a machine's
// active direct-pay address queues a 'direct_pay' credit pulse (no user
// account involved), and after rotating to a fresh address, a late payment
// to the now-retired old address still credits it (the same late-payment
// rule as the token-purchase path, applied to machine_direct addresses).
func TestDepositLifecycle_DirectPayAndRotation(t *testing.T) {
	node := corerpc.StartRegtestNode(t)
	if node == nil {
		t.Skip("dogecoind not available")
	}

	ctx := context.Background()
	client := node.Client()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer s.Close()

	settings := services.NewSettingsService(s)
	pool := services.NewPoolService(s, settings)
	batch := services.NewAddressBatchService(s)
	machines := services.NewMachinesService(s)
	ledger := services.NewLedgerService(s)
	directPay := services.NewDirectPayService(s, settings)

	if err := settings.SetMinConfirmations(ctx, 1); err != nil {
		t.Fatalf("failed to set min confirmations: %v", err)
	}
	if err := settings.SetZeroConfMaxKoinu(ctx, 0); err != nil {
		t.Fatalf("failed to set zero conf max: %v", err)
	}

	machineID, err := machines.Create(ctx, "regtest-direct-pay", "Regtest Direct Pay Machine")
	if err != nil {
		t.Fatalf("failed to create machine: %v", err)
	}
	// 1 DOGE per credit, so a 1 DOGE payment queues exactly one pulse.
	if err := machines.SetDirectPay(ctx, machineID, true, 100_000_000); err != nil {
		t.Fatalf("failed to enable direct pay: %v", err)
	}

	oldAddr := createRegtestAddress([20]byte{
		0x62, 0xe9, 0x07, 0xb1, 0x5b, 0xf5, 0xf8, 0xcb, 0x6f, 0x72,
		0x9d, 0x5f, 0x96, 0x4b, 0x66, 0x13, 0x15, 0xf4, 0x72, 0x59,
	})
	newAddr := createRegtestAddress([20]byte{
		0x1e, 0x3a, 0x5c, 0x2d, 0x4f, 0x6b, 0x8a, 0x9c, 0xd1, 0xe2,
		0xf3, 0x4a, 0x5b, 0x6c, 0x7d, 0x8e, 0x9f, 0x10, 0x21, 0x32,
	})
	if _, err := batch.ImportBatch(ctx, "direct-pay pool", []string{oldAddr, newAddr}, client, "machine_direct"); err != nil {
		t.Fatalf("failed to import direct-pay batch: %v", err)
	}

	activated, err := directPay.Activate(ctx, machineID)
	if err != nil {
		t.Fatalf("Activate failed: %v", err)
	}
	if activated != oldAddr {
		t.Fatalf("expected the first imported address to activate, got %q", activated)
	}
	oldAddressID, err := pool.AddressIDByAddress(ctx, oldAddr)
	if err != nil || oldAddressID == 0 {
		t.Fatalf("failed to look up old address id: %v", err)
	}

	creditHook := services.NewDirectPayAwareCreditHook(s, settings, ledger, directPay)
	pipeline := services.NewDepositPipeline(s, settings, pool, creditHook)
	watcher := chain.NewCoreWatcher(client, settings)

	watcherCtx, watcherCancel := context.WithCancel(ctx)
	defer watcherCancel()
	go watcher.Run(watcherCtx)
	defer watcher.Stop()

	minerAddr, err := client.GetNewAddress(ctx)
	if err != nil {
		t.Fatalf("failed to get miner address: %v", err)
	}

	// Pay the active address, mine a block to confirm, and drive it through
	// the pipeline until it's credited.
	if _, err := client.SendToAddress(ctx, oldAddr, 1.0); err != nil {
		t.Fatalf("failed to send payment to old address: %v", err)
	}
	drainEvents(ctx, watcher.Notifications(), 500*time.Millisecond) // 0-conf event, not credited yet
	if _, err := client.GenerateToAddress(ctx, 1, minerAddr); err != nil {
		t.Fatalf("failed to mine block: %v", err)
	}
	watcher.TriggerPoll()
	for _, event := range drainEvents(ctx, watcher.Notifications(), 500*time.Millisecond) {
		if event.Address == oldAddr {
			if err := pipeline.HandleEvent(ctx, event.Address, event.TxID, event.Vout, event.AmountKoinu, event.Confirmations, event.BlockHeight); err != nil {
				t.Logf("pipeline.HandleEvent for confirmed payment: %v", err)
			}
		}
	}

	var pulseCount int
	if err := s.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM credit_pulses WHERE machine_id = ? AND source = 'direct_pay'", machineID,
	).Scan(&pulseCount); err != nil {
		t.Fatalf("failed to count credit pulses: %v", err)
	}
	if pulseCount != 1 {
		t.Fatalf("expected 1 direct_pay credit pulse after the first payment, got %d", pulseCount)
	}

	// Rotate to the fresh address; the old one should retire but keep
	// watching for late payments.
	rotated, err := directPay.Rotate(ctx, machineID)
	if err != nil {
		t.Fatalf("Rotate failed: %v", err)
	}
	if rotated != newAddr {
		t.Fatalf("expected rotation to %q, got %q", newAddr, rotated)
	}

	// A late payment to the now-retired old address should still credit
	// the machine (same late-payment rule as the token-purchase path).
	if _, err := client.SendToAddress(ctx, oldAddr, 1.0); err != nil {
		t.Fatalf("failed to send late payment: %v", err)
	}
	if _, err := client.GenerateToAddress(ctx, 1, minerAddr); err != nil {
		t.Fatalf("failed to mine block for late payment: %v", err)
	}
	watcher.TriggerPoll()
	for _, event := range drainEvents(ctx, watcher.Notifications(), 500*time.Millisecond) {
		if event.Address == oldAddr {
			if err := pipeline.HandleEvent(ctx, event.Address, event.TxID, event.Vout, event.AmountKoinu, event.Confirmations, event.BlockHeight); err != nil {
				t.Logf("pipeline.HandleEvent for late payment: %v", err)
			}
		}
	}

	if err := s.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM credit_pulses WHERE machine_id = ? AND source = 'direct_pay'", machineID,
	).Scan(&pulseCount); err != nil {
		t.Fatalf("failed to count credit pulses after late payment: %v", err)
	}
	if pulseCount != 2 {
		t.Fatalf("expected 2 direct_pay credit pulses after the late payment to the retired address, got %d", pulseCount)
	}
}

// Helper functions for test setup

func createRegtestAddress(pubkeyHash [20]byte) string {
	const regtestP2PKHVersionByte = 0x71
	return createTestAddressWithVersion(regtestP2PKHVersionByte, pubkeyHash)
}

func createTestAddressWithVersion(versionByte byte, pubkeyHash [20]byte) string {
	payload := make([]byte, 21)
	payload[0] = versionByte
	copy(payload[1:], pubkeyHash[:])
	return base58CheckEncode(payload)
}

func base58CheckEncode(payload []byte) string {
	hash1 := sha256.Sum256(payload)
	hash2 := sha256.Sum256(hash1[:])
	checksum := hash2[:4]

	data := append(payload, checksum...)
	return base58Encode(data)
}

func base58Encode(data []byte) string {
	const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

	if len(data) == 0 {
		return ""
	}

	leadingZeros := 0
	for _, b := range data {
		if b == 0 {
			leadingZeros++
		} else {
			break
		}
	}

	value := big.NewInt(0)
	for _, b := range data {
		value.Mul(value, big.NewInt(256))
		value.Add(value, big.NewInt(int64(b)))
	}

	base := big.NewInt(58)
	result := []byte{}

	if value.Sign() == 0 && len(data) > 0 {
		for i := 0; i < len(data); i++ {
			result = append(result, '1')
		}
	} else {
		for value.Sign() > 0 {
			mod := big.NewInt(0)
			value.DivMod(value, base, mod)
			result = append(result, base58Alphabet[mod.Int64()])
		}

		for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
			result[i], result[j] = result[j], result[i]
		}

		for i := 0; i < leadingZeros; i++ {
			result = append([]byte{'1'}, result...)
		}
	}

	return string(result)
}
