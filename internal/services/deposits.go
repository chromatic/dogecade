package services

import (
	"context"
	"fmt"

	"github.com/chromatic/dogecade/internal/store"
)

// DepositsService provides read access to the deposits table for the admin
// console's deposit browser.
type DepositsService struct {
	store *store.Store
}

// NewDepositsService creates a new DepositsService wrapping the given Store.
func NewDepositsService(s *store.Store) *DepositsService {
	return &DepositsService{store: s}
}

// Deposit is a projection of a deposits row joined with its address, for
// display purposes.
type Deposit struct {
	ID            int64
	Address       string
	TxID          string
	Vout          int
	AmountKoinu   int64
	Confirmations int
	State         string
	CreatedAt     string
	CreditedAt    string
}

// List returns deposits, most recent first, up to limit rows. If state is
// non-empty, results are filtered to that state.
func (svc *DepositsService) List(ctx context.Context, state string, limit int) ([]Deposit, error) {
	query := `
		SELECT d.id, a.address, d.txid, d.vout, d.amount_koinu, d.confirmations,
		       d.state, d.created_at, COALESCE(d.credited_at, '')
		FROM deposits d
		JOIN addresses a ON a.id = d.address_id`
	args := []any{}
	if state != "" {
		query += " WHERE d.state = ?"
		args = append(args, state)
	}
	query += " ORDER BY d.id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := svc.store.DB().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list deposits: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var deposits []Deposit
	for rows.Next() {
		var d Deposit
		if err := rows.Scan(&d.ID, &d.Address, &d.TxID, &d.Vout, &d.AmountKoinu, &d.Confirmations, &d.State, &d.CreatedAt, &d.CreditedAt); err != nil {
			return nil, fmt.Errorf("failed to scan deposit: %w", err)
		}
		deposits = append(deposits, d)
	}
	return deposits, rows.Err()
}
