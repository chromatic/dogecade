package corerpc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewClient_UnconfiguredURL(t *testing.T) {
	// Should return ErrNotConfigured if URL is empty
	client, err := NewClient("", "", "")
	if err != ErrNotConfigured {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
	if client != nil {
		t.Fatalf("expected nil client for unconfigured URL")
	}
}

func TestNewClient_ValidURL(t *testing.T) {
	// Should create a client with valid URL
	client, err := NewClient("http://localhost:8332", "user", "pass")
	if err != nil {
		t.Fatalf("expected no error for valid URL, got %v", err)
	}
	if client == nil {
		t.Fatalf("expected non-nil client")
	}
}

func TestGetBlockchainInfo_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request method and content-type
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read body: %v", err)
		}

		// Parse and verify JSON-RPC request
		var req jsonRPCRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("failed to parse request: %v", err)
		}

		if req.JSONRPC != "1.0" {
			t.Errorf("expected jsonrpc 1.0, got %s", req.JSONRPC)
		}
		if req.Method != "getblockchaininfo" {
			t.Errorf("expected method getblockchaininfo, got %s", req.Method)
		}

		var params []interface{}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatalf("failed to unmarshal params: %v", err)
		}
		if len(params) != 0 {
			t.Errorf("expected no params, got %d", len(params))
		}

		// Return success response
		resp := jsonRPCResponse{
			Result: json.RawMessage([]byte(`{
				"chain": "main",
				"blocks": 12345,
				"headers": 12345,
				"bestblockhash": "abc123",
				"difficulty": 123.45,
				"mediantime": 1609459200,
				"verificationprogress": 0.99,
				"initialblockdownload": false,
				"chainwork": "0000000000000000000000000000000000000000000000000000000000000000"
			}`)),
			Error: nil,
			ID:    req.ID,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "user", "pass")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	info, err := client.GetBlockchainInfo(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if info.Chain != "main" {
		t.Errorf("expected chain main, got %s", info.Chain)
	}
	if info.Blocks != 12345 {
		t.Errorf("expected blocks 12345, got %d", info.Blocks)
	}
	if info.Headers != 12345 {
		t.Errorf("expected headers 12345, got %d", info.Headers)
	}
	if info.InitialBlockDownload != false {
		t.Errorf("expected initialblockdownload false, got %v", info.InitialBlockDownload)
	}
}

func TestImportAddress_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read body: %v", err)
		}

		var req jsonRPCRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("failed to parse request: %v", err)
		}

		if req.Method != "importaddress" {
			t.Errorf("expected method importaddress, got %s", req.Method)
		}

		// Verify params: [addr, label, false]
		var params []interface{}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatalf("failed to unmarshal params: %v", err)
		}

		if len(params) != 3 {
			t.Errorf("expected 3 params, got %d", len(params))
		}

		addr, ok := params[0].(string)
		if !ok {
			t.Fatalf("expected addr to be string")
		}
		label, ok := params[1].(string)
		if !ok {
			t.Fatalf("expected label to be string")
		}
		rescan, ok := params[2].(bool)
		if !ok {
			t.Fatalf("expected rescan to be bool")
		}

		if addr != "DQyP8a9wj7nDzjsoWQWqKVYHGb7kgnvS92" {
			t.Errorf("expected specific address, got %s", addr)
		}
		if label != "test_label" {
			t.Errorf("expected label test_label, got %s", label)
		}
		if rescan != false {
			t.Errorf("expected rescan false, got %v", rescan)
		}

		// Return success: null result with no error
		resp := jsonRPCResponse{
			Result: json.RawMessage([]byte("null")),
			Error:  nil,
			ID:     req.ID,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "user", "pass")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = client.ImportAddress(ctx, "DQyP8a9wj7nDzjsoWQWqKVYHGb7kgnvS92", "test_label", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAddress_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read body: %v", err)
		}

		var req jsonRPCRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("failed to parse request: %v", err)
		}

		if req.Method != "validateaddress" {
			t.Errorf("expected method validateaddress, got %s", req.Method)
		}

		var params []interface{}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatalf("failed to unmarshal params: %v", err)
		}

		if len(params) != 1 {
			t.Errorf("expected 1 param, got %d", len(params))
		}

		_, ok := params[0].(string)
		if !ok {
			t.Fatalf("expected addr to be string")
		}

		// Return success response
		resp := jsonRPCResponse{
			Result: json.RawMessage([]byte(`{
				"isvalid": true,
				"address": "DQyP8a9wj7nDzjsoWQWqKVYHGb7kgnvS92",
				"scriptPubKey": "76a91412345abcd67890ef12345abcd67890ef12345ab88ac",
				"ismine": false,
				"iswatchonly": true
			}`)),
			Error: nil,
			ID:    req.ID,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "user", "pass")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := client.ValidateAddress(ctx, "DQyP8a9wj7nDzjsoWQWqKVYHGb7kgnvS92")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.IsValid {
		t.Errorf("expected isvalid true, got false")
	}
}

func TestValidateAddress_Invalid(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read body: %v", err)
		}

		var req jsonRPCRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("failed to parse request: %v", err)
		}

		// Return response for invalid address
		resp := jsonRPCResponse{
			Result: json.RawMessage([]byte(`{
				"isvalid": false
			}`)),
			Error: nil,
			ID:    req.ID,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "user", "pass")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := client.ValidateAddress(ctx, "invalid")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.IsValid {
		t.Errorf("expected isvalid false, got true")
	}
}

func TestJSONRPCError_Response(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read body: %v", err)
		}

		var req jsonRPCRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("failed to parse request: %v", err)
		}

		// Return JSON-RPC error response
		resp := jsonRPCResponse{
			Result: json.RawMessage([]byte("null")),
			Error: &jsonRPCError{
				Code:    -5,
				Message: "Invalid address",
			},
			ID: req.ID,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "user", "pass")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = client.ValidateAddress(ctx, "invalid")
	if err == nil {
		t.Fatalf("expected error for JSON-RPC error response, got nil")
	}

	if !strings.Contains(err.Error(), "Invalid address") {
		t.Errorf("expected error to contain 'Invalid address', got %v", err)
	}
}

func TestHTTP_ConnectionRefused(t *testing.T) {
	// Create a client pointing to a non-existent server
	client, err := NewClient("http://127.0.0.1:1", "user", "pass")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	_, err = client.GetBlockchainInfo(ctx)
	if err == nil {
		t.Fatalf("expected error for connection refused, got nil")
	}
}

func TestContext_Cancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Delay the response to simulate a slow server
		time.Sleep(500 * time.Millisecond)
		resp := jsonRPCResponse{
			Result: json.RawMessage([]byte("null")),
			Error:  nil,
			ID:     1,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "user", "pass")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	// Create a context that times out quickly
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err2 := client.GetBlockchainInfo(ctx)
	if err2 == nil {
		t.Fatalf("expected error for timeout, got nil")
	}

	if !strings.Contains(err2.Error(), "context") {
		t.Errorf("expected context-related error, got %v", err2)
	}
}

func TestBasicAuth_HeaderSent(t *testing.T) {
	authHeaderReceived := false
	var receivedAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if ok {
			authHeaderReceived = true
			receivedAuth = fmt.Sprintf("%s:%s", username, password)
		}

		body, _ := io.ReadAll(r.Body)
		var req jsonRPCRequest
		_ = json.Unmarshal(body, &req)

		resp := jsonRPCResponse{
			Result: json.RawMessage([]byte("null")),
			Error:  nil,
			ID:     req.ID,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "testuser", "testpass")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx := context.Background()
	_ = client.ImportAddress(ctx, "addr", "label", false)

	if !authHeaderReceived {
		t.Fatalf("expected basic auth header to be sent")
	}

	if receivedAuth != "testuser:testpass" {
		t.Errorf("expected auth 'testuser:testpass', got '%s'", receivedAuth)
	}
}

func TestListSinceBlock_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read body: %v", err)
		}

		var req jsonRPCRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("failed to parse request: %v", err)
		}

		if req.Method != "listsinceblock" {
			t.Errorf("expected method listsinceblock, got %s", req.Method)
		}

		// Return success response with transactions
		resp := jsonRPCResponse{
			Result: json.RawMessage([]byte(`{
				"transactions": [
					{
						"address": "DQyP8a9wj7nDzjsoWQWqKVYHGb7kgnvS92",
						"category": "receive",
						"amount": 1.5,
						"confirmations": 1,
						"txid": "abc123",
						"vout": 0,
						"blockhash": "block_abc",
						"blockheight": 100
					}
				],
				"removed": [],
				"lastblock": "block_abc"
			}`)),
			Error: nil,
			ID:    req.ID,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "user", "pass")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := client.ListSinceBlock(ctx, "", 1, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Transactions) != 1 {
		t.Errorf("expected 1 transaction, got %d", len(result.Transactions))
	}

	tx := result.Transactions[0]
	if tx.Address != "DQyP8a9wj7nDzjsoWQWqKVYHGb7kgnvS92" {
		t.Errorf("expected address DQyP8a9wj7nDzjsoWQWqKVYHGb7kgnvS92, got %s", tx.Address)
	}
	if tx.Amount != 1.5 {
		t.Errorf("expected amount 1.5, got %v", tx.Amount)
	}
	if tx.Category != "receive" {
		t.Errorf("expected category receive, got %s", tx.Category)
	}
	if tx.Confirmations != 1 {
		t.Errorf("expected 1 confirmation, got %d", tx.Confirmations)
	}

	if result.LastBlock != "block_abc" {
		t.Errorf("expected lastblock block_abc, got %s", result.LastBlock)
	}
}

func TestGetBlockHash_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read body: %v", err)
		}

		var req jsonRPCRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("failed to parse request: %v", err)
		}

		if req.Method != "getblockhash" {
			t.Errorf("expected method getblockhash, got %s", req.Method)
		}

		// Verify params: [height]
		var params []interface{}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatalf("failed to unmarshal params: %v", err)
		}

		if len(params) != 1 {
			t.Errorf("expected 1 param, got %d", len(params))
		}

		// Return success response with block hash
		resp := jsonRPCResponse{
			Result: json.RawMessage([]byte(`"abc123def456789"`)),
			Error:  nil,
			ID:     req.ID,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "user", "pass")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	hash, err := client.GetBlockHash(ctx, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if hash != "abc123def456789" {
		t.Errorf("expected hash abc123def456789, got %s", hash)
	}
}

func TestListSinceBlock_WithRemovedTransactions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req jsonRPCRequest
		_ = json.Unmarshal(body, &req)

		// Return response with both normal and removed transactions
		resp := jsonRPCResponse{
			Result: json.RawMessage([]byte(`{
				"transactions": [
					{
						"address": "DAddr1",
						"category": "receive",
						"amount": 1.0,
						"confirmations": 1,
						"txid": "tx1",
						"vout": 0,
						"blockhash": "block1",
						"blockheight": 100
					}
				],
				"removed": [
					{
						"address": "DAddr2",
						"category": "receive",
						"amount": 0.5,
						"confirmations": 0,
						"txid": "tx_removed",
						"vout": 0,
						"blockhash": "",
						"blockheight": 0
					}
				],
				"lastblock": "block1"
			}`)),
			Error: nil,
			ID:    req.ID,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "user", "pass")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := client.ListSinceBlock(ctx, "", 1, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Transactions) != 1 {
		t.Errorf("expected 1 transaction, got %d", len(result.Transactions))
	}

	if len(result.Removed) != 1 {
		t.Errorf("expected 1 removed transaction, got %d", len(result.Removed))
	}

	removed := result.Removed[0]
	if removed.Address != "DAddr2" {
		t.Errorf("expected removed address DAddr2, got %s", removed.Address)
	}
}
