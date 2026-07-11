package keyring

import (
	"crypto/sha256"
	"testing"
)

// TestValidateAddress tests pure-Go Dogecoin address validation.
// Test vectors include dynamically-created addresses for mainnet/testnet P2PKH/P2SH,
// and known-bad addresses (bad checksum, wrong length, invalid chars, etc.).
func TestValidateAddress(t *testing.T) {
	// Create valid test addresses using known payloads
	pubkeyHash1 := [20]byte{
		0x62, 0xe9, 0x07, 0xb1, 0x5b, 0xf5, 0xf8, 0xcb, 0x6f, 0x72,
		0x9d, 0x5f, 0x96, 0x4b, 0x66, 0x13, 0x15, 0xf4, 0x72, 0x59,
	}
	pubkeyHash2 := [20]byte{
		0x1e, 0x3a, 0x5c, 0x2d, 0x4f, 0x6b, 0x8a, 0x9c, 0xd1, 0xe2,
		0xf3, 0x4a, 0x5b, 0x6c, 0x7d, 0x8e, 0x9f, 0x10, 0x21, 0x32,
	}

	mainnetP2PKH1 := createTestAddress(0x1e, pubkeyHash1)
	mainnetP2PKH2 := createTestAddress(0x1e, pubkeyHash2)
	mainnetP2SH1 := createTestAddress(0x16, pubkeyHash1)
	mainnetP2SH2 := createTestAddress(0x16, pubkeyHash2)
	testnetP2PKH1 := createTestAddress(0x71, pubkeyHash1)
	testnetP2PKH2 := createTestAddress(0x71, pubkeyHash2)
	testnetP2SH1 := createTestAddress(0xc4, pubkeyHash1)
	testnetP2SH2 := createTestAddress(0xc4, pubkeyHash2)

	tests := []struct {
		name     string
		address  string
		valid    bool
		category string
	}{
		// Valid mainnet P2PKH addresses
		{
			name:     "valid mainnet P2PKH 1",
			address:  mainnetP2PKH1,
			valid:    true,
			category: "valid_mainnet_p2pkh",
		},
		{
			name:     "valid mainnet P2PKH 2",
			address:  mainnetP2PKH2,
			valid:    true,
			category: "valid_mainnet_p2pkh",
		},

		// Valid mainnet P2SH addresses
		{
			name:     "valid mainnet P2SH 1",
			address:  mainnetP2SH1,
			valid:    true,
			category: "valid_mainnet_p2sh",
		},
		{
			name:     "valid mainnet P2SH 2",
			address:  mainnetP2SH2,
			valid:    true,
			category: "valid_mainnet_p2sh",
		},

		// Valid testnet P2PKH addresses
		{
			name:     "valid testnet P2PKH 1",
			address:  testnetP2PKH1,
			valid:    true,
			category: "valid_testnet_p2pkh",
		},
		{
			name:     "valid testnet P2PKH 2",
			address:  testnetP2PKH2,
			valid:    true,
			category: "valid_testnet_p2pkh",
		},

		// Valid testnet P2SH addresses
		{
			name:     "valid testnet P2SH 1",
			address:  testnetP2SH1,
			valid:    true,
			category: "valid_testnet_p2sh",
		},
		{
			name:     "valid testnet P2SH 2",
			address:  testnetP2SH2,
			valid:    true,
			category: "valid_testnet_p2sh",
		},

		// Known-bad addresses - checksum
		{
			name:     "bad checksum",
			address:  mainnetP2PKH1 + "X", // append invalid char
			valid:    false,
			category: "bad_checksum",
		},

		// Known-bad addresses - length
		{
			name:     "wrong length too short",
			address:  mainnetP2PKH1[:len(mainnetP2PKH1)-5],
			valid:    false,
			category: "bad_length",
		},

		// Known-bad addresses - invalid base58 char
		{
			name:     "invalid base58 character",
			address:  "DTdxNiRadfCQjWSj67pAoVozR4TWNhVsI", // 'I' is not in base58 alphabet
			valid:    false,
			category: "bad_base58",
		},

		// Known-bad addresses - empty
		{
			name:     "empty string",
			address:  "",
			valid:    false,
			category: "bad_empty",
		},

		// Known-bad addresses - wrong version byte
		{
			name:     "wrong version byte (Bitcoin mainnet P2PKH)",
			address:  "1A1z7agoat3fSKLw1QixtjyggF61mEBTs", // Bitcoin address, not Dogecoin
			valid:    false,
			category: "bad_version",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateAddress(tt.address)
			if err != nil {
				if tt.valid {
					t.Fatalf("ValidateAddress(%q) unexpected error: %v", tt.address, err)
				}
				return
			}
			if got != tt.valid {
				t.Errorf("ValidateAddress(%q) = %v, want %v", tt.address, got, tt.valid)
			}
		})
	}
}

// TestBase58CheckDecode tests Base58Check decoding with valid and invalid inputs.
func TestBase58CheckDecode(t *testing.T) {
	pubkeyHash := [20]byte{
		0x62, 0xe9, 0x07, 0xb1, 0x5b, 0xf5, 0xf8, 0xcb, 0x6f, 0x72,
		0x9d, 0x5f, 0x96, 0x4b, 0x66, 0x13, 0x15, 0xf4, 0x72, 0x59,
	}
	versionByte := byte(0x1e) // mainnet P2PKH

	// Create the payload (version + hash) - base58CheckEncode will compute the checksum
	payload := make([]byte, 21)
	payload[0] = versionByte
	copy(payload[1:], pubkeyHash[:])

	// Encode it (base58CheckEncode computes the checksum internally)
	encoded := base58CheckEncode(payload)

	// Now decode and verify
	decoded, err := base58CheckDecode(encoded)
	if err != nil {
		t.Fatalf("base58CheckDecode(%q) unexpected error: %v", encoded, err)
	}
	if len(decoded) != 25 {
		t.Errorf("base58CheckDecode returned %d bytes, want 25; encoded=%q decoded=%x", len(decoded), encoded, decoded)
	}
	if decoded[0] != versionByte {
		t.Errorf("version byte = %d, want %d", decoded[0], versionByte)
	}
	if !bytesEqual(decoded[1:21], pubkeyHash[:]) {
		t.Errorf("hash mismatch after decode")
	}

	// Verify checksum is correct
	payloadForCheck := decoded[:21]
	hash1 := sha256.Sum256(payloadForCheck)
	hash2 := sha256.Sum256(hash1[:])
	expectedChecksum := hash2[:4]
	if !bytesEqual(decoded[21:25], expectedChecksum) {
		t.Errorf("checksum mismatch after decode")
	}
}

// TestBase58CheckDecode_InvalidChecksum tests that invalid checksums are rejected.
func TestBase58CheckDecode_InvalidChecksum(t *testing.T) {
	// Create a payload with an incorrect checksum
	versionByte := byte(0x1e)
	pubkeyHash := [20]byte{
		0x62, 0xe9, 0x07, 0xb1, 0x5b, 0xf5, 0xf8, 0xcb, 0x6f, 0x72,
		0x9d, 0x5f, 0x96, 0x4b, 0x66, 0x13, 0x15, 0xf4, 0x72, 0x59,
	}

	payload := make([]byte, 21)
	payload[0] = versionByte
	copy(payload[1:], pubkeyHash[:])

	// Use an invalid checksum (wrong bytes)
	badChecksum := []byte{0xFF, 0xFF, 0xFF, 0xFF}
	encoded := base58Encode(append(payload, badChecksum...))

	_, err := base58CheckDecode(encoded)
	if err == nil {
		t.Errorf("base58CheckDecode should reject invalid checksum, but didn't")
	}
}

// Utility function to create known-good addresses for testing.
// This allows us to generate valid test vectors without hardcoding actual addresses.
func createTestAddress(versionByte byte, pubkeyHash [20]byte) string {
	payload := make([]byte, 21)
	payload[0] = versionByte
	copy(payload[1:], pubkeyHash[:])

	// base58CheckEncode will compute the checksum itself
	return base58CheckEncode(payload)
}

func TestCreateTestAddress(t *testing.T) {
	// Verify that we can create and validate a test address
	pubkeyHash := [20]byte{
		0x62, 0xe9, 0x07, 0xb1, 0x5b, 0xf5, 0xf8, 0xcb, 0x6f, 0x72,
		0x9d, 0x5f, 0x96, 0x4b, 0x66, 0x13, 0x15, 0xf4, 0x72, 0x59,
	}

	// Create mainnet P2PKH address
	addr := createTestAddress(0x1e, pubkeyHash)
	valid, err := ValidateAddress(addr)
	if err != nil {
		t.Fatalf("ValidateAddress returned error: %v", err)
	}
	if !valid {
		t.Errorf("created test address failed validation: %q", addr)
	}

	// Create mainnet P2SH address
	addr = createTestAddress(0x16, pubkeyHash)
	valid, err = ValidateAddress(addr)
	if err != nil {
		t.Fatalf("ValidateAddress returned error: %v", err)
	}
	if !valid {
		t.Errorf("created test P2SH address failed validation: %q", addr)
	}

	// Create testnet P2PKH address
	addr = createTestAddress(0x71, pubkeyHash)
	valid, err = ValidateAddress(addr)
	if err != nil {
		t.Fatalf("ValidateAddress returned error: %v", err)
	}
	if !valid {
		t.Errorf("created test testnet P2PKH address failed validation: %q", addr)
	}

	// Create testnet P2SH address
	addr = createTestAddress(0xc4, pubkeyHash)
	valid, err = ValidateAddress(addr)
	if err != nil {
		t.Fatalf("ValidateAddress returned error: %v", err)
	}
	if !valid {
		t.Errorf("created test testnet P2SH address failed validation: %q", addr)
	}
}
