package keyring

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math/big"
)

// Keyring replenishes the pool of fresh, never-used receive addresses.
// Assignment itself is a pool operation (SQLite), not a Keyring call.
type Keyring interface {
	// Replenish makes n more addresses available to the pool. The batch
	// backend drains operator-imported addresses; the libdogecoin backend
	// derives them on demand.
	Replenish(ctx context.Context, n int) ([]string, error)
	// ValidateAddress checks if an address is a valid Dogecoin address
	// (mainnet or testnet, P2PKH or P2SH). The context parameter permits
	// future implementations to delegate to a node RPC call, though the
	// pure-Go validation here is entirely offline.
	ValidateAddress(ctx context.Context, addr string) (bool, error)
}

// ValidateAddress validates a Dogecoin address using pure-Go Base58Check decoding.
// It checks:
//   - Valid Base58Check encoding (proper alphabet, valid checksum)
//   - Correct length (25 bytes: 1 version + 20 hash + 4 checksum)
//   - Valid version byte for Dogecoin mainnet/testnet P2PKH/P2SH
//
// Dogecoin version bytes:
//   - Mainnet P2PKH: 0x1e (addresses start with 'D')
//   - Mainnet P2SH:  0x16 (addresses start with 'A' or '9')
//   - Testnet P2PKH: 0x71 (addresses start with 'n' or 'm')
//   - Testnet P2SH:  0xc4 (addresses start with '2')
//
// Regtest uses the same version bytes as testnet per Dogecoin's chainparams.
func ValidateAddress(addr string) (bool, error) {
	if addr == "" {
		return false, nil
	}

	// Decode the address
	decoded, err := base58CheckDecode(addr)
	if err != nil {
		// Invalid Base58Check encoding, checksum mismatch, etc.
		return false, nil
	}

	// Must be exactly 25 bytes: 1 version + 20 hash + 4 checksum
	if len(decoded) != 25 {
		return false, nil
	}

	// Check version byte for valid Dogecoin networks
	versionByte := decoded[0]
	validVersionBytes := map[byte]bool{
		0x1e: true, // mainnet P2PKH
		0x16: true, // mainnet P2SH
		0x71: true, // testnet P2PKH
		0xc4: true, // testnet P2SH
	}

	if !validVersionBytes[versionByte] {
		return false, nil
	}

	return true, nil
}

// base58CheckDecode decodes a Base58Check-encoded string.
// Base58Check format: encode(payload || checksum)
// where checksum = first 4 bytes of SHA256(SHA256(payload))
// Payload for addresses: version (1 byte) + hash (20 bytes)
func base58CheckDecode(encoded string) ([]byte, error) {
	// Decode from Base58
	decoded, err := base58Decode(encoded)
	if err != nil {
		return nil, err
	}

	// Base58Check format requires at least 4 bytes for checksum
	if len(decoded) < 4 {
		return nil, fmt.Errorf("decoded data too short")
	}

	// Split into payload and checksum
	payload := decoded[:len(decoded)-4]
	checksum := decoded[len(decoded)-4:]

	// Verify checksum
	hash1 := sha256.Sum256(payload)
	hash2 := sha256.Sum256(hash1[:])
	expectedChecksum := hash2[:4]

	if !bytesEqual(checksum, expectedChecksum) {
		return nil, fmt.Errorf("checksum mismatch")
	}

	return decoded, nil
}

// base58CheckEncode encodes data using Base58Check encoding.
// This is used internally for testing address generation.
func base58CheckEncode(payload []byte) string {
	// Compute checksum: double SHA256
	hash1 := sha256.Sum256(payload)
	hash2 := sha256.Sum256(hash1[:])
	checksum := hash2[:4]

	// Append checksum and encode
	data := append(payload, checksum...)
	return base58Encode(data)
}

// Base58 alphabet (no 0, O, I, l to avoid confusion)
const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// base58Decode decodes a Base58-encoded string to bytes.
func base58Decode(encoded string) ([]byte, error) {
	// Build a lookup table for Base58 alphabet
	alphabetMap := make(map[rune]byte)
	for i, ch := range base58Alphabet {
		alphabetMap[ch] = byte(i)
	}

	// Count and remove leading '1's (which represent leading zero bytes)
	leadingOnes := 0
	for i, ch := range encoded {
		if ch == '1' {
			leadingOnes++
		} else if i > 0 {
			break
		} else {
			break
		}
	}

	// Convert the non-'1' part to big.Int
	value := big.NewInt(0)
	base := big.NewInt(58)

	for i := leadingOnes; i < len(encoded); i++ {
		ch := rune(encoded[i])
		b, ok := alphabetMap[ch]
		if !ok {
			return nil, fmt.Errorf("invalid base58 character: %c", ch)
		}
		value.Mul(value, base)
		value.Add(value, big.NewInt(int64(b)))
	}

	// Convert big.Int to bytes
	byteSlice := value.Bytes()

	// Prepend leading zero bytes for each leading '1'
	result := make([]byte, leadingOnes+len(byteSlice))
	copy(result[leadingOnes:], byteSlice)

	return result, nil
}

// base58Encode encodes bytes using Base58 encoding.
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

// bytesEqual safely compares two byte slices.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
