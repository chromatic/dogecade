package services

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/chromatic/dogecade/internal/store"
)

// isUniqueConstraintErr reports whether err looks like a SQLite UNIQUE
// constraint violation. modernc.org/sqlite doesn't expose a typed error for
// this, so it's a string match on the driver's message.
func isUniqueConstraintErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// Machine is a projection of a machines row for display purposes.
type Machine struct {
	ID                   int64
	Slug                 string
	Name                 string
	IsActive             bool
	DirectPayEnabled     bool
	DirectPlayPriceKoinu int64
}

// MachinesService provides read access to the machines table for the
// customer-facing and admin UIs.
type MachinesService struct {
	store *store.Store
}

// NewMachinesService creates a new MachinesService wrapping the given Store.
func NewMachinesService(s *store.Store) *MachinesService {
	return &MachinesService{store: s}
}

// ListActive returns all active machines, ordered by name.
func (svc *MachinesService) ListActive(ctx context.Context) ([]Machine, error) {
	rows, err := svc.store.DB().QueryContext(ctx,
		"SELECT id, slug, name, is_active, direct_pay_enabled, COALESCE(direct_play_price_koinu, 0) FROM machines WHERE is_active = 1 ORDER BY name",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list machines: %w", err)
	}
	defer rows.Close()

	var machines []Machine
	for rows.Next() {
		var m Machine
		if err := rows.Scan(&m.ID, &m.Slug, &m.Name, &m.IsActive, &m.DirectPayEnabled, &m.DirectPlayPriceKoinu); err != nil {
			return nil, fmt.Errorf("failed to scan machine row: %w", err)
		}
		machines = append(machines, m)
	}
	return machines, rows.Err()
}

// ErrMachineSlugNotFound is returned when GetBySlug finds no matching row.
var ErrMachineSlugNotFound = fmt.Errorf("machine not found")

// GetBySlug returns the machine with the given slug.
func (svc *MachinesService) GetBySlug(ctx context.Context, slug string) (Machine, error) {
	var m Machine
	err := svc.store.DB().QueryRowContext(ctx,
		"SELECT id, slug, name, is_active, direct_pay_enabled, COALESCE(direct_play_price_koinu, 0) FROM machines WHERE slug = ?",
		slug,
	).Scan(&m.ID, &m.Slug, &m.Name, &m.IsActive, &m.DirectPayEnabled, &m.DirectPlayPriceKoinu)
	if err == sql.ErrNoRows {
		return Machine{}, ErrMachineSlugNotFound
	}
	if err != nil {
		return Machine{}, fmt.Errorf("failed to look up machine %q: %w", slug, err)
	}
	return m, nil
}

// ListAll returns every machine (active and inactive), ordered by name, for
// the admin console.
func (svc *MachinesService) ListAll(ctx context.Context) ([]Machine, error) {
	rows, err := svc.store.DB().QueryContext(ctx,
		"SELECT id, slug, name, is_active, direct_pay_enabled, COALESCE(direct_play_price_koinu, 0) FROM machines ORDER BY name",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list machines: %w", err)
	}
	defer rows.Close()

	var machines []Machine
	for rows.Next() {
		var m Machine
		if err := rows.Scan(&m.ID, &m.Slug, &m.Name, &m.IsActive, &m.DirectPayEnabled, &m.DirectPlayPriceKoinu); err != nil {
			return nil, fmt.Errorf("failed to scan machine row: %w", err)
		}
		machines = append(machines, m)
	}
	return machines, rows.Err()
}

// ErrMachineSlugTaken is returned by Create when the slug is already in use.
var ErrMachineSlugTaken = fmt.Errorf("machine slug already in use")

// Create inserts a new machine, active by default. Returns ErrMachineSlugTaken
// if the slug is already in use.
func (svc *MachinesService) Create(ctx context.Context, slug, name string) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	var id int64
	err := svc.store.DB().QueryRowContext(ctx,
		"INSERT INTO machines (slug, name, is_active, created_at) VALUES (?, ?, 1, ?) RETURNING id",
		slug, name, now,
	).Scan(&id)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return 0, ErrMachineSlugTaken
		}
		return 0, fmt.Errorf("failed to create machine %q: %w", slug, err)
	}
	return id, nil
}

// Update renames a machine's slug and display name. Slug changes take effect
// immediately for the customer-facing /m/{slug} URL and any QR codes that
// encode it, so already-printed/posted QR codes pointing at the old slug
// will 404 once this runs; regenerate and reprint them from
// GET /admin/machines/{id}/qr afterward. Returns ErrMachineSlugTaken if the
// new slug collides with a different machine.
func (svc *MachinesService) Update(ctx context.Context, id int64, slug, name string) error {
	_, err := svc.store.DB().ExecContext(ctx,
		"UPDATE machines SET slug = ?, name = ? WHERE id = ?",
		slug, name, id,
	)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return ErrMachineSlugTaken
		}
		return fmt.Errorf("failed to update machine %d: %w", id, err)
	}
	return nil
}

// SetActive enables or disables a machine (disabled machines are hidden from
// the customer-facing /machines list and can't be redeemed against).
func (svc *MachinesService) SetActive(ctx context.Context, id int64, active bool) error {
	activeInt := 0
	if active {
		activeInt = 1
	}
	_, err := svc.store.DB().ExecContext(ctx,
		"UPDATE machines SET is_active = ? WHERE id = ?", activeInt, id,
	)
	if err != nil {
		return fmt.Errorf("failed to set machine %d active=%v: %w", id, active, err)
	}
	return nil
}

// GetByID returns the machine with the given ID.
func (svc *MachinesService) GetByID(ctx context.Context, id int64) (Machine, error) {
	var m Machine
	err := svc.store.DB().QueryRowContext(ctx,
		"SELECT id, slug, name, is_active, direct_pay_enabled, COALESCE(direct_play_price_koinu, 0) FROM machines WHERE id = ?",
		id,
	).Scan(&m.ID, &m.Slug, &m.Name, &m.IsActive, &m.DirectPayEnabled, &m.DirectPlayPriceKoinu)
	if err == sql.ErrNoRows {
		return Machine{}, ErrMachineSlugNotFound
	}
	if err != nil {
		return Machine{}, fmt.Errorf("failed to look up machine %d: %w", id, err)
	}
	return m, nil
}

// SetDirectPay enables or disables direct-pay-to-machine (design.md's
// "secondary" purchase path) and sets its per-machine price in koinu.
// Disabling leaves any already-active direct-pay address, and the last
// configured price, alone: re-enabling later remembers both instead of
// forcing the admin to redo the setup.
//
// Footgun for callers other than the admin HTTP handler: priceKoinu is only
// applied when enabled is true, and only if it's positive — calling this
// with enabled=true and priceKoinu<=0 clears the remembered price back to
// "unset" rather than leaving it alone. The admin handler guards against
// this (it rejects enabling with a non-positive price before ever calling
// here), so this path is unreachable through the UI today; any new caller
// (CLI, script, test) that enables direct pay programmatically needs the
// same guard, or it will silently wipe a price an admin was relying on
// having remembered.
func (svc *MachinesService) SetDirectPay(ctx context.Context, id int64, enabled bool, priceKoinu int64) error {
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}
	if !enabled {
		// Disabling deliberately leaves direct_play_price_koinu untouched:
		// re-enabling later remembers the last price instead of forcing the
		// admin to retype it (and risk fat-fingering a different value).
		_, err := svc.store.DB().ExecContext(ctx,
			"UPDATE machines SET direct_pay_enabled = ? WHERE id = ?",
			enabledInt, id,
		)
		if err != nil {
			return fmt.Errorf("failed to set direct pay for machine %d: %w", id, err)
		}
		return nil
	}
	var priceArg any
	if priceKoinu > 0 {
		priceArg = priceKoinu
	}
	_, err := svc.store.DB().ExecContext(ctx,
		"UPDATE machines SET direct_pay_enabled = ?, direct_play_price_koinu = ? WHERE id = ?",
		enabledInt, priceArg, id,
	)
	if err != nil {
		return fmt.Errorf("failed to set direct pay for machine %d: %w", id, err)
	}
	return nil
}
