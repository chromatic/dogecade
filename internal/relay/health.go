package relay

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/chromatic/dogecade/internal/services"
	"github.com/chromatic/dogecade/internal/store"
)

// statusChecker is the subset of *Client used for board health polling.
type statusChecker interface {
	Status(ctx context.Context) (StatusResult, error)
}

// HealthClientFactory creates a statusChecker for a board's base URL.
// Defaults to wrapping NewClient; overridable in tests.
type HealthClientFactory func(baseURL string) statusChecker

func defaultHealthClientFactory(baseURL string) statusChecker {
	return NewClient(baseURL)
}

const defaultHealthPollInterval = 30 * time.Second

// BoardHealthChecker periodically polls every active relay board's Tasmota
// "Status" endpoint, updating relay_boards.last_seen_at on success and
// raising a deduped per-board alert when a board doesn't answer.
type BoardHealthChecker struct {
	store         *store.Store
	clientFactory HealthClientFactory
	pollInterval  time.Duration
}

// NewBoardHealthChecker creates a BoardHealthChecker wrapping the given store.
func NewBoardHealthChecker(s *store.Store) *BoardHealthChecker {
	return &BoardHealthChecker{
		store:         s,
		clientFactory: defaultHealthClientFactory,
		pollInterval:  defaultHealthPollInterval,
	}
}

// SetClientFactory overrides how the checker builds a statusChecker for a
// given board base URL. Intended for tests.
func (h *BoardHealthChecker) SetClientFactory(f HealthClientFactory) {
	h.clientFactory = f
}

// SetPollInterval overrides the polling interval. Intended for tests.
func (h *BoardHealthChecker) SetPollInterval(interval time.Duration) {
	h.pollInterval = interval
}

// Run starts the health-poll loop. It polls all active boards at
// pollInterval until ctx is cancelled. Should be called in a goroutine.
func (h *BoardHealthChecker) Run(ctx context.Context) {
	if err := h.CheckAll(ctx); err != nil {
		slog.Error("relay board health check failed", "err", err)
	}

	ticker := time.NewTicker(h.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := h.CheckAll(ctx); err != nil {
				slog.Error("relay board health check failed", "err", err)
			}
		}
	}
}

type activeBoard struct {
	ID      int64
	Name    string
	BaseURL string
}

// CheckAll polls the Status endpoint of every active relay board once.
// A board that fails to respond gets a deduped "relay_board_offline_<id>"
// alert (scoped per board so one offline board doesn't mask another); a
// board that responds gets last_seen_at bumped and any existing offline
// alert for it acknowledged.
func (h *BoardHealthChecker) CheckAll(ctx context.Context) error {
	boards, err := h.fetchActiveBoards(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch active relay boards: %w", err)
	}

	for _, b := range boards {
		h.checkBoard(ctx, b)
	}

	return nil
}

func (h *BoardHealthChecker) fetchActiveBoards(ctx context.Context) ([]activeBoard, error) {
	rows, err := h.store.DB().QueryContext(ctx,
		"SELECT id, name, base_url FROM relay_boards WHERE is_active = 1")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var boards []activeBoard
	for rows.Next() {
		var b activeBoard
		if err := rows.Scan(&b.ID, &b.Name, &b.BaseURL); err != nil {
			return nil, err
		}
		boards = append(boards, b)
	}
	return boards, rows.Err()
}

func (h *BoardHealthChecker) alertKind(boardID int64) string {
	return fmt.Sprintf("relay_board_offline_%d", boardID)
}

func (h *BoardHealthChecker) checkBoard(ctx context.Context, b activeBoard) {
	client := h.clientFactory(b.BaseURL)
	_, err := client.Status(ctx)

	if err != nil {
		message := fmt.Sprintf("Relay board %q (%s) is not responding: %v", b.Name, b.BaseURL, err)
		if alertErr := services.InsertAlertIfNotExists(ctx, h.store.DB(), h.alertKind(b.ID), message); alertErr != nil {
			slog.Error("failed to insert relay board offline alert", "board_id", b.ID, "err", alertErr)
		}
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := h.store.DB().ExecContext(ctx,
		"UPDATE relay_boards SET last_seen_at = ? WHERE id = ?", now, b.ID,
	); err != nil {
		slog.Error("failed to update relay board last_seen_at", "board_id", b.ID, "err", err)
	}

	if _, err := h.store.DB().ExecContext(ctx,
		"UPDATE alerts SET acked_at = ? WHERE kind = ? AND acked_at IS NULL", now, h.alertKind(b.ID),
	); err != nil {
		slog.Error("failed to ack relay board offline alert", "board_id", b.ID, "err", err)
	}
}
