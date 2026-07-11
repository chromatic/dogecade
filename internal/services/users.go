package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chromatic/dogecade/internal/store"
)

// UsersService provides basic account lookup/creation. Full OIDC-driven
// sign-in lands in Phase 6; for now this is enough for the token core to
// have accounts to credit and debit against.
type UsersService struct {
	store *store.Store
}

// NewUsersService creates a new UsersService wrapping the given Store.
func NewUsersService(s *store.Store) *UsersService {
	return &UsersService{store: s}
}

// User is a minimal projection of a users row, enough for session/auth use.
type User struct {
	ID          int64
	DisplayName string
	IsAdmin     bool
}

// GetOrCreateBySubjectHash returns the user for the given subject hash (e.g.
// sha256(issuer||subject) from OIDC), creating a new user row if none exists
// yet. isAdmin only applies at creation time (first-login bootstrap); it has
// no effect on an existing user, so revoking admin later must go through an
// explicit SetAdmin call rather than a repeated login.
func (svc *UsersService) GetOrCreateBySubjectHash(ctx context.Context, subjectHash, displayName string, isAdmin bool) (User, error) {
	var u User
	err := svc.store.DB().QueryRowContext(ctx,
		"SELECT id, display_name, is_admin FROM users WHERE subject_hash = ?",
		subjectHash,
	).Scan(&u.ID, &u.DisplayName, &u.IsAdmin)
	if err == nil {
		return u, nil
	}
	if err != sql.ErrNoRows {
		return User{}, fmt.Errorf("failed to look up user by subject hash: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	adminInt := 0
	if isAdmin {
		adminInt = 1
	}
	result, err := svc.store.DB().ExecContext(ctx,
		"INSERT INTO users (subject_hash, display_name, is_admin, created_at) VALUES (?, ?, ?, ?)",
		subjectHash, displayName, adminInt, now,
	)
	if err != nil {
		return User{}, fmt.Errorf("failed to create user: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return User{}, fmt.Errorf("failed to get new user ID: %w", err)
	}
	return User{ID: id, DisplayName: displayName, IsAdmin: isAdmin}, nil
}

// GetByID returns the user with the given ID.
func (svc *UsersService) GetByID(ctx context.Context, id int64) (User, error) {
	var u User
	err := svc.store.DB().QueryRowContext(ctx,
		"SELECT id, display_name, is_admin FROM users WHERE id = ?",
		id,
	).Scan(&u.ID, &u.DisplayName, &u.IsAdmin)
	if err != nil {
		return User{}, fmt.Errorf("failed to look up user %d: %w", id, err)
	}
	return u, nil
}

// SearchByDisplayName returns users whose display_name contains query
// (case-insensitive), up to limit rows. There's no email to search by design
// (see design.md's privacy stance), so display name is the only lookup key
// an admin has.
func (svc *UsersService) SearchByDisplayName(ctx context.Context, query string, limit int) ([]User, error) {
	rows, err := svc.store.DB().QueryContext(ctx,
		"SELECT id, COALESCE(display_name, ''), is_admin FROM users WHERE display_name LIKE ? ESCAPE '\\' ORDER BY id DESC LIMIT ?",
		"%"+escapeLike(query)+"%", limit,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to search users: %w", err)
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.DisplayName, &u.IsAdmin); err != nil {
			return nil, fmt.Errorf("failed to scan user: %w", err)
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// escapeLike escapes SQL LIKE wildcard characters so a search query is
// matched literally rather than as a pattern.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// ErrCannotMergeSameUser is returned by Merge when fromID equals toID.
var ErrCannotMergeSameUser = errors.New("cannot merge a user into itself")

// Merge reassigns fromID's ledger entries and bound addresses to toID (the
// "lost account" tool: a customer signs in with a different provider/device
// and an admin merges their old balance into the new account), then deletes
// the now-empty fromID user row. The merge is logged by the caller via
// AdminAuditService, not here.
func (svc *UsersService) Merge(ctx context.Context, fromID, toID int64) error {
	if fromID == toID {
		return ErrCannotMergeSameUser
	}
	tx, err := svc.store.DB().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "UPDATE token_ledger SET user_id = ? WHERE user_id = ?", toID, fromID); err != nil {
		return fmt.Errorf("failed to reassign ledger entries: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE addresses SET user_id = ? WHERE user_id = ?", toID, fromID); err != nil {
		return fmt.Errorf("failed to reassign addresses: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE credit_pulses SET user_id = ? WHERE user_id = ?", toID, fromID); err != nil {
		return fmt.Errorf("failed to reassign credit pulses: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM users WHERE id = ?", fromID); err != nil {
		return fmt.Errorf("failed to delete merged user %d: %w", fromID, err)
	}

	return tx.Commit()
}
