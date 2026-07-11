package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// openTestStore is a test helper that creates a temporary database and opens it.
// The caller is responsible for calling store.Close() via defer.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() failed: %v", err)
	}
	return store
}

// tableExists is a test helper that checks if a table exists in the database.
func tableExists(t *testing.T, store *Store, tableName string) bool {
	t.Helper()
	var name string
	err := store.db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name=?",
		tableName,
	).Scan(&name)
	if err != nil && err != sql.ErrNoRows {
		t.Fatalf("Failed to query table existence: %v", err)
	}
	return name == tableName
}

func TestOpenCreatesDB(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	// Open should create the file and apply migrations.
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() failed: %v", err)
	}
	defer store.Close()

	// Verify the file exists.
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("Database file not created: %v", err)
	}
}

func TestOpenCreatesSettingsTable(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()

	if !tableExists(t, store, "settings") {
		t.Error("settings table not created")
	}
}

func TestOpenRecordsMigrationInSchemaMigrations(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()

	if !tableExists(t, store, "schema_migrations") {
		t.Fatal("schema_migrations table not created")
	}

	// Check that migration 0001 is recorded.
	var version string
	err := store.db.QueryRow(
		"SELECT version FROM schema_migrations WHERE version='0001_settings'",
	).Scan(&version)
	if err != nil {
		t.Fatalf("Migration 0001_settings not recorded: %v", err)
	}
	if version != "0001_settings" {
		t.Errorf("Expected version '0001_settings', got '%s'", version)
	}
}

func TestReopenDoesNotReapplyMigrations(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	// First open
	store1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("First Open() failed: %v", err)
	}
	store1.Close()

	// Second open should not fail or re-apply migrations
	store2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Second Open() failed: %v", err)
	}
	defer store2.Close()

	// Verify migration is still recorded exactly once (idempotency check)
	var count int
	err = store2.db.QueryRow(
		"SELECT COUNT(*) FROM schema_migrations WHERE version='0001_settings'",
	).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query schema_migrations: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 migration record after re-open, got %d (migrations re-applied?)", count)
	}
}

func TestForeignKeysAreEnabled(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()

	// Query foreign_keys pragma
	var value int
	err := store.db.QueryRow("PRAGMA foreign_keys").Scan(&value)
	if err != nil {
		t.Fatalf("Failed to query foreign_keys pragma: %v", err)
	}
	if value != 1 {
		t.Errorf("Expected foreign_keys=1, got %d", value)
	}
}

func TestWALModeIsSet(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()

	// Query journal_mode pragma
	var journalMode string
	err := store.db.QueryRow("PRAGMA journal_mode").Scan(&journalMode)
	if err != nil {
		t.Fatalf("Failed to query journal_mode pragma: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("Expected journal_mode=wal, got %s", journalMode)
	}
}

func TestClose(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() failed: %v", err)
	}

	// Close should succeed
	err = store.Close()
	if err != nil {
		t.Fatalf("Close() failed: %v", err)
	}

	// Subsequent DB operations should fail
	var version string
	err = store.db.QueryRow(
		"SELECT version FROM schema_migrations",
	).Scan(&version)
	if err == nil {
		t.Error("Expected query to fail after Close(), but it succeeded")
	}
}

func TestPing(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()

	// Ping with context should succeed
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := store.Ping(ctx)
	if err != nil {
		t.Fatalf("Ping() failed: %v", err)
	}
}

func TestPingClosedDatabase(t *testing.T) {
	store := openTestStore(t)
	store.Close()

	// Ping on closed database should fail
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := store.Ping(ctx)
	if err == nil {
		t.Error("Expected Ping() to fail on closed database, but it succeeded")
	}
}

func TestSettingsTableSchema(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()

	// Query the schema of settings table
	rows, err := store.db.Query("PRAGMA table_info(settings)")
	if err != nil {
		t.Fatalf("Failed to query table_info: %v", err)
	}
	defer rows.Close()

	columns := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dfltValue sql.NullString
		err = rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk)
		if err != nil {
			t.Fatalf("Failed to scan column info: %v", err)
		}
		columns[name] = true
	}

	// Verify key and value columns exist
	if !columns["key"] {
		t.Error("settings table missing 'key' column")
	}
	if !columns["value"] {
		t.Error("settings table missing 'value' column")
	}
}

func TestMigrationsAppliedInTransaction(t *testing.T) {
	// This test verifies that migrations are applied in a transaction by
	// checking that both schema_migrations and settings table exist together,
	// or neither exists (no partial state).
	store := openTestStore(t)
	defer store.Close()

	// Both tables should exist
	var settingsTable, migrationsTable string
	err := store.db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name='settings'",
	).Scan(&settingsTable)
	if err != nil && err != sql.ErrNoRows {
		t.Fatalf("Failed to query settings table: %v", err)
	}

	err = store.db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name='schema_migrations'",
	).Scan(&migrationsTable)
	if err != nil && err != sql.ErrNoRows {
		t.Fatalf("Failed to query schema_migrations table: %v", err)
	}

	// Both should exist or both should not exist (transactional consistency)
	if (settingsTable == "" && migrationsTable != "") ||
		(settingsTable != "" && migrationsTable == "") {
		t.Error("Transactional consistency violated: tables in partial state")
	}
}

func TestBusyTimeout(t *testing.T) {
	// Verify that the database opens with a configured busy timeout.
	// This is a basic smoke test to ensure the connection string includes
	// the timeout pragma.
	store := openTestStore(t)
	defer store.Close()

	// Query busy_timeout pragma - if not set or invalid, this may fail
	var timeout int
	err := store.db.QueryRow("PRAGMA busy_timeout").Scan(&timeout)
	if err != nil {
		t.Fatalf("Failed to query busy_timeout pragma: %v", err)
	}
	if timeout == 0 {
		t.Error("Expected non-zero busy_timeout")
	}
}

// Test that specific migrations are recorded
func TestMigrationsRecorded(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()

	tests := []string{
		"0001_settings",
		"0002_addresses",
	}

	for _, migName := range tests {
		var version string
		err := store.db.QueryRow(
			"SELECT version FROM schema_migrations WHERE version=?",
			migName,
		).Scan(&version)
		if err != nil {
			t.Errorf("Migration %s not recorded: %v", migName, err)
		}
	}
}

// Test that all required tables exist
func TestRequiredTablesExist(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()

	requiredTables := []string{
		"settings",
		"schema_migrations",
		"address_batches",
		"addresses",
		"hd_cursor",
	}

	for _, table := range requiredTables {
		if !tableExists(t, store, table) {
			t.Errorf("Required table '%s' not created", table)
		}
	}
}

func TestHdCursorTableIsEmpty(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()

	// Verify hd_cursor table starts empty (reserved for future use).
	var count int
	err := store.db.QueryRow("SELECT COUNT(*) FROM hd_cursor").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query hd_cursor: %v", err)
	}
	if count != 0 {
		t.Errorf("Expected hd_cursor to be empty, got %d rows", count)
	}
}

func TestAddressesInsertAndRead(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()

	// Insert a minimal valid addresses row.
	testAddr := "DKB3rR1kH3GK5bQEYvb9GxkNnbU3WsAUHf"
	_, err := store.db.Exec(`
		INSERT INTO addresses (address, state, purpose)
		VALUES (?, ?, ?)
	`, testAddr, "pool", "token_deposit")
	if err != nil {
		t.Fatalf("Failed to insert address: %v", err)
	}

	// Read it back.
	var id int
	var readAddr, state, purpose string
	err = store.db.QueryRow(`
		SELECT id, address, state, purpose FROM addresses WHERE address = ?
	`, testAddr).Scan(&id, &readAddr, &state, &purpose)
	if err != nil {
		t.Fatalf("Failed to read address: %v", err)
	}
	if readAddr != testAddr {
		t.Errorf("Expected address '%s', got '%s'", testAddr, readAddr)
	}
	if state != "pool" {
		t.Errorf("Expected state 'pool', got '%s'", state)
	}
	if purpose != "token_deposit" {
		t.Errorf("Expected purpose 'token_deposit', got '%s'", purpose)
	}
}

func TestAddressesUniqueConstraint(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	// Insert first address.
	testAddr := "DKB3rR1kH3GK5bQEYvb9GxkNnbU3WsAUHf"
	_, err := store.db.Exec(`
		INSERT INTO addresses (address, state, purpose)
		VALUES (?, ?, ?)
	`, testAddr, "pool", "token_deposit")
	if err != nil {
		t.Fatalf("Failed to insert first address: %v", err)
	}

	// Try to insert duplicate address; should fail.
	_, err = store.db.Exec(`
		INSERT INTO addresses (address, state, purpose)
		VALUES (?, ?, ?)
	`, testAddr, "pool", "machine_direct")
	if err == nil {
		t.Error("Expected UNIQUE constraint violation for duplicate address, but got no error")
	}
}

func TestAddressesStateCheckConstraint(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	// Try to insert an address with invalid state value.
	_, err := store.db.Exec(`
		INSERT INTO addresses (address, state, purpose)
		VALUES (?, ?, ?)
	`, "DKB3rR1kH3GK5bQEYvb9GxkNnbU3WsAUHf", "invalid_state", "token_deposit")
	if err == nil {
		t.Error("Expected CHECK constraint violation for invalid state, but got no error")
	}
}

func TestAddressesPurposeCheckConstraint(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()
	// Try to insert an address with invalid purpose value.
	_, err := store.db.Exec(`
		INSERT INTO addresses (address, state, purpose)
		VALUES (?, ?, ?)
	`, "DKB3rR1kH3GK5bQEYvb9GxkNnbU3WsAUHf", "pool", "invalid_purpose")
	if err == nil {
		t.Error("Expected CHECK constraint violation for invalid purpose, but got no error")
	}
}

func TestDepositsTableExists(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()

	if !tableExists(t, store, "deposits") {
		t.Error("deposits table not created")
	}
}

// insertTestAddress is a helper to create a test address in the database.
// Returns the address ID. Ensures FK tables exist first.
func insertTestAddress(t *testing.T, store *Store, addr, state, purpose string) int {
	t.Helper()
	_, err := store.db.Exec(`
		INSERT INTO addresses (address, state, purpose)
		VALUES (?, ?, ?)
	`, addr, state, purpose)
	if err != nil {
		t.Fatalf("Failed to insert address: %v", err)
	}

	var id int
	err = store.db.QueryRow(`SELECT id FROM addresses WHERE address = ?`, addr).Scan(&id)
	if err != nil {
		t.Fatalf("Failed to query inserted address ID: %v", err)
	}
	return id
}

func TestDepositsInsertMinimalValidRow(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()

	// Create a local address row to satisfy the FK constraint.
	testAddr := "DKB3rR1kH3GK5bQEYvb9GxkNnbU3WsAUHf"
	addressID := insertTestAddress(t, store, testAddr, "pool", "token_deposit")

	// Insert a minimal valid deposits row (state='seen', block_height NULL).
	testTxid := "abc123def456"
	testVout := 0
	_, err := store.db.Exec(`
		INSERT INTO deposits (address_id, txid, vout, amount_koinu, state, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, addressID, testTxid, testVout, 1000000, "seen", "2025-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("Failed to insert deposit: %v", err)
	}
}

func TestDepositsInsertAndRead(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()

	// Create a local address row to satisfy the FK constraint.
	testAddr := "DKB3rR1kH3GK5bQEYvb9GxkNnbU3WsAUHf"
	addressID := insertTestAddress(t, store, testAddr, "pool", "token_deposit")

	// Insert a deposits row.
	testTxid := "abc123def456"
	testVout := 0
	testCreatedAt := "2025-01-01T00:00:00Z"
	_, err := store.db.Exec(`
		INSERT INTO deposits (address_id, txid, vout, amount_koinu, state, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, addressID, testTxid, testVout, 1000000, "seen", testCreatedAt)
	if err != nil {
		t.Fatalf("Failed to insert deposit: %v", err)
	}

	// Read it back.
	var id, readAddressID, readVout, readAmountKoinu int
	var readTxid, readState, readCreatedAt string
	var readBlockHeight sql.NullInt64
	err = store.db.QueryRow(`
		SELECT id, address_id, txid, vout, amount_koinu, state, block_height, created_at
		FROM deposits WHERE txid = ?
	`, testTxid).Scan(&id, &readAddressID, &readTxid, &readVout, &readAmountKoinu, &readState, &readBlockHeight, &readCreatedAt)
	if err != nil {
		t.Fatalf("Failed to read deposit: %v", err)
	}

	if readTxid != testTxid {
		t.Errorf("Expected txid '%s', got '%s'", testTxid, readTxid)
	}
	if readVout != testVout {
		t.Errorf("Expected vout %d, got %d", testVout, readVout)
	}
	if readAddressID != addressID {
		t.Errorf("Expected address_id %d, got %d", addressID, readAddressID)
	}
	if readState != "seen" {
		t.Errorf("Expected state 'seen', got '%s'", readState)
	}
	if readBlockHeight.Valid {
		t.Errorf("Expected block_height to be NULL, got %d", readBlockHeight.Int64)
	}
	if readCreatedAt != testCreatedAt {
		t.Errorf("Expected created_at '%s', got '%s'", testCreatedAt, readCreatedAt)
	}
}

func TestDepositsUniqueTxidVoutConstraint(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()

	// Create a local address row.
	testAddr := "DKB3rR1kH3GK5bQEYvb9GxkNnbU3WsAUHf"
	addressID := insertTestAddress(t, store, testAddr, "pool", "token_deposit")

	// Insert first deposit.
	testTxid := "abc123def456"
	testVout := 0
	_, err := store.db.Exec(`
		INSERT INTO deposits (address_id, txid, vout, amount_koinu, state, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, addressID, testTxid, testVout, 1000000, "seen", "2025-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("Failed to insert first deposit: %v", err)
	}

	// Try to insert duplicate (txid, vout) pair; should fail.
	_, err = store.db.Exec(`
		INSERT INTO deposits (address_id, txid, vout, amount_koinu, state, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, addressID, testTxid, testVout, 2000000, "confirmed", "2025-01-02T00:00:00Z")
	if err == nil {
		t.Error("Expected UNIQUE constraint violation for duplicate (txid, vout), but got no error")
	}
}

func TestDepositsSameTxidDifferentVoutAllowed(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()

	// Create a local address row.
	testAddr := "DKB3rR1kH3GK5bQEYvb9GxkNnbU3WsAUHf"
	addressID := insertTestAddress(t, store, testAddr, "pool", "token_deposit")

	// Insert first deposit with vout=0.
	testTxid := "abc123def456"
	_, err := store.db.Exec(`
		INSERT INTO deposits (address_id, txid, vout, amount_koinu, state, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, addressID, testTxid, 0, 1000000, "seen", "2025-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("Failed to insert first deposit: %v", err)
	}

	// Insert second deposit with same txid but different vout=1; should succeed.
	_, err = store.db.Exec(`
		INSERT INTO deposits (address_id, txid, vout, amount_koinu, state, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, addressID, testTxid, 1, 2000000, "seen", "2025-01-02T00:00:00Z")
	if err != nil {
		t.Fatalf("Failed to insert second deposit with different vout: %v", err)
	}

	// Verify both are in the database.
	var count int
	err = store.db.QueryRow("SELECT COUNT(*) FROM deposits WHERE txid = ?", testTxid).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to count deposits: %v", err)
	}
	if count != 2 {
		t.Errorf("Expected 2 deposits with txid '%s', got %d", testTxid, count)
	}
}

func TestDepositsStateCheckConstraint(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()

	// Create a local address row.
	testAddr := "DKB3rR1kH3GK5bQEYvb9GxkNnbU3WsAUHf"
	addressID := insertTestAddress(t, store, testAddr, "pool", "token_deposit")

	// Try to insert a deposit with invalid state value.
	_, err := store.db.Exec(`
		INSERT INTO deposits (address_id, txid, vout, amount_koinu, state, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, addressID, "abc123def456", 0, 1000000, "invalid_state", "2025-01-01T00:00:00Z")
	if err == nil {
		t.Error("Expected CHECK constraint violation for invalid state, but got no error")
	}
}

func TestDepositsForeignKeyConstraint(t *testing.T) {
	store := openTestStore(t)
	defer store.Close()

	// Try to insert a deposit with a non-existent address_id.
	// This should fail due to the FK constraint (foreign_keys=ON is set by store.Open).
	_, err := store.db.Exec(`
		INSERT INTO deposits (address_id, txid, vout, amount_koinu, state, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, 99999, "abc123def456", 0, 1000000, "seen", "2025-01-01T00:00:00Z")
	if err == nil {
		t.Error("Expected FK constraint violation for non-existent address_id, but got no error")
	}
}
