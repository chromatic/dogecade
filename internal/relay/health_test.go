package relay

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
)

type fakeStatusChecker struct {
	err error
}

func (f *fakeStatusChecker) Status(ctx context.Context) (StatusResult, error) {
	if f.err != nil {
		return StatusResult{}, f.err
	}
	return StatusResult{}, nil
}

func boardLastSeenAt(t *testing.T, ctx context.Context, s interface {
	DB() *sql.DB
}, boardID int64) sql.NullString {
	t.Helper()
	var lastSeen sql.NullString
	err := s.DB().QueryRowContext(ctx, "SELECT last_seen_at FROM relay_boards WHERE id = ?", boardID).Scan(&lastSeen)
	if err != nil {
		t.Fatalf("failed to query last_seen_at: %v", err)
	}
	return lastSeen
}

func TestCheckAllUpdatesLastSeenOnSuccess(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	h := NewBoardHealthChecker(s)
	h.SetClientFactory(func(baseURL string) statusChecker { return &fakeStatusChecker{} })

	boardID := seedRelayBoard(t, ctx, s, "http://board.invalid")

	if err := h.CheckAll(ctx); err != nil {
		t.Fatalf("CheckAll failed: %v", err)
	}

	lastSeen := boardLastSeenAt(t, ctx, s, boardID)
	if !lastSeen.Valid || lastSeen.String == "" {
		t.Error("expected last_seen_at to be set after a successful health check")
	}
}

func TestCheckAllAlertsOnFailureAndDedupsPerBoard(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	h := NewBoardHealthChecker(s)
	h.SetClientFactory(func(baseURL string) statusChecker {
		return &fakeStatusChecker{err: errors.New("connection refused")}
	})

	boardID := seedRelayBoard(t, ctx, s, "http://board.invalid")

	if err := h.CheckAll(ctx); err != nil {
		t.Fatalf("CheckAll failed: %v", err)
	}
	if err := h.CheckAll(ctx); err != nil {
		t.Fatalf("second CheckAll failed: %v", err)
	}

	kind := fmt.Sprintf("relay_board_offline_%d", boardID)
	if got := alertCount(t, ctx, s, kind); got != 1 {
		t.Errorf("expected exactly 1 unacked alert after two failed checks (dedup), got %d", got)
	}

	lastSeen := boardLastSeenAt(t, ctx, s, boardID)
	if lastSeen.Valid {
		t.Error("expected last_seen_at to remain unset for a board that never responded")
	}
}

func TestCheckAllAcksAlertOnRecovery(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	h := NewBoardHealthChecker(s)

	failing := true
	h.SetClientFactory(func(baseURL string) statusChecker {
		if failing {
			return &fakeStatusChecker{err: errors.New("timeout")}
		}
		return &fakeStatusChecker{}
	})

	boardID := seedRelayBoard(t, ctx, s, "http://board.invalid")

	if err := h.CheckAll(ctx); err != nil {
		t.Fatalf("CheckAll failed: %v", err)
	}
	kind := fmt.Sprintf("relay_board_offline_%d", boardID)
	if got := alertCount(t, ctx, s, kind); got != 1 {
		t.Fatalf("expected 1 unacked alert while board is down, got %d", got)
	}

	failing = false
	if err := h.CheckAll(ctx); err != nil {
		t.Fatalf("CheckAll (recovery) failed: %v", err)
	}
	if got := alertCount(t, ctx, s, kind); got != 0 {
		t.Errorf("expected offline alert to be acked after recovery, got %d unacked", got)
	}
}

func TestCheckAllSkipsInactiveBoards(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	h := NewBoardHealthChecker(s)

	called := false
	h.SetClientFactory(func(baseURL string) statusChecker {
		called = true
		return &fakeStatusChecker{}
	})

	boardID := seedRelayBoard(t, ctx, s, "http://board.invalid")
	if _, err := s.DB().ExecContext(ctx, "UPDATE relay_boards SET is_active = 0 WHERE id = ?", boardID); err != nil {
		t.Fatalf("failed to deactivate board: %v", err)
	}

	if err := h.CheckAll(ctx); err != nil {
		t.Fatalf("CheckAll failed: %v", err)
	}
	if called {
		t.Error("expected inactive board to be skipped, but client factory was invoked")
	}
}
