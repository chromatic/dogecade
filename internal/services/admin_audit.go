package services

import (
	"context"
	"fmt"
	"time"

	"github.com/chromatic/dogecade/internal/store"
)

// AdminAuditService records and lists admin console mutations, so the
// operator has a who/what/when trail without reading application logs.
type AdminAuditService struct {
	store *store.Store
}

// NewAdminAuditService creates a new AdminAuditService wrapping the given Store.
func NewAdminAuditService(s *store.Store) *AdminAuditService {
	return &AdminAuditService{store: s}
}

// AdminAuditEntry is a projection of an admin_audit row for display purposes.
type AdminAuditEntry struct {
	ID        int64
	UserID    int64
	Action    string
	Target    string
	Note      string
	CreatedAt string
}

// Log records an admin mutation. action is a short verb-ish identifier
// (e.g. "machine.create"), target identifies the affected entity (e.g.
// "machine:7"), and note carries any human-readable detail.
func (svc *AdminAuditService) Log(ctx context.Context, userID int64, action, target, note string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := svc.store.DB().ExecContext(ctx,
		"INSERT INTO admin_audit (user_id, action, target, note, created_at) VALUES (?, ?, ?, ?, ?)",
		userID, action, target, note, now,
	)
	if err != nil {
		return fmt.Errorf("failed to log admin action %q: %w", action, err)
	}
	return nil
}

// List returns the most recent admin_audit entries, most recent first.
func (svc *AdminAuditService) List(ctx context.Context, limit int) ([]AdminAuditEntry, error) {
	rows, err := svc.store.DB().QueryContext(ctx,
		"SELECT id, user_id, action, COALESCE(target, ''), COALESCE(note, ''), created_at FROM admin_audit ORDER BY id DESC LIMIT ?",
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list admin audit entries: %w", err)
	}
	defer rows.Close()

	var entries []AdminAuditEntry
	for rows.Next() {
		var e AdminAuditEntry
		if err := rows.Scan(&e.ID, &e.UserID, &e.Action, &e.Target, &e.Note, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan admin audit entry: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
