package chain

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/chromatic/dogecade/internal/chain/corerpc"
	"github.com/chromatic/dogecade/internal/services"
	"github.com/chromatic/dogecade/internal/store"
)

// listSinceBlocker is an interface that lets us use both real RPC client and fakes.
type listSinceBlocker interface {
	ListSinceBlock(ctx context.Context, blockHash string, targetConf int, includeWatchOnly bool) (corerpc.ListSinceBlockResult, error)
	GetBlockHash(ctx context.Context, height int64) (string, error)
}

// CoreWatcher implements ChainWatcher using Dogecoin Core RPC with polling.
// It watches for transactions to registered addresses via listsinceblock
// and emits PaymentEvents as transactions are seen and confirmed.
type CoreWatcher struct {
	backend    listSinceBlocker
	store      *store.Store
	settings   *services.SettingsService
	notifyCh   chan PaymentEvent
	removedCh  chan PaymentEvent // Separate channel for reorg-removed transactions
	pollNow    chan struct{}
	pollTicker *time.Ticker
}

// NewCoreWatcher creates a new CoreWatcher backed by Dogecoin Core RPC.
// If client is nil, the watcher will gracefully idle (unconfigured node).
func NewCoreWatcher(client *corerpc.Client, settings *services.SettingsService) *CoreWatcher {
	return &CoreWatcher{
		backend:    client,
		settings:   settings,
		notifyCh:   make(chan PaymentEvent, 10),
		removedCh:  make(chan PaymentEvent, 10),
		pollNow:    make(chan struct{}, 1),
		pollTicker: time.NewTicker(5 * time.Second), // default polling interval
	}
}

// Watch adds addresses to the watched set. For the polling backend, this is a no-op
// since listsinceblock already returns all watch-only transactions. We track the
// addresses for bookkeeping but don't need to register them here (that happens
// at import time via importaddress).
func (w *CoreWatcher) Watch(ctx context.Context, addrs ...string) error {
	// For polling backend, watching is implicit: listsinceblock with watch_only=true
	// returns all transactions to watched addresses regardless. Per-address subscription
	// isn't needed here (that's a ZMQ concern). This method is a no-op.
	// No backend check needed since this is just bookkeeping.
	return nil
}

// Notifications returns a read-only channel that receives PaymentEvents.
func (w *CoreWatcher) Notifications() <-chan PaymentEvent {
	return w.notifyCh
}

// RemovedNotifications returns a read-only channel that receives PaymentEvents
// for transactions that were removed due to reorganization.
// This is a CoreWatcher-specific extension (not part of the ChainWatcher interface).
// Callers can access it via type assertion: if cw, ok := watcher.(*CoreWatcher); ok { ... }
func (w *CoreWatcher) RemovedNotifications() <-chan PaymentEvent {
	return w.removedCh
}

// Rescan requests a re-check from a given block height.
// It translates height to block hash via getblockhash and triggers an immediate poll.
func (w *CoreWatcher) Rescan(ctx context.Context, fromHeight int64) error {
	// Check for nil backend. An interface with a nil concrete value is not the same
	// as a nil interface, so we need to check both the interface and its concrete value.
	if w.backend == nil {
		return nil
	}

	// Check if backend is a nil *corerpc.Client (for real clients)
	if client, ok := w.backend.(*corerpc.Client); ok && client == nil {
		return nil
	}

	// Convert height to block hash using the backend (works for both real and fake)
	blockHash, err := w.backend.GetBlockHash(ctx, fromHeight)
	if err != nil {
		return fmt.Errorf("failed to get block hash for height %d: %w", fromHeight, err)
	}

	// Persist the new cursor
	if err := w.settings.SetChainCursor(ctx, blockHash); err != nil {
		return fmt.Errorf("failed to set chain cursor: %w", err)
	}

	// Trigger an immediate poll
	w.TriggerPoll()

	return nil
}

// TriggerPoll sends a non-blocking request for an immediate poll.
// This is called by ZMQ nudges (via the ZMQNudger) when rawtx/hashblock messages arrive,
// providing low-latency payment notifications as a complement to the RPC polling timer.
// If the poll request channel is full, the poll loop will pick up the new data
// on the next regular timer cycle anyway (RPC polling remains the reliable source of truth).
func (w *CoreWatcher) TriggerPoll() {
	select {
	case w.pollNow <- struct{}{}:
	default:
		// Non-blocking; channel full means polling is already queued or in progress.
	}
}

// Run starts the polling loop. This should be called in a goroutine.
// It polls listsinceblock at regular intervals and emits PaymentEvents.
func (w *CoreWatcher) Run(ctx context.Context) {
	// Check if backend is truly configured
	if w.backend == nil {
		// Unconfigured; idle loop that just waits for context cancellation
		<-ctx.Done()
		return
	}
	if client, ok := w.backend.(*corerpc.Client); ok && client == nil {
		// Nil concrete value; idle loop
		<-ctx.Done()
		return
	}

	// Trigger immediate poll on startup
	if err := w.poll(ctx); err != nil {
		slog.Error("initial poll failed", "err", err)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.pollNow:
			// Immediate poll request (e.g., from Rescan)
			if err := w.poll(ctx); err != nil {
				slog.Error("poll failed", "err", err)
			}
		case <-w.pollTicker.C:
			if err := w.poll(ctx); err != nil {
				slog.Error("poll failed", "err", err)
			}
		}
	}
}

// Stop releases the polling ticker. The poll loop itself is stopped by
// cancelling the context passed to Run, not by calling Stop — closing
// notifyCh here would race with a poll() goroutine concurrently blocked
// sending on it (send-on-closed-channel panic), since Run's only shutdown
// signal is ctx.Done(). Safe to call more than once.
func (w *CoreWatcher) Stop() {
	if w.pollTicker != nil {
		w.pollTicker.Stop()
	}
}

// poll calls listsinceblock with the persisted cursor and emits PaymentEvents.
func (w *CoreWatcher) poll(ctx context.Context) error {
	if w.backend == nil {
		return nil
	}

	// Get persisted cursor
	cursor, err := w.settings.GetChainCursor(ctx)
	if err != nil {
		return fmt.Errorf("failed to get chain cursor: %w", err)
	}

	// Call listsinceblock via the backend interface
	result, err := w.backend.ListSinceBlock(ctx, cursor, 1, true)
	if err != nil {
		return fmt.Errorf("listsinceblock failed: %w", err)
	}

	// Process transactions and emit PaymentEvents for receive category only
	for _, tx := range result.Transactions {
		event, ok := w.txToPaymentEvent(tx)
		if !ok {
			// Non-receive transaction; skip
			continue
		}

		// Send blocks until the consumer reads it or ctx is cancelled. This
		// MUST NOT drop: the cursor advances unconditionally once this poll's
		// batch is processed (see below), so a dropped event here would be
		// lost permanently once listsinceblock stops reporting it from a
		// later cursor. Backpressure (a slow consumer stalls polling) is the
		// correct tradeoff for a payment pipeline over silently losing a
		// deposit notification.
		select {
		case w.notifyCh <- event:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// Process removed (reorged) transactions similarly
	for _, tx := range result.Removed {
		event, ok := w.txToPaymentEvent(tx)
		if !ok {
			// Non-receive transaction; skip
			continue
		}

		// Send on removedCh with same blocking discipline
		select {
		case w.removedCh <- event:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// Persist the new cursor only after successfully processing all transactions
	if result.LastBlock != "" {
		if err := w.settings.SetChainCursor(ctx, result.LastBlock); err != nil {
			return fmt.Errorf("failed to set chain cursor: %w", err)
		}
	}

	return nil
}

// txToPaymentEvent converts a TransactionInfo to a PaymentEvent.
// Returns (event, ok) where ok is false for non-receive transactions.
func (w *CoreWatcher) txToPaymentEvent(tx corerpc.TransactionInfo) (PaymentEvent, bool) {
	if tx.Category != "receive" {
		// Skip non-receive transactions (send, generate, etc.)
		return PaymentEvent{}, false
	}

	// Convert amount to koinu using careful rounding
	amountKoinu := amountToKoinu(tx.Amount)

	event := PaymentEvent{
		Address:       tx.Address,
		TxID:          tx.TxID,
		Vout:          tx.Vout,
		AmountKoinu:   amountKoinu,
		Confirmations: tx.Confirmations,
		BlockHeight:   blockHeight(tx),
	}

	return event, true
}

// amountToKoinu converts a float64 DOGE amount to int64 koinu.
// 1 DOGE = 1e8 koinu. Uses math.Round to avoid float precision issues.
// For example: 0.12345678 DOGE -> 12345678 koinu, 100.0 DOGE -> 10000000000 koinu.
func amountToKoinu(doge float64) int64 {
	return int64(math.Round(doge * 1e8))
}

// blockHeight extracts the block height from a TransactionInfo.
// Returns the BlockHeight if present, otherwise tries BlockIndex (older Core versions),
// and defaults to 0 for unconfirmed transactions.
func blockHeight(tx corerpc.TransactionInfo) int64 {
	if tx.BlockHeight != 0 {
		return tx.BlockHeight
	}
	if tx.BlockIndex != 0 {
		return tx.BlockIndex
	}
	return 0
}
