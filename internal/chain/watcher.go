package chain

import "context"

// ChainWatcher reports transactions paying watched addresses.
type ChainWatcher interface {
	// Watch adds addresses to the watched set (idempotent, persistent
	// across restarts via re-registration at startup).
	Watch(ctx context.Context, addrs ...string) error
	// Notifications delivers payment events, including confirmation
	// updates for previously seen txids.
	Notifications() <-chan PaymentEvent
	// Rescan requests a re-check from a given block height (admin tool,
	// disaster recovery).
	Rescan(ctx context.Context, fromHeight int64) error
}

// PaymentEvent represents a payment to a watched address.
type PaymentEvent struct {
	Address       string
	TxID          string
	Vout          uint32
	AmountKoinu   int64 // 1 DOGE = 1e8 koinu; integers only
	Confirmations int
	BlockHeight   int64 // 0 while unconfirmed
}
