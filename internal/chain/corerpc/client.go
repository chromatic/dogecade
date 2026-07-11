package corerpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
)

var (
	// ErrNotConfigured is returned when a client is created with an empty RPC URL.
	ErrNotConfigured = errors.New("dogecoin RPC client not configured (empty URL)")
)

// Client is a minimal JSON-RPC 1.0 client for Dogecoin Core.
// It implements the core RPC methods needed for address watching and pool management.
type Client struct {
	url      string
	user     string
	password string
	http     *http.Client
	id       atomic.Int64
}

// NewClient creates a new Dogecoin Core RPC client.
// If url is empty, returns ErrNotConfigured.
// The client uses HTTP Basic Auth with the provided user and password.
func NewClient(url, user, password string) (*Client, error) {
	if url == "" {
		return nil, ErrNotConfigured
	}

	return &Client{
		url:      url,
		user:     user,
		password: password,
		http:     &http.Client{},
	}, nil
}

// BlockchainInfo contains selected fields from getblockchaininfo response.
type BlockchainInfo struct {
	Chain                string  `json:"chain"`
	Blocks               int64   `json:"blocks"`
	Headers              int64   `json:"headers"`
	BestBlockHash        string  `json:"bestblockhash"`
	Difficulty           float64 `json:"difficulty"`
	MedianTime           int64   `json:"mediantime"`
	VerificationProgress float64 `json:"verificationprogress"`
	InitialBlockDownload bool    `json:"initialblockdownload"`
	Chainwork            string  `json:"chainwork"`
	SizeOnDisk           int64   `json:"size_on_disk,omitempty"`
	Prune                bool    `json:"prune,omitempty"`
	TxIndex              bool    `json:"txindex,omitempty"`
	AddressIndex         bool    `json:"addressindex,omitempty"`
	SpentIndex           bool    `json:"spentindex,omitempty"`
}

// ValidateAddressResult contains selected fields from validateaddress response.
type ValidateAddressResult struct {
	IsValid      bool   `json:"isvalid"`
	Address      string `json:"address,omitempty"`
	ScriptPubKey string `json:"scriptPubKey,omitempty"`
	IsMine       bool   `json:"ismine,omitempty"`
	IsWatchOnly  bool   `json:"iswatchonly,omitempty"`
}

// GetBlockchainInfo retrieves blockchain info from the node via getblockchaininfo RPC call.
func (c *Client) GetBlockchainInfo(ctx context.Context) (BlockchainInfo, error) {
	var result BlockchainInfo
	if err := c.call(ctx, "getblockchaininfo", nil, &result); err != nil {
		return BlockchainInfo{}, err
	}
	return result, nil
}

// ImportAddress registers a watch-only address with the node.
// The rescan parameter controls whether the node rescan for historical transactions.
func (c *Client) ImportAddress(ctx context.Context, addr, label string, rescan bool) error {
	params := []interface{}{addr, label, rescan}
	var result interface{}
	return c.call(ctx, "importaddress", params, &result)
}

// ValidateAddress checks if an address is valid according to the node.
func (c *Client) ValidateAddress(ctx context.Context, addr string) (ValidateAddressResult, error) {
	var result ValidateAddressResult
	params := []interface{}{addr}
	if err := c.call(ctx, "validateaddress", params, &result); err != nil {
		return ValidateAddressResult{}, err
	}
	return result, nil
}

// ListSinceBlockResult contains selected fields from listsinceblock response.
type ListSinceBlockResult struct {
	Transactions []TransactionInfo `json:"transactions"`
	Removed      []TransactionInfo `json:"removed,omitempty"`
	LastBlock    string            `json:"lastblock"`
}

// TransactionInfo contains transaction details from listsinceblock response.
type TransactionInfo struct {
	Address       string  `json:"address,omitempty"`
	Category      string  `json:"category"`
	Amount        float64 `json:"amount"`
	Confirmations int     `json:"confirmations"`
	TxID          string  `json:"txid"`
	Vout          uint32  `json:"vout"`
	BlockHash     string  `json:"blockhash,omitempty"`
	BlockHeight   int64   `json:"blockheight,omitempty"`
	BlockIndex    int64   `json:"blockindex,omitempty"` // older Core versions
}

// ListSinceBlock retrieves transactions since the given block hash.
// If blockHash is empty, starts from genesis. targetConfirmations and
// includeWatchOnly control the filtering (see Dogecoin Core RPC docs).
func (c *Client) ListSinceBlock(ctx context.Context, blockHash string, targetConfirmations int, includeWatchOnly bool) (ListSinceBlockResult, error) {
	params := []interface{}{blockHash, targetConfirmations, includeWatchOnly}
	var result ListSinceBlockResult
	if err := c.call(ctx, "listsinceblock", params, &result); err != nil {
		return ListSinceBlockResult{}, err
	}
	return result, nil
}

// GetBlockHash retrieves the block hash at the given height.
func (c *Client) GetBlockHash(ctx context.Context, height int64) (string, error) {
	params := []interface{}{height}
	var result string
	if err := c.call(ctx, "getblockhash", params, &result); err != nil {
		return "", err
	}
	return result, nil
}

// InvalidateBlock invalidates the specified block (for regtest reorg testing).
// This is only useful in regtest mode to simulate blockchain reorganizations.
func (c *Client) InvalidateBlock(ctx context.Context, blockHash string) error {
	params := []interface{}{blockHash}
	var result interface{}
	return c.call(ctx, "invalidateblock", params, &result)
}

// GetNewAddress requests a new address from the node.
func (c *Client) GetNewAddress(ctx context.Context) (string, error) {
	var addr string
	if err := c.call(ctx, "getnewaddress", nil, &addr); err != nil {
		return "", err
	}
	return addr, nil
}

// SendToAddress sends DOGE to the specified address and returns the transaction ID.
func (c *Client) SendToAddress(ctx context.Context, addr string, amount float64) (string, error) {
	params := []interface{}{addr, amount}
	var txID string
	if err := c.call(ctx, "sendtoaddress", params, &txID); err != nil {
		return "", err
	}
	return txID, nil
}

// GenerateToAddress mines blocks to a specific address and returns the hash of the last generated block.
func (c *Client) GenerateToAddress(ctx context.Context, numBlocks int, address string) (string, error) {
	params := []interface{}{numBlocks, address}
	var result []interface{}
	if err := c.call(ctx, "generatetoaddress", params, &result); err != nil {
		return "", err
	}
	// generatetoaddress returns an array of block hashes; return the last one
	if len(result) > 0 {
		if lastHash, ok := result[len(result)-1].(string); ok {
			return lastHash, nil
		}
	}
	return "", nil
}

// call makes a JSON-RPC 1.0 call to the node.
// It sends a request with the given method and params, and unmarshals the result.
// If the RPC response contains an error, it returns that error as a Go error.
func (c *Client) call(ctx context.Context, method string, params interface{}, result interface{}) error {
	reqID := c.id.Add(1)

	// Build the JSON-RPC 1.0 request
	req := jsonRPCRequest{
		JSONRPC: "1.0",
		ID:      reqID,
		Method:  method,
	}

	// Marshal params: if nil, use empty array; otherwise marshal the provided params
	if params == nil {
		req.Params = json.RawMessage([]byte("[]"))
	} else {
		paramsJSON, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("failed to marshal params: %w", err)
		}
		req.Params = json.RawMessage(paramsJSON)
	}

	// Encode request body
	reqBody, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to encode request: %w", err)
	}

	// Create HTTP request with context
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.url, bytes.NewBuffer(reqBody))
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Set headers and basic auth
	httpReq.Header.Set("Content-Type", "application/json")
	if c.user != "" || c.password != "" {
		httpReq.SetBasicAuth(c.user, c.password)
	}

	// Send request
	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("RPC request failed: %w", err)
	}
	defer httpResp.Body.Close()

	// Read response body
	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	// Decode JSON-RPC response
	var resp jsonRPCResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	// Check for JSON-RPC error
	if resp.Error != nil {
		return fmt.Errorf("RPC error %d: %s", resp.Error.Code, resp.Error.Message)
	}

	// Unmarshal result
	if result != nil {
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return fmt.Errorf("failed to unmarshal result: %w", err)
		}
	}

	return nil
}

// jsonRPCRequest represents a JSON-RPC 1.0 request.
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// jsonRPCResponse represents a JSON-RPC 1.0 response.
type jsonRPCResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *jsonRPCError   `json:"error"`
	ID     int64           `json:"id"`
}

// jsonRPCError represents a JSON-RPC error object.
type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
