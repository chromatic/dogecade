package chain

import (
	"context"
	"log/slog"

	"github.com/go-zeromq/zmq4"
)

// ZMQNudger subscribes to ZMQ rawtx/hashblock topics and triggers immediate polls.
// It is optional and best-effort: if the ZMQ endpoint is unreachable or misconfigured,
// it degrades gracefully to "no nudges, rely on the polling timer instead".
// The nudge callback should be non-blocking (e.g., a buffered channel send).
type ZMQNudger struct {
	endpoint string
	onNudge  func()
}

// NewZMQNudger creates a new ZMQ nudger for the given endpoint.
// If endpoint is empty (""), Run will return nil immediately (no-op).
// onNudge is called each time a rawtx or hashblock message is received.
func NewZMQNudger(endpoint string, onNudge func()) *ZMQNudger {
	return &ZMQNudger{
		endpoint: endpoint,
		onNudge:  onNudge,
	}
}

// Run starts the ZMQ subscription loop. It:
//   - Returns nil immediately if endpoint is empty (no-op for unconfigured ZMQ).
//   - Attempts to connect to the ZMQ endpoint and subscribe to rawtx and hashblock.
//   - Calls onNudge() on every received message.
//   - Returns nil (not an error) if the endpoint is unreachable, degrading gracefully
//     to "RPC polling only" (per the design: ZMQ is best-effort, polling is reliable).
//   - Returns an error only for truly exceptional conditions (e.g., context already cancelled).
//   - Respects the context: exits when ctx is cancelled.
//
// Note: If no ZMQ endpoint is configured, the RPC poller (CoreWatcher's ticker)
// is the sole source of payment updates. ZMQ is a low-latency optimization only.
func (z *ZMQNudger) Run(ctx context.Context) error {
	// No-op if endpoint is empty (unconfigured ZMQ).
	if z.endpoint == "" {
		return nil
	}

	// Create a SUB socket.
	sub := zmq4.NewSub(ctx)
	defer sub.Close()

	// Attempt to connect to the ZMQ publisher.
	// If the endpoint is unreachable, this will fail; log the warning
	// and return nil (graceful degradation to polling-only).
	if err := sub.Dial(z.endpoint); err != nil {
		slog.Warn("zmq nudger: failed to connect to endpoint (degrading to polling only)", "endpoint", z.endpoint, "err", err)
		return nil
	}

	// Subscribe to rawtx and hashblock topics.
	if err := sub.SetOption(zmq4.OptionSubscribe, "rawtx"); err != nil {
		slog.Warn("zmq nudger: failed to subscribe to rawtx", "err", err)
		return nil
	}
	if err := sub.SetOption(zmq4.OptionSubscribe, "hashblock"); err != nil {
		slog.Warn("zmq nudger: failed to subscribe to hashblock", "err", err)
		return nil
	}

	// Loop receiving messages until context is cancelled or Recv errors.
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Recv blocks until a message is available or the context is cancelled.
		// Set a socket timeout (via context) to allow periodic checks of ctx.Done().
		_, err := sub.Recv()
		if err != nil {
			// Check if it's a context cancellation (expected shutdown).
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Otherwise, log the error and exit gracefully (ZMQ connection died).
			slog.Debug("zmq nudger: Recv error (closing ZMQ loop)", "err", err)
			return nil
		}

		// Call the nudge callback (should be non-blocking, e.g., buffered channel).
		z.onNudge()
	}
}
