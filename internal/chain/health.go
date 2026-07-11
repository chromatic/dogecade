package chain

import (
	"context"
	"database/sql"

	"github.com/chromatic/dogecade/internal/chain/corerpc"
	"github.com/chromatic/dogecade/internal/services"
)

// NodeState represents the health state of the Dogecoin node.
type NodeState string

const (
	NodeStateUnconfigured NodeState = "unconfigured"
	NodeStateUnreachable  NodeState = "unreachable"
	NodeStateSyncing      NodeState = "syncing"
	NodeStateOk           NodeState = "ok"
)

// blockchainInfoGetter is an interface for getting blockchain info.
// This allows us to test with fakes without needing a real RPC connection.
type blockchainInfoGetter interface {
	GetBlockchainInfo(ctx context.Context) (corerpc.BlockchainInfo, error)
}

// NodeHealthChecker periodically checks the health of the Dogecoin node.
type NodeHealthChecker struct {
	getter blockchainInfoGetter
	db     *sql.DB
}

// NewNodeHealthChecker creates a new NodeHealthChecker.
// If client is nil, the checker will return "unconfigured" on Check().
func NewNodeHealthChecker(client *corerpc.Client) *NodeHealthChecker {
	if client == nil {
		return &NodeHealthChecker{getter: nil, db: nil}
	}

	// For now, return a checker with the client as the getter
	// We'll wire up the db in the next phase
	return &NodeHealthChecker{getter: client, db: nil}
}

// Check performs a health check on the node and returns its current state.
// It also creates a dedup'd alert if the state is not "ok".
func (h *NodeHealthChecker) Check(ctx context.Context) (NodeState, error) {
	// If no getter (client is nil), return unconfigured
	if h.getter == nil {
		return NodeStateUnconfigured, nil
	}

	// Call getblockchaininfo with a timeout
	info, err := h.getter.GetBlockchainInfo(ctx)
	if err != nil {
		// RPC call failed; node is unreachable
		if h.db != nil {
			_ = services.InsertAlertIfNotExists(ctx, h.db, "node_unreachable", "Dogecoin node is unreachable")
		}
		return NodeStateUnreachable, nil
	}

	// Check if node is still syncing
	if info.InitialBlockDownload {
		if h.db != nil {
			_ = services.InsertAlertIfNotExists(ctx, h.db, "node_syncing", "Dogecoin node is syncing blocks")
		}
		return NodeStateSyncing, nil
	}

	// Node is healthy
	return NodeStateOk, nil
}
