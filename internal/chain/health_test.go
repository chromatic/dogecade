package chain

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/chromatic/dogecade/internal/chain/corerpc"
	"github.com/chromatic/dogecade/internal/store"
)

// fakeBlockchainInfoGetter is a test double for the GetBlockchainInfo-returning interface.
type fakeBlockchainInfoGetter struct {
	info corerpc.BlockchainInfo
	err  error
}

func (f *fakeBlockchainInfoGetter) GetBlockchainInfo(ctx context.Context) (corerpc.BlockchainInfo, error) {
	return f.info, f.err
}

func TestNodeHealthChecker_NilClient_ReturnsUnconfigured(t *testing.T) {
	ctx := context.Background()
	checker := NewNodeHealthChecker(nil)

	state, err := checker.Check(ctx)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if state != NodeStateUnconfigured {
		t.Errorf("expected state %q, got %q", NodeStateUnconfigured, state)
	}
}

func TestNodeHealthChecker_RPCError_ReturnsUnreachable(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, t.TempDir())
	defer s.Close()

	checker := newTestNodeHealthChecker(t, s, corerpc.BlockchainInfo{}, fmt.Errorf("connection refused"))

	state, err := checker.Check(ctx)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if state != NodeStateUnreachable {
		t.Errorf("expected state %q, got %q", NodeStateUnreachable, state)
	}

	// Verify an unacked alert was created
	alertCount := countUnackedAlerts(t, s, "node_unreachable")
	if alertCount != 1 {
		t.Errorf("expected 1 unacked alert, got %d", alertCount)
	}
}

func TestNodeHealthChecker_Syncing_ReturnsSyncing(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, t.TempDir())
	defer s.Close()

	info := corerpc.BlockchainInfo{
		Chain:                "testnet",
		Blocks:               100,
		Headers:              200,
		InitialBlockDownload: true,
	}
	checker := newTestNodeHealthChecker(t, s, info, nil)

	state, err := checker.Check(ctx)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if state != NodeStateSyncing {
		t.Errorf("expected state %q, got %q", NodeStateSyncing, state)
	}

	// Verify an unacked alert was created
	alertCount := countUnackedAlerts(t, s, "node_syncing")
	if alertCount != 1 {
		t.Errorf("expected 1 unacked alert, got %d", alertCount)
	}
}

func TestNodeHealthChecker_Healthy_ReturnsOk(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, t.TempDir())
	defer s.Close()

	info := corerpc.BlockchainInfo{
		Chain:                "testnet",
		Blocks:               200,
		Headers:              200,
		InitialBlockDownload: false,
	}
	checker := newTestNodeHealthChecker(t, s, info, nil)

	state, err := checker.Check(ctx)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if state != NodeStateOk {
		t.Errorf("expected state %q, got %q", NodeStateOk, state)
	}

	// Verify NO unacked alert was created for ok state
	for _, kind := range []string{"node_ok", "node_unreachable", "node_syncing", "node_unconfigured"} {
		alertCount := countUnackedAlerts(t, s, kind)
		if alertCount != 0 {
			t.Errorf("expected 0 unacked alerts for %q, got %d", kind, alertCount)
		}
	}
}

func TestNodeHealthChecker_NoDoubleInsertAlert_OnRepeatUnhealthy(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, t.TempDir())
	defer s.Close()

	checker := newTestNodeHealthChecker(t, s, corerpc.BlockchainInfo{}, fmt.Errorf("connection refused"))

	// First check should insert alert
	state1, err := checker.Check(ctx)
	if err != nil {
		t.Fatalf("first Check failed: %v", err)
	}
	if state1 != NodeStateUnreachable {
		t.Errorf("first Check: expected state %q, got %q", NodeStateUnreachable, state1)
	}

	alertCount1 := countUnackedAlerts(t, s, "node_unreachable")
	if alertCount1 != 1 {
		t.Errorf("after first Check: expected 1 alert, got %d", alertCount1)
	}

	// Second check with same error should NOT insert another alert
	state2, err := checker.Check(ctx)
	if err != nil {
		t.Fatalf("second Check failed: %v", err)
	}
	if state2 != NodeStateUnreachable {
		t.Errorf("second Check: expected state %q, got %q", NodeStateUnreachable, state2)
	}

	alertCount2 := countUnackedAlerts(t, s, "node_unreachable")
	if alertCount2 != 1 {
		t.Errorf("after second Check: expected 1 alert (no duplicate), got %d", alertCount2)
	}
}

// openTestStore is a helper to open a store with migrations applied.
func openTestStore(t *testing.T, tmpDir string) *store.Store {
	t.Helper()
	dbPath := filepath.Join(tmpDir, "test.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	return s
}

// newTestNodeHealthChecker creates a NodeHealthChecker with a fake backend and test DB for testing.
func newTestNodeHealthChecker(t *testing.T, s *store.Store, info corerpc.BlockchainInfo, err error) *NodeHealthChecker {
	t.Helper()
	fake := &fakeBlockchainInfoGetter{info: info, err: err}
	return &NodeHealthChecker{
		getter: fake,
		db:     s.DB(),
	}
}

// countUnackedAlerts counts unacked alerts of a given kind in the test database.
func countUnackedAlerts(t *testing.T, s *store.Store, kind string) int {
	t.Helper()
	ctx := context.Background()
	var count int
	err := s.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM alerts WHERE kind = ? AND acked_at IS NULL",
		kind,
	).Scan(&count)
	if err != nil {
		t.Fatalf("failed to count alerts: %v", err)
	}
	return count
}

// TestNodeHealthChecker_StateTransition verifies transitions between health states work correctly.
func TestNodeHealthChecker_StateTransition(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, t.TempDir())
	defer s.Close()

	// Simulate state transitions: healthy -> syncing -> healthy
	tests := []struct {
		name     string
		info     corerpc.BlockchainInfo
		err      error
		expected NodeState
	}{
		{
			name: "healthy_node",
			info: corerpc.BlockchainInfo{
				Chain:                "mainnet",
				Blocks:               100,
				Headers:              100,
				InitialBlockDownload: false,
			},
			err:      nil,
			expected: NodeStateOk,
		},
		{
			name: "syncing_node",
			info: corerpc.BlockchainInfo{
				Chain:                "mainnet",
				Blocks:               50,
				Headers:              100,
				InitialBlockDownload: true,
			},
			err:      nil,
			expected: NodeStateSyncing,
		},
		{
			name: "back_to_healthy",
			info: corerpc.BlockchainInfo{
				Chain:                "mainnet",
				Blocks:               100,
				Headers:              100,
				InitialBlockDownload: false,
			},
			err:      nil,
			expected: NodeStateOk,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			checker := newTestNodeHealthChecker(t, s, test.info, test.err)
			state, err := checker.Check(ctx)
			if err != nil {
				t.Errorf("Check failed: %v", err)
			}
			if state != test.expected {
				t.Errorf("expected state %q, got %q", test.expected, state)
			}
		})
	}
}
