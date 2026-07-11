package web

import (
	"context"
	"database/sql"
	"fmt"
)

// depositStatus is the JSON shape sent to the /buy page, both over SSE and
// as the polling fallback.
type depositStatus struct {
	State            string `json:"state"`
	Confirmations    int    `json:"confirmations"`
	MinConfirmations int    `json:"min_confirmations"`
	AmountKoinu      int64  `json:"amount_koinu"`
}

// latestDepositStatus looks up the most recent deposit paid to address (if
// any) and reports its state alongside the confirmation threshold, so the
// buy page can render "seen" / "confirming (n/m)" / "credited" without the
// dispatcher needing to push anything itself — the page just polls (or
// subscribes over SSE, which is implemented as periodic polling
// server-side, since there's no pub/sub in this codebase yet).
func latestDepositStatus(ctx context.Context, db *sql.DB, address string, minConfirmations int) (depositStatus, error) {
	status := depositStatus{State: "waiting", MinConfirmations: minConfirmations}

	err := db.QueryRowContext(ctx,
		`SELECT d.state, d.confirmations, d.amount_koinu
		 FROM deposits d
		 JOIN addresses a ON a.id = d.address_id
		 WHERE a.address = ?
		 ORDER BY d.id DESC LIMIT 1`,
		address,
	).Scan(&status.State, &status.Confirmations, &status.AmountKoinu)
	if err == sql.ErrNoRows {
		return status, nil
	}
	if err != nil {
		return depositStatus{}, fmt.Errorf("failed to look up deposit status for %q: %w", address, err)
	}
	return status, nil
}
