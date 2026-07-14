package services

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"math/big"
	"path/filepath"
	"testing"

	"github.com/chromatic/dogecade/internal/store"
)

// mockNodeImporter is a test fake that implements NodeImporter for testing.
type mockNodeImporter struct {
	// Map of address -> error; if key is missing, call succeeds
	failures map[string]error
	calls    []string // Track all ImportAddress calls for verification
}

func (m *mockNodeImporter) ImportAddress(ctx context.Context, addr, label string, rescan bool) error {
	m.calls = append(m.calls, addr)
	if err, ok := m.failures[addr]; ok {
		return err
	}
	return nil
}

func TestImportBatchValidAddresses(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer func() { _ = s.Close() }()

	svc := NewAddressBatchService(s)

	// Valid Dogecoin addresses (mainnet P2PKH)
	pubkeyHash1 := [20]byte{
		0x62, 0xe9, 0x07, 0xb1, 0x5b, 0xf5, 0xf8, 0xcb, 0x6f, 0x72,
		0x9d, 0x5f, 0x96, 0x4b, 0x66, 0x13, 0x15, 0xf4, 0x72, 0x59,
	}
	pubkeyHash2 := [20]byte{
		0x1e, 0x3a, 0x5c, 0x2d, 0x4f, 0x6b, 0x8a, 0x9c, 0xd1, 0xe2,
		0xf3, 0x4a, 0x5b, 0x6c, 0x7d, 0x8e, 0x9f, 0x10, 0x21, 0x32,
	}
	addrs := []string{
		createTestAddress(0x1e, pubkeyHash1), // mainnet P2PKH
		createTestAddress(0x1e, pubkeyHash2), // mainnet P2PKH
	}

	// Import with nil node (no registration)
	batchID, err := svc.ImportBatch(ctx, "test batch", addrs, nil, "token_deposit")
	if err != nil {
		t.Fatalf("ImportBatch failed: %v", err)
	}
	if batchID == 0 {
		t.Fatal("expected non-zero batch ID")
	}

	// Verify batch row was created
	var batchCount int
	err = s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM address_batches WHERE id = ?", batchID).Scan(&batchCount)
	if err != nil {
		t.Fatalf("failed to query batch: %v", err)
	}
	if batchCount != 1 {
		t.Fatalf("expected 1 batch row, got %d", batchCount)
	}

	// Verify address rows were created with correct state and purpose
	var count int
	var state string
	var purpose string
	var registered sql.NullString

	err = s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM addresses WHERE batch_id = ?", batchID).Scan(&count)
	if err != nil {
		t.Fatalf("failed to count addresses: %v", err)
	}
	if count != len(addrs) {
		t.Fatalf("expected %d addresses, got %d", len(addrs), count)
	}

	// Check each address has correct state, purpose, and node_registered_at=NULL
	for _, addr := range addrs {
		err := s.DB().QueryRowContext(ctx,
			"SELECT state, purpose, node_registered_at FROM addresses WHERE address = ?",
			addr,
		).Scan(&state, &purpose, &registered)
		if err != nil {
			t.Fatalf("failed to query address %s: %v", addr, err)
		}
		if state != "pool" {
			t.Errorf("expected state 'pool', got %q", state)
		}
		if purpose != "token_deposit" {
			t.Errorf("expected purpose 'token_deposit', got %q", purpose)
		}
		if registered.Valid {
			t.Errorf("expected node_registered_at=NULL, got %q", registered.String)
		}
	}
}

func TestImportBatchMachineDirectPurpose(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	svc := NewAddressBatchService(s)

	pubkeyHash := [20]byte{
		0x62, 0xe9, 0x07, 0xb1, 0x5b, 0xf5, 0xf8, 0xcb, 0x6f, 0x72,
		0x9d, 0x5f, 0x96, 0x4b, 0x66, 0x13, 0x15, 0xf4, 0x72, 0x59,
	}
	addr := createTestAddress(0x1e, pubkeyHash)

	batchID, err := svc.ImportBatch(ctx, "direct-pay batch", []string{addr}, nil, "machine_direct")
	if err != nil {
		t.Fatalf("ImportBatch failed: %v", err)
	}
	if batchID == 0 {
		t.Fatal("expected non-zero batch ID")
	}

	var purpose string
	if err := s.DB().QueryRowContext(ctx, "SELECT purpose FROM addresses WHERE address = ?", addr).Scan(&purpose); err != nil {
		t.Fatalf("failed to query address: %v", err)
	}
	if purpose != "machine_direct" {
		t.Errorf("expected purpose 'machine_direct', got %q", purpose)
	}
}

func TestImportBatchRejectsInvalidPurpose(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	svc := NewAddressBatchService(s)

	pubkeyHash := [20]byte{
		0x62, 0xe9, 0x07, 0xb1, 0x5b, 0xf5, 0xf8, 0xcb, 0x6f, 0x72,
		0x9d, 0x5f, 0x96, 0x4b, 0x66, 0x13, 0x15, 0xf4, 0x72, 0x59,
	}
	addr := createTestAddress(0x1e, pubkeyHash)

	if _, err := svc.ImportBatch(ctx, "bad purpose batch", []string{addr}, nil, "not_a_real_purpose"); err == nil {
		t.Fatal("expected an error for an invalid purpose")
	}
}

func TestImportBatchWithMockNode(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer func() { _ = s.Close() }()

	svc := NewAddressBatchService(s)

	pubkeyHash1 := [20]byte{
		0x62, 0xe9, 0x07, 0xb1, 0x5b, 0xf5, 0xf8, 0xcb, 0x6f, 0x72,
		0x9d, 0x5f, 0x96, 0x4b, 0x66, 0x13, 0x15, 0xf4, 0x72, 0x59,
	}
	pubkeyHash2 := [20]byte{
		0x1e, 0x3a, 0x5c, 0x2d, 0x4f, 0x6b, 0x8a, 0x9c, 0xd1, 0xe2,
		0xf3, 0x4a, 0x5b, 0x6c, 0x7d, 0x8e, 0x9f, 0x10, 0x21, 0x32,
	}
	addrs := []string{
		createTestAddress(0x1e, pubkeyHash1), // mainnet P2PKH
		createTestAddress(0x1e, pubkeyHash2), // mainnet P2PKH
	}

	mockNode := &mockNodeImporter{failures: make(map[string]error)}

	batchID, err := svc.ImportBatch(ctx, "test with node", addrs, mockNode, "token_deposit")
	if err != nil {
		t.Fatalf("ImportBatch failed: %v", err)
	}

	// Verify node registration was attempted for all addresses
	if len(mockNode.calls) != len(addrs) {
		t.Fatalf("expected %d node calls, got %d", len(addrs), len(mockNode.calls))
	}

	// Verify node_registered_at was set for all addresses
	for _, addr := range addrs {
		var registered sql.NullString
		err := s.DB().QueryRowContext(ctx,
			"SELECT node_registered_at FROM addresses WHERE address = ? AND batch_id = ?",
			addr, batchID,
		).Scan(&registered)
		if err != nil {
			t.Fatalf("failed to query address %s: %v", addr, err)
		}
		if !registered.Valid || registered.String == "" {
			t.Errorf("expected node_registered_at to be set for %s", addr)
		}
	}
}

func TestImportBatchWithNodeRegistrationFailure(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer func() { _ = s.Close() }()

	svc := NewAddressBatchService(s)

	pubkeyHash1 := [20]byte{
		0x62, 0xe9, 0x07, 0xb1, 0x5b, 0xf5, 0xf8, 0xcb, 0x6f, 0x72,
		0x9d, 0x5f, 0x96, 0x4b, 0x66, 0x13, 0x15, 0xf4, 0x72, 0x59,
	}
	pubkeyHash2 := [20]byte{
		0x1e, 0x3a, 0x5c, 0x2d, 0x4f, 0x6b, 0x8a, 0x9c, 0xd1, 0xe2,
		0xf3, 0x4a, 0x5b, 0x6c, 0x7d, 0x8e, 0x9f, 0x10, 0x21, 0x32,
	}
	addr1 := createTestAddress(0x1e, pubkeyHash1)
	addr2 := createTestAddress(0x1e, pubkeyHash2)
	addrs := []string{addr1, addr2}

	mockNode := &mockNodeImporter{
		failures: map[string]error{
			addr2: errors.New("node error: address already imported"),
		},
	}

	// Import should succeed (DB transaction commits despite node failures)
	batchID, err := svc.ImportBatch(ctx, "partial node failure", addrs, mockNode, "token_deposit")
	if err != nil {
		t.Fatalf("ImportBatch failed: %v", err)
	}

	// Verify both addresses exist in DB
	var count int
	err = s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM addresses WHERE batch_id = ?", batchID).Scan(&count)
	if err != nil {
		t.Fatalf("failed to count addresses: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 addresses, got %d", count)
	}

	// Verify addr1 has node_registered_at set (registration succeeded)
	var registered1 sql.NullString
	err = s.DB().QueryRowContext(ctx,
		"SELECT node_registered_at FROM addresses WHERE address = ? AND batch_id = ?",
		addr1, batchID,
	).Scan(&registered1)
	if err != nil {
		t.Fatalf("failed to query addr1: %v", err)
	}
	if !registered1.Valid || registered1.String == "" {
		t.Errorf("expected node_registered_at to be set for addr1")
	}

	// Verify addr2 has node_registered_at NULL (registration failed)
	var registered2 sql.NullString
	err = s.DB().QueryRowContext(ctx,
		"SELECT node_registered_at FROM addresses WHERE address = ? AND batch_id = ?",
		addr2, batchID,
	).Scan(&registered2)
	if err != nil {
		t.Fatalf("failed to query addr2: %v", err)
	}
	if registered2.Valid {
		t.Errorf("expected node_registered_at=NULL for addr2 after registration failure")
	}
}

func TestImportBatchInvalidAddress(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer func() { _ = s.Close() }()

	svc := NewAddressBatchService(s)

	addrs := []string{
		"DHcsSoXQwqg9w6gJ8V8UqLBbBMsqPJz79F",
		"invalid_address", // Invalid
		"DFJku9qifo5JJizc4ux5TrRcwy5LCSt3He",
	}

	// Import should fail with all-or-nothing semantics
	_, importErr := svc.ImportBatch(ctx, "batch with invalid", addrs, nil, "token_deposit")
	if importErr == nil {
		t.Fatal("expected ImportBatch to fail on invalid address")
	}

	// Verify no batch was created
	var batchCount int
	err = s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM address_batches").Scan(&batchCount)
	if err != nil {
		t.Fatalf("failed to count batches: %v", err)
	}
	if batchCount != 0 {
		t.Fatalf("expected 0 batches (all-or-nothing), got %d", batchCount)
	}

	// Verify no addresses were inserted
	var addrCount int
	err = s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM addresses").Scan(&addrCount)
	if err != nil {
		t.Fatalf("failed to count addresses: %v", err)
	}
	if addrCount != 0 {
		t.Fatalf("expected 0 addresses (all-or-nothing), got %d", addrCount)
	}
}

func TestImportBatchDuplicateInBatch(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer func() { _ = s.Close() }()

	svc := NewAddressBatchService(s)

	pubkeyHash := [20]byte{
		0x62, 0xe9, 0x07, 0xb1, 0x5b, 0xf5, 0xf8, 0xcb, 0x6f, 0x72,
		0x9d, 0x5f, 0x96, 0x4b, 0x66, 0x13, 0x15, 0xf4, 0x72, 0x59,
	}
	addr := createTestAddress(0x1e, pubkeyHash)
	addrs := []string{addr, addr} // Duplicate

	_, importErr := svc.ImportBatch(ctx, "batch with duplicate", addrs, nil, "token_deposit")
	if importErr == nil {
		t.Fatal("expected ImportBatch to fail on duplicate address in batch")
	}

	// Verify no batch was created
	var batchCount int
	err = s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM address_batches").Scan(&batchCount)
	if err != nil {
		t.Fatalf("failed to count batches: %v", err)
	}
	if batchCount != 0 {
		t.Fatalf("expected 0 batches (all-or-nothing), got %d", batchCount)
	}
}

func TestImportBatchAddressAlreadyExists(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer func() { _ = s.Close() }()

	svc := NewAddressBatchService(s)

	pubkeyHash := [20]byte{
		0x62, 0xe9, 0x07, 0xb1, 0x5b, 0xf5, 0xf8, 0xcb, 0x6f, 0x72,
		0x9d, 0x5f, 0x96, 0x4b, 0x66, 0x13, 0x15, 0xf4, 0x72, 0x59,
	}
	addr := createTestAddress(0x1e, pubkeyHash)

	// First import succeeds
	_, err = svc.ImportBatch(ctx, "first batch", []string{addr}, nil, "token_deposit")
	if err != nil {
		t.Fatalf("first import failed: %v", err)
	}

	// Second import with the same address should fail
	_, importErr := svc.ImportBatch(ctx, "second batch", []string{addr}, nil, "token_deposit")
	if importErr == nil {
		t.Fatal("expected ImportBatch to fail on duplicate address from prior batch")
	}

	// Verify only one batch was created
	var batchCount int
	err2 := s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM address_batches").Scan(&batchCount)
	if err2 != nil {
		t.Fatalf("failed to count batches: %v", err2)
	}
	if err != nil {
		t.Fatalf("failed to count batches: %v", err)
	}
	if batchCount != 1 {
		t.Fatalf("expected 1 batch, got %d", batchCount)
	}

	// Verify only one address exists
	var addrCount int
	err = s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM addresses").Scan(&addrCount)
	if err != nil {
		t.Fatalf("failed to count addresses: %v", err)
	}
	if addrCount != 1 {
		t.Fatalf("expected 1 address, got %d", addrCount)
	}
}

func TestImportBatchCLI(t *testing.T) {
	// Test the CLI command via subprocess would go here
	// For now, we rely on integration/smoke testing to verify the CLI works
	t.Skip("CLI integration test covered by manual smoke testing")
}

// Helper functions for creating valid test addresses

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func base58CheckEncode(payload []byte) string {
	// Compute checksum: double SHA256
	hash1 := sha256.Sum256(payload)
	hash2 := sha256.Sum256(hash1[:])
	checksum := hash2[:4]

	// Append checksum and encode
	data := append(payload, checksum...)
	return base58Encode(data)
}

func base58Encode(data []byte) string {
	if len(data) == 0 {
		return ""
	}

	// Count leading zero bytes
	leadingZeros := 0
	for _, b := range data {
		if b == 0 {
			leadingZeros++
		} else {
			break
		}
	}

	// Convert bytes to big.Int
	value := big.NewInt(0)
	for _, b := range data {
		value.Mul(value, big.NewInt(256))
		value.Add(value, big.NewInt(int64(b)))
	}

	// Convert big.Int to base58
	base := big.NewInt(58)
	result := []byte{}

	// Handle the case where value is zero
	if value.Sign() == 0 && len(data) > 0 {
		// If input is all zeros, result should be all '1's
		for i := 0; i < len(data); i++ {
			result = append(result, '1')
		}
	} else {
		for value.Sign() > 0 {
			mod := big.NewInt(0)
			value.DivMod(value, base, mod)
			result = append(result, base58Alphabet[mod.Int64()])
		}

		// Reverse result (built backwards)
		for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
			result[i], result[j] = result[j], result[i]
		}

		// Prepend '1' for each leading zero byte
		for i := 0; i < leadingZeros; i++ {
			result = append([]byte{'1'}, result...)
		}
	}

	return string(result)
}

func createTestAddress(versionByte byte, pubkeyHash [20]byte) string {
	payload := make([]byte, 21)
	payload[0] = versionByte
	copy(payload[1:], pubkeyHash[:])

	// base58CheckEncode will compute the checksum itself
	return base58CheckEncode(payload)
}
