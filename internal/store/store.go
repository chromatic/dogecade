package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Store wraps a SQLite database connection and provides data access.
type Store struct {
	db *sql.DB
}

// Open opens or creates a SQLite database at the given path, applies
// migrations, and returns a configured Store. The database is opened with:
// - WAL (Write-Ahead Logging) mode for concurrency
// - foreign_keys=ON for referential integrity
// - busy_timeout=5000ms to handle lock contention
func Open(path string) (*Store, error) {
	// Build connection string with pragmas.
	// modernc.org/sqlite accepts URI parameters:
	// file:path?param=value
	connStr := fmt.Sprintf("file:%s?cache=shared&mode=rwc", path)

	db, err := sql.Open("sqlite", connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Set connection pool settings.
	db.SetMaxOpenConns(1) // SQLite serializes writes anyway.
	db.SetMaxIdleConns(1)

	// Enable pragmas via connection.
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	}
	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("failed to set pragma %q: %w", pragma, err)
		}
	}

	store := &Store{db: db}

	// Run migrations.
	if err := store.runMigrations(); err != nil {
		_ = db.Close()
		return nil, err
	}

	return store, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// Ping verifies that the database connection is alive.
func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// DB returns the underlying *sql.DB connection.
// This is used by services to execute queries directly.
func (s *Store) DB() *sql.DB {
	return s.db
}

// runMigrations applies pending migrations from the migrations/ directory.
// Migrations are SQL files named NNN_description.sql and are applied in
// filename order within a transaction. Each migration is recorded in the
// schema_migrations table.
func (s *Store) runMigrations() error {
	// Create schema_migrations table if it doesn't exist.
	// This table is idempotent: repeated Open() calls don't recreate it.
	if err := s.createSchemaMigrationsTable(); err != nil {
		return err
	}

	// Read all migration files.
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		// No migrations directory is fine; just return.
		if strings.Contains(err.Error(), "no such file") {
			return nil
		}
		return fmt.Errorf("failed to read migrations directory: %w", err)
	}

	// Filter and sort migration files.
	var migrations []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".sql") {
			migrations = append(migrations, name)
		}
	}
	sort.Strings(migrations)

	// Apply each migration.
	for _, migName := range migrations {
		// Remove .sql extension for version tracking.
		version := strings.TrimSuffix(migName, ".sql")

		// Check if migration has already been applied.
		var applied int
		err := s.db.QueryRow(
			"SELECT COUNT(*) FROM schema_migrations WHERE version = ?",
			version,
		).Scan(&applied)
		if err != nil {
			return fmt.Errorf("failed to query schema_migrations: %w", err)
		}
		if applied > 0 {
			// Already applied.
			continue
		}

		// Read migration file.
		content, err := fs.ReadFile(migrationFS, filepath.Join("migrations", migName))
		if err != nil {
			return fmt.Errorf("failed to read migration %s: %w", migName, err)
		}

		// Apply migration in a transaction.
		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("failed to begin transaction for migration %s: %w", migName, err)
		}

		_, err = tx.Exec(string(content))
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("failed to execute migration %s: %w", migName, err)
		}

		// Record migration in schema_migrations.
		_, err = tx.Exec(
			"INSERT INTO schema_migrations (version) VALUES (?)",
			version,
		)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("failed to record migration %s in schema_migrations: %w", migName, err)
		}

		err = tx.Commit()
		if err != nil {
			return fmt.Errorf("failed to commit migration %s: %w", migName, err)
		}
	}

	return nil
}

// createSchemaMigrationsTable creates the schema_migrations table if it
// doesn't already exist. This is idempotent.
func (s *Store) createSchemaMigrationsTable() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create schema_migrations table: %w", err)
	}
	return nil
}
