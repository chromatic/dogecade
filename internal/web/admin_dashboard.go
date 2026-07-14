package web

import (
	"context"
	"database/sql"
	"fmt"
)

// recentRedemption is a projection of a credit_pulses row (source =
// token_redemption) joined with its machine, for the admin dashboard's
// "recent redemptions" panel.
type recentRedemption struct {
	ID          int64
	MachineName string
	State       string
	Attempts    int
	CreatedAt   string
}

// recentRedemptions returns the most recent token-redemption credit pulses,
// most recent first. Raw SQL here mirrors the existing precedent in
// buy_status.go rather than adding a single-purpose service for one
// dashboard panel.
func recentRedemptions(ctx context.Context, db *sql.DB, limit int) ([]recentRedemption, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT cp.id, m.name, cp.state, cp.attempts, cp.created_at
		FROM credit_pulses cp
		JOIN machines m ON m.id = cp.machine_id
		WHERE cp.source = 'token_redemption'
		ORDER BY cp.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list recent redemptions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []recentRedemption
	for rows.Next() {
		var r recentRedemption
		if err := rows.Scan(&r.ID, &r.MachineName, &r.State, &r.Attempts, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan recent redemption: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
