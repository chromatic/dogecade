package chain

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/chromatic/dogecade/internal/chain/corerpc"
	"github.com/chromatic/dogecade/internal/services"
	"github.com/chromatic/dogecade/internal/store"
)

// fakeListSinceBlocker is a fake RPC backend for testing CoreWatcher without HTTP.
type fakeListSinceBlocker interface {
	ListSinceBlock(ctx context.Context, blockHash string, targetConf int, includeWatchOnly bool) (corerpc.ListSinceBlockResult, error)
	GetBlockHash(ctx context.Context, height int64) (string, error)
}

// fakeCoreRPC implements the fakeListSinceBlocker interface for testing.
type fakeCoreRPC struct {
	transactions map[string]corerpc.ListSinceBlockResult
	blockHashes  map[int64]string
	callCount    int
	// failListSinceBlock causes ListSinceBlock to return an error
	failListSinceBlock bool
}

func (f *fakeCoreRPC) ListSinceBlock(ctx context.Context, blockHash string, targetConf int, includeWatchOnly bool) (corerpc.ListSinceBlockResult, error) {
	f.callCount++
	if f.failListSinceBlock {
		return corerpc.ListSinceBlockResult{}, fmt.Errorf("simulated RPC error")
	}
	if result, ok := f.transactions[blockHash]; ok {
		return result, nil
	}
	return corerpc.ListSinceBlockResult{}, nil
}

func (f *fakeCoreRPC) GetBlockHash(ctx context.Context, height int64) (string, error) {
	if hash, ok := f.blockHashes[height]; ok {
		return hash, nil
	}
	return "", nil
}

// testCoreWatcherSetup creates a test store and settings service for CoreWatcher tests.
// The caller is responsible for calling store.Close() when done.
func testCoreWatcherSetup(t *testing.T) (*store.Store, *services.SettingsService) {
	t.Helper()
	dbPath := t.TempDir() + "/test.db"
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	settings := services.NewSettingsService(s)
	return s, settings
}

func TestCoreWatcher_SingleReceiveTransaction(t *testing.T) {
	s, settings := testCoreWatcherSetup(t)
	defer s.Close()

	ctx := context.Background()

	// Create fake RPC with one receive transaction
	fake := &fakeCoreRPC{
		transactions: map[string]corerpc.ListSinceBlockResult{
			"": { // empty hash = genesis
				Transactions: []corerpc.TransactionInfo{
					{
						Address:       "D1234567890abcdefgh",
						Category:      "receive",
						Amount:        0.12345678,
						Confirmations: 1,
						TxID:          "tx_hash_1",
						Vout:          0,
						BlockHash:     "block_hash_1",
						BlockHeight:   100,
					},
				},
				LastBlock: "block_hash_1",
			},
		},
		blockHashes: map[int64]string{
			0: "",
		},
	}

	// Create watcher with fake backend
	watcher := newCoreWatcherWithFake(s, settings, fake)
	defer watcher.Stop()

	// Start watcher
	go watcher.Run(ctx)

	// Give it a moment to process
	time.Sleep(100 * time.Millisecond)

	// Read one event
	select {
	case event := <-watcher.Notifications():
		if event.Address != "D1234567890abcdefgh" {
			t.Errorf("expected address D1234567890abcdefgh, got %s", event.Address)
		}
		if event.TxID != "tx_hash_1" {
			t.Errorf("expected txid tx_hash_1, got %s", event.TxID)
		}
		if event.Vout != 0 {
			t.Errorf("expected vout 0, got %d", event.Vout)
		}
		// 0.12345678 DOGE = 12345678 koinu
		if event.AmountKoinu != 12345678 {
			t.Errorf("expected 12345678 koinu, got %d", event.AmountKoinu)
		}
		if event.Confirmations != 1 {
			t.Errorf("expected 1 confirmation, got %d", event.Confirmations)
		}
		if event.BlockHeight != 100 {
			t.Errorf("expected block height 100, got %d", event.BlockHeight)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for PaymentEvent")
	}

	// Verify cursor was persisted
	cursor, err := settings.GetChainCursor(ctx)
	if err != nil {
		t.Fatalf("failed to get cursor: %v", err)
	}
	if cursor != "block_hash_1" {
		t.Errorf("expected cursor block_hash_1, got %s", cursor)
	}
}

func TestCoreWatcher_KoinuConversion(t *testing.T) {
	tests := []struct {
		name         string
		amount       float64
		expectedKoin int64
	}{
		{"100 DOGE", 100.0, 10000000000},
		{"1 DOGE", 1.0, 100000000},
		{"0.1 DOGE", 0.1, 10000000},
		{"0.01 DOGE", 0.01, 1000000},
		{"0.12345678 DOGE (precise)", 0.12345678, 12345678},
		{"1 koinu", 0.00000001, 1},
		{"10.5 DOGE", 10.5, 1050000000},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s, settings := testCoreWatcherSetup(t)
			defer s.Close()

			ctx := context.Background()

			fake := &fakeCoreRPC{
				transactions: map[string]corerpc.ListSinceBlockResult{
					"": {
						Transactions: []corerpc.TransactionInfo{
							{
								Address:       "DTestAddr",
								Category:      "receive",
								Amount:        test.amount,
								Confirmations: 0,
								TxID:          "tx_test",
								Vout:          0,
								BlockHeight:   0,
							},
						},
						LastBlock: "block_1",
					},
				},
			}

			watcher := newCoreWatcherWithFake(s, settings, fake)
			defer watcher.Stop()

			go watcher.Run(ctx)
			time.Sleep(100 * time.Millisecond)

			select {
			case event := <-watcher.Notifications():
				if event.AmountKoinu != test.expectedKoin {
					t.Errorf("expected %d koinu, got %d", test.expectedKoin, event.AmountKoinu)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("timeout waiting for PaymentEvent")
			}
		})
	}
}

func TestCoreWatcher_SendCategoryIgnored(t *testing.T) {
	s, settings := testCoreWatcherSetup(t)
	defer s.Close()

	ctx := context.Background()

	fake := &fakeCoreRPC{
		transactions: map[string]corerpc.ListSinceBlockResult{
			"": {
				Transactions: []corerpc.TransactionInfo{
					{
						Address:       "D1234567890abcdefgh",
						Category:      "send",
						Amount:        -1.0,
						Confirmations: 1,
						TxID:          "tx_send",
						Vout:          0,
						BlockHeight:   100,
					},
				},
				LastBlock: "block_hash_1",
			},
		},
	}

	watcher := newCoreWatcherWithFake(s, settings, fake)
	defer watcher.Stop()

	go watcher.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	// Should NOT receive any events for send category
	select {
	case event := <-watcher.Notifications():
		t.Errorf("should not emit event for send category, got %+v", event)
	case <-time.After(500 * time.Millisecond):
		// Expected: no event
	}
}

func TestCoreWatcher_CursorPersistence(t *testing.T) {
	s, settings := testCoreWatcherSetup(t)
	defer s.Close()

	ctx := context.Background()

	// Simulate two polls with different cursors
	fake := &fakeCoreRPC{
		transactions: map[string]corerpc.ListSinceBlockResult{
			"": {
				Transactions: []corerpc.TransactionInfo{
					{
						Address:  "D1",
						Category: "receive",
						Amount:   1.0,
						TxID:     "tx_1",
						Vout:     0,
					},
				},
				LastBlock: "block_1",
			},
			"block_1": {
				Transactions: []corerpc.TransactionInfo{
					{
						Address:  "D2",
						Category: "receive",
						Amount:   2.0,
						TxID:     "tx_2",
						Vout:     0,
					},
				},
				LastBlock: "block_2",
			},
		},
	}

	watcher := newCoreWatcherWithFake(s, settings, fake)
	defer watcher.Stop()

	go watcher.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	// Drain first event
	select {
	case <-watcher.Notifications():
	case <-time.After(1 * time.Second):
	}

	// Verify cursor after first poll
	cursor1, _ := settings.GetChainCursor(ctx)
	if cursor1 != "block_1" {
		t.Errorf("after first poll: expected cursor block_1, got %s", cursor1)
	}

	// Drain second event (happens after next poll)
	time.Sleep(100 * time.Millisecond)
	select {
	case <-watcher.Notifications():
	case <-time.After(1 * time.Second):
	}

	// Verify cursor after second poll
	cursor2, _ := settings.GetChainCursor(ctx)
	if cursor2 != "block_2" {
		t.Errorf("after second poll: expected cursor block_2, got %s", cursor2)
	}
}

func TestCoreWatcher_RescanReset(t *testing.T) {
	s, settings := testCoreWatcherSetup(t)
	defer s.Close()

	ctx := context.Background()

	// Set initial cursor
	_ = settings.SetChainCursor(ctx, "block_50")

	fake := &fakeCoreRPC{
		blockHashes: map[int64]string{
			10: "block_10",
		},
		transactions: map[string]corerpc.ListSinceBlockResult{
			"block_10": {
				Transactions: []corerpc.TransactionInfo{},
				LastBlock:    "block_10",
			},
		},
	}

	watcher := newCoreWatcherWithFake(s, settings, fake)
	defer watcher.Stop()

	go watcher.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	// Call Rescan from height 10
	watcher.Rescan(ctx, 10)
	time.Sleep(100 * time.Millisecond)

	// Verify cursor was reset to block at height 10
	cursor, _ := settings.GetChainCursor(ctx)
	if cursor != "block_10" {
		t.Errorf("after rescan: expected cursor block_10, got %s", cursor)
	}
}

func TestCoreWatcher_UnconfirmedTransactions(t *testing.T) {
	s, settings := testCoreWatcherSetup(t)
	defer s.Close()

	ctx := context.Background()

	fake := &fakeCoreRPC{
		transactions: map[string]corerpc.ListSinceBlockResult{
			"": {
				Transactions: []corerpc.TransactionInfo{
					{
						Address:       "DUnconf",
						Category:      "receive",
						Amount:        0.5,
						Confirmations: 0,
						TxID:          "tx_unconf",
						Vout:          0,
						BlockHeight:   0,
					},
				},
				LastBlock: "",
			},
		},
	}

	watcher := newCoreWatcherWithFake(s, settings, fake)
	defer watcher.Stop()

	go watcher.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	select {
	case event := <-watcher.Notifications():
		if event.BlockHeight != 0 {
			t.Errorf("expected BlockHeight 0 for unconfirmed, got %d", event.BlockHeight)
		}
		if event.Confirmations != 0 {
			t.Errorf("expected 0 confirmations, got %d", event.Confirmations)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for PaymentEvent")
	}
}

func TestCoreWatcher_NilBackendDoesntPanic(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer s.Close()

	settings := services.NewSettingsService(s)
	ctx := context.Background()

	// Create watcher with nil client (unconfigured)
	watcher := NewCoreWatcher(nil, settings)

	// Watch should not panic
	_ = watcher.Watch(ctx, "D1234567890abcdefgh")

	// Rescan should not panic
	_ = watcher.Rescan(ctx, 100)

	// Notifications channel should be readable (but no events)
	select {
	case <-watcher.Notifications():
		t.Fatalf("should not receive events from nil watcher")
	case <-time.After(100 * time.Millisecond):
		// Expected
	}
}

// TestCoreWatcher_TriggerPoll tests that TriggerPoll sends a non-blocking request for an immediate poll.
func TestCoreWatcher_TriggerPoll(t *testing.T) {
	s, settings := testCoreWatcherSetup(t)
	defer s.Close()

	ctx := context.Background()

	fake := &fakeCoreRPC{
		transactions: map[string]corerpc.ListSinceBlockResult{
			"": {
				Transactions: []corerpc.TransactionInfo{
					{
						Address:  "D1",
						Category: "receive",
						Amount:   1.0,
						TxID:     "tx_1",
						Vout:     0,
					},
				},
				LastBlock: "block_1",
			},
			"block_1": {
				Transactions: []corerpc.TransactionInfo{
					{
						Address:  "D2",
						Category: "receive",
						Amount:   2.0,
						TxID:     "tx_2",
						Vout:     0,
					},
				},
				LastBlock: "block_2",
			},
		},
	}

	watcher := newCoreWatcherWithFake(s, settings, fake)
	defer watcher.Stop()

	go watcher.Run(ctx)

	// First poll happens on startup; drain first event
	time.Sleep(100 * time.Millisecond)
	select {
	case <-watcher.Notifications():
	case <-time.After(1 * time.Second):
		t.Fatalf("timeout waiting for first event")
	}

	// Verify cursor advanced to block_1 after first poll
	cursor1, _ := settings.GetChainCursor(ctx)
	if cursor1 != "block_1" {
		t.Errorf("after startup poll: expected cursor block_1, got %s", cursor1)
	}

	// Now call TriggerPoll explicitly (should force another poll)
	watcher.TriggerPoll()

	// Give the poller time to process the nudge
	time.Sleep(100 * time.Millisecond)

	// Should get a second event (from the triggered poll with new data from block_1)
	select {
	case <-watcher.Notifications():
		// Expected
	case <-time.After(1 * time.Second):
		t.Fatalf("timeout waiting for event after TriggerPoll")
	}

	// Verify cursor advanced to block_2 after triggered poll
	cursor2, _ := settings.GetChainCursor(ctx)
	if cursor2 != "block_2" {
		t.Errorf("after TriggerPoll: expected cursor block_2, got %s", cursor2)
	}
}

func TestCoreWatcher_RemovedNotifications(t *testing.T) {
	s, settings := testCoreWatcherSetup(t)
	defer s.Close()

	ctx := context.Background()

	// Create fake RPC with one receive transaction in Removed list
	fake := &fakeCoreRPC{
		transactions: map[string]corerpc.ListSinceBlockResult{
			"": { // empty hash = genesis
				Transactions: []corerpc.TransactionInfo{}, // no normal transactions
				Removed: []corerpc.TransactionInfo{
					{
						Address:       "D1234567890abcdefgh",
						Category:      "receive",
						Amount:        0.12345678,
						Confirmations: 0,
						TxID:          "tx_removed_1",
						Vout:          0,
						BlockHash:     "",
						BlockHeight:   0,
					},
				},
				LastBlock: "block_hash_1",
			},
		},
		blockHashes: map[int64]string{
			0: "",
		},
	}

	// Create watcher with fake backend
	watcher := newCoreWatcherWithFake(s, settings, fake)
	defer watcher.Stop()

	// Start watcher
	go watcher.Run(ctx)

	// Give it a moment to process
	time.Sleep(100 * time.Millisecond)

	// Read one removed event from the removedCh
	select {
	case event := <-watcher.RemovedNotifications():
		if event.Address != "D1234567890abcdefgh" {
			t.Errorf("expected address D1234567890abcdefgh, got %s", event.Address)
		}
		if event.TxID != "tx_removed_1" {
			t.Errorf("expected txid tx_removed_1, got %s", event.TxID)
		}
		if event.Vout != 0 {
			t.Errorf("expected vout 0, got %d", event.Vout)
		}
		// 0.12345678 DOGE = 12345678 koinu
		if event.AmountKoinu != 12345678 {
			t.Errorf("expected 12345678 koinu, got %d", event.AmountKoinu)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for removed PaymentEvent")
	}

	// Verify we don't get anything on the normal notifications channel
	select {
	case <-watcher.Notifications():
		t.Fatalf("should not receive normal notification when only removed transactions exist")
	case <-time.After(100 * time.Millisecond):
		// Expected; no normal notifications
	}
}

// TestCoreWatcher_StopIdempotency verifies that Stop() can be called multiple times safely.
func TestCoreWatcher_StopIdempotency(t *testing.T) {
	s, settings := testCoreWatcherSetup(t)
	defer s.Close()

	fake := &fakeCoreRPC{
		transactions: map[string]corerpc.ListSinceBlockResult{},
		blockHashes:  map[int64]string{},
	}

	watcher := newCoreWatcherWithFake(s, settings, fake)

	// Call Stop multiple times; should not panic or error
	watcher.Stop()
	watcher.Stop()
	watcher.Stop()
}

// TestCoreWatcher_ContextCancellationDuringPoll verifies that poll() respects context cancellation
// and does not advance the cursor if a send is interrupted by context cancellation.
func TestCoreWatcher_ContextCancellationDuringPoll(t *testing.T) {
	s, settings := testCoreWatcherSetup(t)
	defer s.Close()

	fake := &fakeCoreRPC{
		transactions: map[string]corerpc.ListSinceBlockResult{
			"": {
				Transactions: []corerpc.TransactionInfo{
					{
						Address:  "D1",
						Category: "receive",
						Amount:   1.0,
						TxID:     "tx_1",
						Vout:     0,
					},
				},
				LastBlock: "block_1",
			},
		},
	}

	watcher := newCoreWatcherWithFake(s, settings, fake)
	defer watcher.Stop()

	// Create a context that's already cancelled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Call poll with cancelled context
	// It should return an error (either direct context.Canceled or wrapped)
	err := watcher.poll(ctx)
	if err == nil {
		t.Errorf("expected context-related error, got nil")
	}
	// The error might be wrapped by GetChainCursor, so check if it's context-related
	if !strings.Contains(err.Error(), "context") && err != context.Canceled {
		t.Errorf("expected context-related error, got %v", err)
	}

	// Verify cursor was not advanced
	cursor, _ := settings.GetChainCursor(context.Background())
	if cursor != "" {
		t.Errorf("expected cursor to remain empty after cancellation, got %s", cursor)
	}
}

// TestCoreWatcher_TriggerPollNonBlocking verifies that TriggerPoll doesn't block
// even when the poll channel is full.
func TestCoreWatcher_TriggerPollNonBlocking(t *testing.T) {
	s, settings := testCoreWatcherSetup(t)
	defer s.Close()

	fake := &fakeCoreRPC{
		transactions: map[string]corerpc.ListSinceBlockResult{},
	}

	watcher := newCoreWatcherWithFake(s, settings, fake)
	defer watcher.Stop()

	// Fill the poll channel
	watcher.pollNow <- struct{}{}

	// TriggerPoll should not block even though channel is full
	done := make(chan bool, 1)
	go func() {
		watcher.TriggerPoll()
		done <- true
	}()

	// Should return quickly (no blocking)
	select {
	case <-done:
		// Expected: non-blocking return
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("TriggerPoll blocked (channel full); should be non-blocking")
	}
}

// TestCoreWatcher_RescanWithNilBackend verifies Rescan returns nil for nil backend.
func TestCoreWatcher_RescanWithNilBackend(t *testing.T) {
	s, settings := testCoreWatcherSetup(t)
	defer s.Close()

	// Create watcher with nil backend
	watcher := NewCoreWatcher(nil, settings)
	defer watcher.Stop()

	ctx := context.Background()
	err := watcher.Rescan(ctx, 10)
	if err != nil {
		t.Errorf("expected nil error for nil backend Rescan, got %v", err)
	}
}

// TestCoreWatcher_PollRPCError verifies that poll() returns error when ListSinceBlock fails.
func TestCoreWatcher_PollRPCError(t *testing.T) {
	s, settings := testCoreWatcherSetup(t)
	defer s.Close()

	fake := &fakeCoreRPC{
		transactions:       map[string]corerpc.ListSinceBlockResult{},
		failListSinceBlock: true,
	}

	watcher := newCoreWatcherWithFake(s, settings, fake)
	defer watcher.Stop()

	ctx := context.Background()
	err := watcher.poll(ctx)
	if err == nil {
		t.Errorf("expected error for RPC failure, got nil")
	}
	if !strings.Contains(err.Error(), "simulated RPC error") {
		t.Errorf("expected 'simulated RPC error' in error message, got %v", err)
	}
}

// TestCoreWatcher_WatchIsNoOp verifies that Watch() is a no-op (doesn't error or panic).
func TestCoreWatcher_WatchIsNoOp(t *testing.T) {
	s, settings := testCoreWatcherSetup(t)
	defer s.Close()

	fake := &fakeCoreRPC{
		transactions: map[string]corerpc.ListSinceBlockResult{},
	}

	watcher := newCoreWatcherWithFake(s, settings, fake)
	defer watcher.Stop()

	ctx := context.Background()
	// Watch should be a no-op for polling backend
	err := watcher.Watch(ctx, "D1", "D2", "D3")
	if err != nil {
		t.Errorf("Watch should be no-op, got error %v", err)
	}
}

// newCoreWatcherWithFake creates a CoreWatcher with a fake RPC backend for testing.
// This allows us to test the watcher logic without HTTP overhead.
func newCoreWatcherWithFake(s *store.Store, settings *services.SettingsService, fake fakeListSinceBlocker) *CoreWatcher {
	// Create a watcher and inject the fake backend
	watcher := &CoreWatcher{
		backend:    fake,
		store:      s,
		settings:   settings,
		notifyCh:   make(chan PaymentEvent, 10),
		removedCh:  make(chan PaymentEvent, 10),
		pollNow:    make(chan struct{}, 1),
		pollTicker: time.NewTicker(100 * time.Millisecond), // fast for tests
	}
	return watcher
}
