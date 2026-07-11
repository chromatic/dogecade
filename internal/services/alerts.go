package services

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/chromatic/dogecade/internal/store"
)

// AlertsService provides read/ack access to the alerts table for the admin
// dashboard. Alerts are inserted by InsertAlertIfNotExists from various
// services; this type only handles listing and acknowledging them.
type AlertsService struct {
	store *store.Store
}

// NewAlertsService creates a new AlertsService wrapping the given Store.
func NewAlertsService(s *store.Store) *AlertsService {
	return &AlertsService{store: s}
}

// Alert is a projection of an alerts row for display purposes.
type Alert struct {
	ID        int64
	Kind      string
	Message   string
	CreatedAt string
}

// ListUnacked returns all alerts with acked_at IS NULL, most recent first.
func (svc *AlertsService) ListUnacked(ctx context.Context) ([]Alert, error) {
	rows, err := svc.store.DB().QueryContext(ctx,
		"SELECT id, kind, message, created_at FROM alerts WHERE acked_at IS NULL ORDER BY id DESC",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list unacked alerts: %w", err)
	}
	defer rows.Close()

	var alerts []Alert
	for rows.Next() {
		var a Alert
		if err := rows.Scan(&a.ID, &a.Kind, &a.Message, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan alert: %w", err)
		}
		alerts = append(alerts, a)
	}
	return alerts, rows.Err()
}

// Ack marks an alert as acknowledged (acked_at = now).
func (svc *AlertsService) Ack(ctx context.Context, id int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := svc.store.DB().ExecContext(ctx,
		"UPDATE alerts SET acked_at = ? WHERE id = ? AND acked_at IS NULL",
		now, id,
	)
	if err != nil {
		return fmt.Errorf("failed to ack alert %d: %w", id, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("no unacked alert found with id %d", id)
	}
	return nil
}

// InsertAlertIfNotExists inserts an alert into the alerts table only if an unacked
// alert of the same kind does not already exist. This prevents alert spam by deduping.
// It returns nil on success, or an error if the database operation fails.
func InsertAlertIfNotExists(ctx context.Context, db *sql.DB, kind, message string) error {
	// Check for existing unacked alert
	var existingCount int
	err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM alerts WHERE kind = ? AND acked_at IS NULL",
		kind,
	).Scan(&existingCount)
	if err != nil {
		return fmt.Errorf("failed to check for existing alert: %w", err)
	}

	if existingCount > 0 {
		// Unacked alert already exists; don't spam
		return nil
	}

	// Insert new alert
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = db.ExecContext(ctx,
		"INSERT INTO alerts (kind, message, created_at) VALUES (?, ?, ?)",
		kind, message, now,
	)
	if err != nil {
		return fmt.Errorf("failed to insert alert: %w", err)
	}

	return nil
}
