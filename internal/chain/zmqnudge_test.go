package chain

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-zeromq/zmq4"
)

// TestZMQNudger_EmptyEndpointReturnsNil tests that an empty endpoint is a no-op.
func TestZMQNudger_EmptyEndpointReturnsNil(t *testing.T) {
	nudgeCalled := false
	onNudge := func() {
		nudgeCalled = true
	}

	nudger := NewZMQNudger("", onNudge)

	// Create a context with a short timeout to ensure Run doesn't block.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := nudger.Run(ctx)
	if err != nil {
		t.Errorf("expected nil error for empty endpoint, got %v", err)
	}

	// Give a bit of time to ensure no concurrent nudge happens.
	time.Sleep(50 * time.Millisecond)

	if nudgeCalled {
		t.Errorf("expected onNudge not to be called for empty endpoint")
	}
}

// TestZMQNudger_UnreachableEndpointReturnsNilGracefully tests that an unreachable
// endpoint degrades gracefully to a nil return (not an error).
func TestZMQNudger_UnreachableEndpointReturnsNilGracefully(t *testing.T) {
	nudgeCalled := false
	onNudge := func() {
		nudgeCalled = true
	}

	// Use a port that's unlikely to have a ZMQ publisher.
	nudger := NewZMQNudger("tcp://127.0.0.1:1", onNudge)

	// Create a context with a reasonable timeout; Dial should fail quickly.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := nudger.Run(ctx)
	// Should return nil (graceful degradation), not an error.
	if err != nil {
		t.Errorf("expected nil error for unreachable endpoint (graceful degradation), got %v", err)
	}

	if nudgeCalled {
		t.Errorf("expected onNudge not to be called for unreachable endpoint")
	}
}

// TestZMQNudger_LocalPubSubIntegration tests against a real local ZMQ pub/sub pair
// if socket binding is permitted in this sandbox.
func TestZMQNudger_LocalPubSubIntegration(t *testing.T) {
	// Try to create a local publisher to test against.
	// This may fail in sandboxes with restricted socket permissions.
	pub := zmq4.NewPub(context.Background())
	defer func() { _ = pub.Close() }()

	// Bind to a random port (127.0.0.1:0 lets the OS choose).
	endpoint := "tcp://127.0.0.1:0"
	err := pub.Listen(endpoint)
	if err != nil {
		t.Skip("ZMQ Listen failed (likely sandbox restriction); skipping integration test")
	}

	// Retrieve the actual bound endpoint.
	boundAddr := pub.Addr()
	if boundAddr == nil {
		t.Skip("ZMQ did not bind to an address; skipping integration test")
	}

	// Convert net.Addr to endpoint string (tcp://127.0.0.1:port)
	boundEndpoint := "tcp://" + boundAddr.String()

	// Track nudge calls.
	nudgeChan := make(chan struct{}, 10)
	onNudge := func() {
		select {
		case nudgeChan <- struct{}{}:
		default:
			// Buffer full, test will catch this
		}
	}

	nudger := NewZMQNudger(boundEndpoint, onNudge)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Run the nudger in a goroutine.
	errChan := make(chan error, 1)
	go func() {
		errChan <- nudger.Run(ctx)
	}()

	// Give the nudger time to connect.
	time.Sleep(200 * time.Millisecond)

	// Publish a rawtx message.
	err = pub.Send(zmq4.NewMsg([]byte("rawtx")))
	if err != nil {
		t.Fatalf("failed to send rawtx message: %v", err)
	}

	// Should receive a nudge within a reasonable time.
	select {
	case <-nudgeChan:
		// Expected: nudge received.
	case <-time.After(1 * time.Second):
		t.Fatalf("timeout waiting for nudge after rawtx publish")
	}

	// Publish a hashblock message.
	err = pub.Send(zmq4.NewMsg([]byte("hashblock")))
	if err != nil {
		t.Fatalf("failed to send hashblock message: %v", err)
	}

	// Should receive another nudge.
	select {
	case <-nudgeChan:
		// Expected: nudge received.
	case <-time.After(1 * time.Second):
		t.Fatalf("timeout waiting for nudge after hashblock publish")
	}

	// Cancel the context to stop the nudger.
	cancel()

	// Verify Run returns without error (context cancellation is expected).
	select {
	case err := <-errChan:
		if err != nil && err != context.Canceled {
			t.Errorf("unexpected error from Run: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("timeout waiting for Run to exit after context cancellation")
	}
}

// TestZMQNudger_ContextCancellation tests that Run exits cleanly on context cancellation.
func TestZMQNudger_ContextCancellation(t *testing.T) {
	onNudge := func() {}

	nudger := NewZMQNudger("tcp://127.0.0.1:29999", onNudge)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// Run should eventually exit (either due to dial failure or timeout).
	err := nudger.Run(ctx)
	if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
		t.Logf("Run returned error (expected for unreachable endpoint): %v", err)
	}
}

// TestZMQNudger_MultipleMessages verifies that consecutive nudges are all received.
func TestZMQNudger_MultipleMessages(t *testing.T) {
	// Try to create a local publisher to test against.
	pub := zmq4.NewPub(context.Background())
	defer func() { _ = pub.Close() }()

	// Bind to a random port (127.0.0.1:0 lets the OS choose).
	endpoint := "tcp://127.0.0.1:0"
	err := pub.Listen(endpoint)
	if err != nil {
		t.Skip("ZMQ Listen failed (likely sandbox restriction); skipping integration test")
	}

	// Retrieve the actual bound endpoint.
	boundAddr := pub.Addr()
	if boundAddr == nil {
		t.Skip("ZMQ did not bind to an address; skipping integration test")
	}

	boundEndpoint := "tcp://" + boundAddr.String()

	// Track nudge calls with an atomic counter: onNudge runs on the Run()
	// goroutine while the test goroutine reads the count concurrently.
	var nudgeCount atomic.Int64
	onNudge := func() {
		nudgeCount.Add(1)
	}

	nudger := NewZMQNudger(boundEndpoint, onNudge)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Run the nudger in a goroutine.
	errChan := make(chan error, 1)
	go func() {
		errChan <- nudger.Run(ctx)
	}()

	// Give the nudger time to connect.
	time.Sleep(200 * time.Millisecond)

	// Send multiple messages
	for i := 0; i < 3; i++ {
		err = pub.Send(zmq4.NewMsg([]byte("rawtx")))
		if err != nil {
			t.Fatalf("failed to send rawtx message: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Wait for context to expire
	<-ctx.Done()

	// Verify we received at least 3 nudges
	if got := nudgeCount.Load(); got < 3 {
		t.Errorf("expected at least 3 nudges, got %d", got)
	}
}
