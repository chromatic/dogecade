package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/chromatic/dogecade/internal/store"
)

// defaultRotationCheckInterval is how often RotationJob checks whether any
// direct-pay-enabled machine's active address is due for rotation.
const defaultRotationCheckInterval = 5 * time.Minute

// RotationJob periodically rotates each direct-pay-enabled machine's active
// address after a configurable time interval and/or number of uses (8.3).
// Both triggers are disabled by default (settings default to 0); a machine
// with neither configured keeps its address forever, same as before Phase 8.
type RotationJob struct {
	store    *store.Store
	settings *SettingsService
	direct   *DirectPayService
	logger   *slog.Logger
	interval time.Duration
}

// NewRotationJob creates a new RotationJob.
func NewRotationJob(s *store.Store, settings *SettingsService, direct *DirectPayService, logger *slog.Logger) *RotationJob {
	return &RotationJob{
		store:    s,
		settings: settings,
		direct:   direct,
		logger:   logger,
		interval: defaultRotationCheckInterval,
	}
}

// SetCheckInterval overrides how often CheckAll runs in Run. Intended for
// tests.
func (j *RotationJob) SetCheckInterval(d time.Duration) {
	j.interval = d
}

// Run starts the rotation-check loop, running CheckAll immediately and then
// every interval until ctx is cancelled. Should be called in a goroutine.
func (j *RotationJob) Run(ctx context.Context) {
	if err := j.CheckAll(ctx); err != nil {
		j.logger.Error("rotation check failed", "err", err)
	}
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := j.CheckAll(ctx); err != nil {
				j.logger.Error("rotation check failed", "err", err)
			}
		}
	}
}

type rotationCandidate struct {
	machineID  int64
	useCount   int
	assignedAt string
}

// CheckAll scans every direct-pay-enabled machine's active address and
// rotates it if either the interval or use-count threshold has been
// crossed. A pool-empty rotation failure is logged but not treated as a
// CheckAll error (DirectPayService.Rotate already raises an admin alert for
// it and leaves the old address in place).
func (j *RotationJob) CheckAll(ctx context.Context) error {
	rotateIntervalHours, err := j.settings.GetDirectPayRotateIntervalHours(ctx)
	if err != nil {
		return err
	}
	rotateAfterUses, err := j.settings.GetDirectPayRotateAfterUses(ctx)
	if err != nil {
		return err
	}
	if rotateIntervalHours <= 0 && rotateAfterUses <= 0 {
		return nil
	}

	rows, err := j.store.DB().QueryContext(ctx, `
		SELECT a.machine_id, a.use_count, COALESCE(a.assigned_at, '')
		FROM addresses a
		JOIN machines m ON m.id = a.machine_id
		WHERE a.purpose = 'machine_direct' AND a.state = 'assigned' AND m.direct_pay_enabled = 1
	`)
	if err != nil {
		return fmt.Errorf("failed to list active direct-pay addresses: %w", err)
	}
	var candidates []rotationCandidate
	for rows.Next() {
		var c rotationCandidate
		if err := rows.Scan(&c.machineID, &c.useCount, &c.assignedAt); err != nil {
			rows.Close()
			return fmt.Errorf("failed to scan direct-pay address: %w", err)
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, c := range candidates {
		due := rotateAfterUses > 0 && c.useCount >= rotateAfterUses
		if !due && rotateIntervalHours > 0 && c.assignedAt != "" {
			if assignedAt, err := time.Parse(time.RFC3339, c.assignedAt); err == nil {
				due = time.Since(assignedAt) >= time.Duration(rotateIntervalHours)*time.Hour
			}
		}
		if !due {
			continue
		}
		if _, err := j.direct.Rotate(ctx, c.machineID); err != nil && !errors.Is(err, ErrPoolEmpty) {
			j.logger.Error("failed to rotate direct-pay address", "machine_id", c.machineID, "err", err)
		}
	}
	return nil
}
