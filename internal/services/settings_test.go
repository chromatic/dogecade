package services

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/chromatic/dogecade/internal/config"
	"github.com/chromatic/dogecade/internal/store"
)

func TestGetMinConfirmationsDefault(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() failed: %v", err)
	}
	defer func() { _ = s.Close() }()

	svc := NewSettingsService(s)
	ctx := context.Background()

	value, err := svc.GetMinConfirmations(ctx)
	if err != nil {
		t.Fatalf("GetMinConfirmations() failed: %v", err)
	}
	if value != 1 {
		t.Errorf("Expected default min_confirmations=1, got %d", value)
	}
}

func TestSetAndGetMinConfirmations(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() failed: %v", err)
	}
	defer func() { _ = s.Close() }()

	svc := NewSettingsService(s)
	ctx := context.Background()

	// Set a new value
	if err := svc.SetMinConfirmations(ctx, 3); err != nil {
		t.Fatalf("SetMinConfirmations() failed: %v", err)
	}

	// Verify it was set
	value, err := svc.GetMinConfirmations(ctx)
	if err != nil {
		t.Fatalf("GetMinConfirmations() after set failed: %v", err)
	}
	if value != 3 {
		t.Errorf("Expected min_confirmations=3 after set, got %d", value)
	}
}

func TestGetZeroConfMaxKoinuDefault(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() failed: %v", err)
	}
	defer func() { _ = s.Close() }()

	svc := NewSettingsService(s)
	ctx := context.Background()

	value, err := svc.GetZeroConfMaxKoinu(ctx)
	if err != nil {
		t.Fatalf("GetZeroConfMaxKoinu() failed: %v", err)
	}
	if value != 0 {
		t.Errorf("Expected default zero_conf_max_koinu=0, got %d", value)
	}
}

func TestSetAndGetZeroConfMaxKoinu(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() failed: %v", err)
	}
	defer func() { _ = s.Close() }()

	svc := NewSettingsService(s)
	ctx := context.Background()

	// Set a new value
	expectedValue := int64(100000)
	if err := svc.SetZeroConfMaxKoinu(ctx, expectedValue); err != nil {
		t.Fatalf("SetZeroConfMaxKoinu() failed: %v", err)
	}

	// Verify it was set
	value, err := svc.GetZeroConfMaxKoinu(ctx)
	if err != nil {
		t.Fatalf("GetZeroConfMaxKoinu() after set failed: %v", err)
	}
	if value != expectedValue {
		t.Errorf("Expected zero_conf_max_koinu=%d after set, got %d", expectedValue, value)
	}
}

func TestSettingsPersistAfterReopen(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	// First session: set values
	s1, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() failed: %v", err)
	}

	svc1 := NewSettingsService(s1)
	ctx := context.Background()

	if err := svc1.SetMinConfirmations(ctx, 5); err != nil {
		t.Fatalf("SetMinConfirmations() failed: %v", err)
	}
	if err := svc1.SetZeroConfMaxKoinu(ctx, 250000); err != nil {
		t.Fatalf("SetZeroConfMaxKoinu() failed: %v", err)
	}

	if err := s1.Close(); err != nil {
		t.Fatalf("s1.Close() failed: %v", err)
	}

	// Second session: verify values persisted
	s2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() after reopen failed: %v", err)
	}
	defer func() { _ = s2.Close() }()

	svc2 := NewSettingsService(s2)

	minConf, err := svc2.GetMinConfirmations(ctx)
	if err != nil {
		t.Fatalf("GetMinConfirmations() after reopen failed: %v", err)
	}
	if minConf != 5 {
		t.Errorf("Expected persisted min_confirmations=5, got %d", minConf)
	}

	maxKoinu, err := svc2.GetZeroConfMaxKoinu(ctx)
	if err != nil {
		t.Fatalf("GetZeroConfMaxKoinu() after reopen failed: %v", err)
	}
	if maxKoinu != 250000 {
		t.Errorf("Expected persisted zero_conf_max_koinu=250000, got %d", maxKoinu)
	}
}

func TestSeedFromEnvNoEnvironmentVariables(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() failed: %v", err)
	}
	defer func() { _ = s.Close() }()

	svc := NewSettingsService(s)
	ctx := context.Background()

	// Create a minimal config with no node/admin config
	cfg := config.Config{
		DBPath:     dbPath,
		BaseURL:    "http://localhost:8080",
		ListenAddr: ":8080",
	}

	// SeedFromEnv should not fail even with empty env vars
	if err := svc.SeedFromEnv(ctx, cfg); err != nil {
		t.Fatalf("SeedFromEnv() failed: %v", err)
	}

	// Settings should still have defaults
	minConf, err := svc.GetMinConfirmations(ctx)
	if err != nil {
		t.Fatalf("GetMinConfirmations() failed: %v", err)
	}
	if minConf != 1 {
		t.Errorf("Expected default min_confirmations=1 after SeedFromEnv, got %d", minConf)
	}

	maxKoinu, err := svc.GetZeroConfMaxKoinu(ctx)
	if err != nil {
		t.Fatalf("GetZeroConfMaxKoinu() failed: %v", err)
	}
	if maxKoinu != 0 {
		t.Errorf("Expected default zero_conf_max_koinu=0 after SeedFromEnv, got %d", maxKoinu)
	}
}

func TestSeedFromEnvDoesNotOverwriteExisting(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() failed: %v", err)
	}
	defer func() { _ = s.Close() }()

	svc := NewSettingsService(s)
	ctx := context.Background()

	// Set an initial value
	if err := svc.SetMinConfirmations(ctx, 7); err != nil {
		t.Fatalf("SetMinConfirmations() failed: %v", err)
	}

	// Create a config
	cfg := config.Config{
		DBPath:     dbPath,
		BaseURL:    "http://localhost:8080",
		ListenAddr: ":8080",
	}

	// SeedFromEnv should not overwrite existing value
	if err := svc.SeedFromEnv(ctx, cfg); err != nil {
		t.Fatalf("SeedFromEnv() failed: %v", err)
	}

	// Value should remain unchanged
	minConf, err := svc.GetMinConfirmations(ctx)
	if err != nil {
		t.Fatalf("GetMinConfirmations() failed: %v", err)
	}
	if minConf != 7 {
		t.Errorf("Expected min_confirmations=7 (unchanged), got %d", minConf)
	}
}

func TestPoolWarnThresholdGetSetRoundtrip(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() failed: %v", err)
	}
	defer func() { _ = s.Close() }()

	svc := NewSettingsService(s)
	ctx := context.Background()

	// Test default
	value, err := svc.GetPoolWarnThreshold(ctx)
	if err != nil {
		t.Fatalf("GetPoolWarnThreshold() failed: %v", err)
	}
	if value != 25 {
		t.Errorf("Expected default pool_warn_threshold=25, got %d", value)
	}

	// Set and verify
	expectedValue := 50
	if err := svc.SetPoolWarnThreshold(ctx, expectedValue); err != nil {
		t.Fatalf("SetPoolWarnThreshold() failed: %v", err)
	}

	value, err = svc.GetPoolWarnThreshold(ctx)
	if err != nil {
		t.Fatalf("GetPoolWarnThreshold() after set failed: %v", err)
	}
	if value != expectedValue {
		t.Errorf("Expected pool_warn_threshold=%d after set, got %d", expectedValue, value)
	}
}

func TestPoolUrgentThresholdGetSetRoundtrip(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() failed: %v", err)
	}
	defer func() { _ = s.Close() }()

	svc := NewSettingsService(s)
	ctx := context.Background()

	// Test default
	value, err := svc.GetPoolUrgentThreshold(ctx)
	if err != nil {
		t.Fatalf("GetPoolUrgentThreshold() failed: %v", err)
	}
	if value != 10 {
		t.Errorf("Expected default pool_urgent_threshold=10, got %d", value)
	}

	// Set and verify
	expectedValue := 5
	if err := svc.SetPoolUrgentThreshold(ctx, expectedValue); err != nil {
		t.Fatalf("SetPoolUrgentThreshold() failed: %v", err)
	}

	value, err = svc.GetPoolUrgentThreshold(ctx)
	if err != nil {
		t.Fatalf("GetPoolUrgentThreshold() after set failed: %v", err)
	}
	if value != expectedValue {
		t.Errorf("Expected pool_urgent_threshold=%d after set, got %d", expectedValue, value)
	}
}

func TestChainCursorGetSetRoundtrip(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() failed: %v", err)
	}
	defer func() { _ = s.Close() }()

	svc := NewSettingsService(s)
	ctx := context.Background()

	// Test default (empty string = genesis)
	value, err := svc.GetChainCursor(ctx)
	if err != nil {
		t.Fatalf("GetChainCursor() failed: %v", err)
	}
	if value != "" {
		t.Errorf("Expected default chain_cursor='', got %q", value)
	}

	// Set and verify
	expectedValue := "0000abcd1234ef5678"
	if err := svc.SetChainCursor(ctx, expectedValue); err != nil {
		t.Fatalf("SetChainCursor() failed: %v", err)
	}

	value, err = svc.GetChainCursor(ctx)
	if err != nil {
		t.Fatalf("GetChainCursor() after set failed: %v", err)
	}
	if value != expectedValue {
		t.Errorf("Expected chain_cursor=%q after set, got %q", expectedValue, value)
	}
}

func TestTokenPriceKoinuGetSetRoundtrip(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() failed: %v", err)
	}
	defer func() { _ = s.Close() }()

	svc := NewSettingsService(s)
	ctx := context.Background()

	value, err := svc.GetTokenPriceKoinu(ctx)
	if err != nil {
		t.Fatalf("GetTokenPriceKoinu() failed: %v", err)
	}
	if value != 100_000_000 {
		t.Errorf("Expected default token_price_koinu=100000000, got %d", value)
	}

	expectedValue := int64(50_000_000)
	if err := svc.SetTokenPriceKoinu(ctx, expectedValue); err != nil {
		t.Fatalf("SetTokenPriceKoinu() failed: %v", err)
	}

	value, err = svc.GetTokenPriceKoinu(ctx)
	if err != nil {
		t.Fatalf("GetTokenPriceKoinu() after set failed: %v", err)
	}
	if value != expectedValue {
		t.Errorf("Expected token_price_koinu=%d after set, got %d", expectedValue, value)
	}
}

func TestAllSettingsDefaultValues(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open() failed: %v", err)
	}
	defer func() { _ = s.Close() }()

	svc := NewSettingsService(s)
	ctx := context.Background()

	// Test all settings have correct defaults when not set
	tests := []struct {
		name     string
		testFn   func() (interface{}, error)
		expected interface{}
	}{
		{
			name: "MinConfirmations default",
			testFn: func() (interface{}, error) {
				return svc.GetMinConfirmations(ctx)
			},
			expected: 1,
		},
		{
			name: "ZeroConfMaxKoinu default",
			testFn: func() (interface{}, error) {
				return svc.GetZeroConfMaxKoinu(ctx)
			},
			expected: int64(0),
		},
		{
			name: "PoolWarnThreshold default",
			testFn: func() (interface{}, error) {
				return svc.GetPoolWarnThreshold(ctx)
			},
			expected: 25,
		},
		{
			name: "PoolUrgentThreshold default",
			testFn: func() (interface{}, error) {
				return svc.GetPoolUrgentThreshold(ctx)
			},
			expected: 10,
		},
		{
			name: "ChainCursor default",
			testFn: func() (interface{}, error) {
				return svc.GetChainCursor(ctx)
			},
			expected: "",
		},
		{
			name: "TokenPriceKoinu default",
			testFn: func() (interface{}, error) {
				return svc.GetTokenPriceKoinu(ctx)
			},
			expected: int64(100_000_000),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, err := tt.testFn()
			if err != nil {
				t.Fatalf("failed: %v", err)
			}
			if value != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, value)
			}
		})
	}
}
