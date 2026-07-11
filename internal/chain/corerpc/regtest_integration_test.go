//go:build integration

package corerpc

import (
	"context"
	"crypto/sha256"
	"math/big"
	"path/filepath"
	"testing"

	"github.com/chromatic/dogecade/internal/services"
	"github.com/chromatic/dogecade/internal/store"
)

func TestRegtestNode_StartAndStop(t *testing.T) {
	node := StartRegtestNode(t)
	if node == nil {
		t.Skip("dogecoind not available")
	}

	// Verify we got a client
	client := node.Client()
	if client == nil {
		t.Fatal("expected non-nil client")
	}

	// Verify RPC is responsive
	ctx := context.Background()
	info, err := client.GetBlockchainInfo(ctx)
	if err != nil {
		t.Fatalf("failed to call getblockchaininfo: %v", err)
	}

	// Verify we're on regtest
	if info.Chain != "regtest" {
		t.Errorf("expected chain regtest, got %s", info.Chain)
	}

	// Verify we have blocks (should have mined at least 101)
	if info.Blocks < 101 {
		t.Errorf("expected at least 101 blocks, got %d", info.Blocks)
	}
}

func TestAddressBatchImport_Integration(t *testing.T) {
	node := StartRegtestNode(t)
	if node == nil {
		t.Skip("dogecoind not available")
	}

	ctx := context.Background()
	client := node.Client()

	// Create an in-memory SQLite store for this test
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer s.Close()

	// Create address batch service
	svc := services.NewAddressBatchService(s)

	// Generate test addresses (regtest P2PKH format)
	// Regtest uses version byte 0xc4 for P2PKH
	pubkeyHash1 := [20]byte{
		0x62, 0xe9, 0x07, 0xb1, 0x5b, 0xf5, 0xf8, 0xcb, 0x6f, 0x72,
		0x9d, 0x5f, 0x96, 0x4b, 0x66, 0x13, 0x15, 0xf4, 0x72, 0x59,
	}
	pubkeyHash2 := [20]byte{
		0x1e, 0x3a, 0x5c, 0x2d, 0x4f, 0x6b, 0x8a, 0x9c, 0xd1, 0xe2,
		0xf3, 0x4a, 0x5b, 0x6c, 0x7d, 0x8e, 0x9f, 0x10, 0x21, 0x32,
	}

	// Create regtest addresses (version byte 0x71 for regtest P2PKH)
	// Regtest uses testnet version bytes per Dogecoin's chainparams
	addr1 := createRegtestAddress(pubkeyHash1)
	addr2 := createRegtestAddress(pubkeyHash2)
	addrs := []string{addr1, addr2}

	// Import batch via the service with the regtest node as the importer
	batchID, err := svc.ImportBatch(ctx, "integration test batch", addrs, client, "token_deposit")
	if err != nil {
		t.Fatalf("failed to import batch: %v", err)
	}
	if batchID == 0 {
		t.Fatal("expected non-zero batch ID")
	}

	// Verify addresses are registered with the node
	for _, addr := range addrs {
		result, err := client.ValidateAddress(ctx, addr)
		if err != nil {
			t.Fatalf("failed to validate address %s: %v", addr, err)
		}

		if !result.IsValid {
			t.Errorf("expected address %s to be valid, got isvalid=false", addr)
		}

		// After import, address should be watch-only
		if !result.IsWatchOnly {
			t.Errorf("expected address %s to be watch-only, got iswatchonly=false", addr)
		}
	}
}

// Helper function to create regtest addresses
// Regtest uses testnet version bytes: 0x71 for P2PKH
func createRegtestAddress(pubkeyHash [20]byte) string {
	const regtestP2PKHVersionByte = 0x71
	return createTestAddressWithVersion(regtestP2PKHVersionByte, pubkeyHash)
}

// createTestAddressWithVersion creates a Base58Check-encoded address with a specific version byte
func createTestAddressWithVersion(versionByte byte, pubkeyHash [20]byte) string {
	payload := make([]byte, 21)
	payload[0] = versionByte
	copy(payload[1:], pubkeyHash[:])
	return base58CheckEncode(payload)
}

// base58CheckEncode encodes a payload using Base58Check
func base58CheckEncode(payload []byte) string {
	hash1 := sha256.Sum256(payload)
	hash2 := sha256.Sum256(hash1[:])
	checksum := hash2[:4]

	data := append(payload, checksum...)
	return base58Encode(data)
}

// base58Encode encodes data in base58
func base58Encode(data []byte) string {
	const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

	if len(data) == 0 {
		return ""
	}

	leadingZeros := 0
	for _, b := range data {
		if b == 0 {
			leadingZeros++
		} else {
			break
		}
	}

	value := big.NewInt(0)
	for _, b := range data {
		value.Mul(value, big.NewInt(256))
		value.Add(value, big.NewInt(int64(b)))
	}

	base := big.NewInt(58)
	result := []byte{}

	if value.Sign() == 0 && len(data) > 0 {
		for i := 0; i < len(data); i++ {
			result = append(result, '1')
		}
	} else {
		for value.Sign() > 0 {
			mod := big.NewInt(0)
			value.DivMod(value, base, mod)
			result = append(result, base58Alphabet[mod.Int64()])
		}

		for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
			result[i], result[j] = result[j], result[i]
		}

		for i := 0; i < leadingZeros; i++ {
			result = append([]byte{'1'}, result...)
		}
	}

	return string(result)
}
