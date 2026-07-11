//go:build integration

package corerpc

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"
)

// RegtestNode represents a running regtest node instance.
// It holds the process, RPC client, and cleanup resources.
type RegtestNode struct {
	cmd         *exec.Cmd
	client      *Client
	dataDir     string
	rpcPort     int
	rpcUser     string
	rpcPassword string
	process     *os.Process
}

// Client returns the RPC client connected to this regtest node.
func (rn *RegtestNode) Client() *Client {
	return rn.client
}

// RPCPort returns the RPC port this node is listening on.
func (rn *RegtestNode) RPCPort() int {
	return rn.rpcPort
}

// Stop kills the regtest node process and cleans up resources.
// It is automatically registered with t.Cleanup().
func (rn *RegtestNode) Stop() error {
	if rn.process != nil {
		rn.process.Kill()
		rn.process.Wait()
	}

	// Clean up temporary data directory
	if rn.dataDir != "" {
		os.RemoveAll(rn.dataDir)
	}

	return nil
}

// StartRegtestNode launches a dogecoind -regtest node and waits for RPC readiness.
// If dogecoind is not found on PATH, it calls t.Skip with a clear message.
// The caller must not call Stop() directly; cleanup is registered via t.Cleanup().
// On success, returns a *RegtestNode configured with a ready RPC client.
func StartRegtestNode(t *testing.T) *RegtestNode {
	t.Helper()

	// Locate dogecoind binary
	dogecoindPath, err := findDogecoindBinary()
	if err != nil {
		t.Skipf("dogecoind binary not found on PATH; skipping integration test: %v", err)
		return nil // Unreachable after t.Skip, but satisfies return type
	}

	// Create temporary data directory
	dataDir, err := os.MkdirTemp("", "dogecade-regtest-*")
	if err != nil {
		t.Fatalf("failed to create temp directory: %v", err)
	}

	// Find a free TCP port for RPC
	rpcPort, err := findFreePort()
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}

	rpcUser := "rpcuser"
	rpcPassword := "rpcpass"

	// Prepare command: dogecoind -regtest -datadir=... -rpcuser=... -rpcpassword=... -rpcport=... -daemon=0 -fallbackfee=0.001
	cmd := exec.Command(
		dogecoindPath,
		"-regtest",
		fmt.Sprintf("-datadir=%s", dataDir),
		fmt.Sprintf("-rpcuser=%s", rpcUser),
		fmt.Sprintf("-rpcpassword=%s", rpcPassword),
		fmt.Sprintf("-rpcport=%d", rpcPort),
		"-daemon=0",          // Run in foreground (not daemon mode)
		"-fallbackfee=0.001", // Minimal fallback fee for regtest
	)

	// Capture stdout/stderr for debugging
	cmd.Stderr = io.Discard
	cmd.Stdout = io.Discard

	// Start the process
	if err := cmd.Start(); err != nil {
		os.RemoveAll(dataDir)
		t.Fatalf("failed to start dogecoind: %v", err)
	}

	node := &RegtestNode{
		cmd:         cmd,
		dataDir:     dataDir,
		rpcPort:     rpcPort,
		rpcUser:     rpcUser,
		rpcPassword: rpcPassword,
		process:     cmd.Process,
	}

	// Register cleanup
	t.Cleanup(func() {
		node.Stop()
	})

	// Build RPC URL
	rpcURL := fmt.Sprintf("http://127.0.0.1:%d", rpcPort)

	// Create RPC client
	client, err := NewClient(rpcURL, rpcUser, rpcPassword)
	if err != nil {
		node.Stop()
		t.Fatalf("failed to create RPC client: %v", err)
	}

	node.client = client

	// Wait for RPC to be ready (poll with backoff, timeout 15 seconds)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := waitForRPC(ctx, client); err != nil {
		node.Stop()
		t.Fatalf("RPC did not become ready: %v", err)
	}

	// Create wallet
	walletName := "regtest"
	if err := createWallet(ctx, client, walletName); err != nil {
		node.Stop()
		t.Logf("warning: failed to create wallet (may already exist): %v", err)
		// Don't fail; wallet might already exist on restart scenarios
	}

	// Mine some blocks to establish a chain
	// First, get an address to mine to
	addr, err := client.getNewAddress(ctx)
	if err != nil {
		node.Stop()
		t.Fatalf("failed to get new address: %v", err)
	}

	// Mine 101 blocks (regtest requires 100 confirmations for coinbase)
	if err := client.generateToAddress(ctx, 101, addr); err != nil {
		node.Stop()
		t.Fatalf("failed to mine blocks: %v", err)
	}

	return node
}

// waitForRPC polls the node's RPC endpoint until it responds or context expires.
func waitForRPC(ctx context.Context, client *Client) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("RPC readiness timeout: %w", ctx.Err())
		case <-ticker.C:
			_, err := client.GetBlockchainInfo(ctx)
			if err == nil {
				return nil
			}
			// Continue polling on error
		}
	}
}

// createWallet creates a wallet via RPC (createwallet).
// If the wallet already exists, it may return an error, which is non-fatal.
func createWallet(ctx context.Context, client *Client, walletName string) error {
	// Use the client's internal call method
	// We'll add a helper to the client for wallet creation
	params := []interface{}{walletName}
	var result interface{}
	return client.call(ctx, "createwallet", params, &result)
}

// getNewAddress requests a new address from the node.
func (c *Client) getNewAddress(ctx context.Context) (string, error) {
	var addr string
	if err := c.call(ctx, "getnewaddress", nil, &addr); err != nil {
		return "", err
	}
	return addr, nil
}

// generateToAddress mines blocks to a specific address.
func (c *Client) generateToAddress(ctx context.Context, numBlocks int, address string) error {
	params := []interface{}{numBlocks, address}
	var result interface{}
	return c.call(ctx, "generatetoaddress", params, &result)
}

// findFreePort finds an available TCP port by binding to :0 and reading the assigned port.
func findFreePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()

	addr := listener.Addr().(*net.TCPAddr)
	return addr.Port, nil
}
