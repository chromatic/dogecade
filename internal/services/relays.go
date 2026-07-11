package services

import (
	"context"
	"fmt"

	"github.com/chromatic/dogecade/internal/store"
)

// RelaysService provides admin CRUD over relay_boards and machine_relays.
// Actually pulsing a board is the relay package's Dispatcher/Client's job;
// this service only manages the data those depend on.
type RelaysService struct {
	store *store.Store
}

// NewRelaysService creates a new RelaysService wrapping the given Store.
func NewRelaysService(s *store.Store) *RelaysService {
	return &RelaysService{store: s}
}

// RelayBoard is a projection of a relay_boards row for display purposes.
type RelayBoard struct {
	ID         int64
	Name       string
	BaseURL    string
	IsActive   bool
	LastSeenAt string
}

// ListBoards returns all relay boards, ordered by name.
func (svc *RelaysService) ListBoards(ctx context.Context) ([]RelayBoard, error) {
	rows, err := svc.store.DB().QueryContext(ctx,
		"SELECT id, name, base_url, is_active, COALESCE(last_seen_at, '') FROM relay_boards ORDER BY name",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list relay boards: %w", err)
	}
	defer rows.Close()

	var boards []RelayBoard
	for rows.Next() {
		var b RelayBoard
		if err := rows.Scan(&b.ID, &b.Name, &b.BaseURL, &b.IsActive, &b.LastSeenAt); err != nil {
			return nil, fmt.Errorf("failed to scan relay board: %w", err)
		}
		boards = append(boards, b)
	}
	return boards, rows.Err()
}

// ErrBoardNameTaken is returned by CreateBoard when the name is already in use.
var ErrBoardNameTaken = fmt.Errorf("relay board name already in use")

// CreateBoard inserts a new active relay board.
func (svc *RelaysService) CreateBoard(ctx context.Context, name, baseURL string) (int64, error) {
	var id int64
	err := svc.store.DB().QueryRowContext(ctx,
		"INSERT INTO relay_boards (name, base_url, is_active) VALUES (?, ?, 1) RETURNING id",
		name, baseURL,
	).Scan(&id)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return 0, ErrBoardNameTaken
		}
		return 0, fmt.Errorf("failed to create relay board %q: %w", name, err)
	}
	return id, nil
}

// SetBoardActive enables or disables a relay board.
func (svc *RelaysService) SetBoardActive(ctx context.Context, id int64, active bool) error {
	activeInt := 0
	if active {
		activeInt = 1
	}
	_, err := svc.store.DB().ExecContext(ctx,
		"UPDATE relay_boards SET is_active = ? WHERE id = ?", activeInt, id,
	)
	if err != nil {
		return fmt.Errorf("failed to set relay board %d active=%v: %w", id, active, err)
	}
	return nil
}

// MachineRelay is a projection of a machine_relays row joined with its board,
// for display purposes.
type MachineRelay struct {
	ID          int64
	MachineID   int64
	MachineName string
	BoardID     int64
	BoardName   string
	RelayNumber int
	IsActive    bool
}

// ListBindings returns every machine_relays row joined with machine and
// board names, ordered by machine name.
func (svc *RelaysService) ListBindings(ctx context.Context) ([]MachineRelay, error) {
	rows, err := svc.store.DB().QueryContext(ctx, `
		SELECT mr.id, mr.machine_id, m.name, mr.board_id, rb.name, mr.relay_number, mr.is_active
		FROM machine_relays mr
		JOIN machines m ON m.id = mr.machine_id
		JOIN relay_boards rb ON rb.id = mr.board_id
		ORDER BY m.name`)
	if err != nil {
		return nil, fmt.Errorf("failed to list relay bindings: %w", err)
	}
	defer rows.Close()

	var bindings []MachineRelay
	for rows.Next() {
		var b MachineRelay
		if err := rows.Scan(&b.ID, &b.MachineID, &b.MachineName, &b.BoardID, &b.BoardName, &b.RelayNumber, &b.IsActive); err != nil {
			return nil, fmt.Errorf("failed to scan relay binding: %w", err)
		}
		bindings = append(bindings, b)
	}
	return bindings, rows.Err()
}

// ErrBindingConflict is returned by Bind when the machine already has an
// active relay binding (only one is allowed at a time).
var ErrBindingConflict = fmt.Errorf("machine already has an active relay binding")

// Bind creates an active machine_relays row, binding machineID to
// boardID/relayNumber. Returns ErrBindingConflict if the machine already has
// an active binding (deactivate it first).
func (svc *RelaysService) Bind(ctx context.Context, machineID, boardID int64, relayNumber int) (int64, error) {
	var id int64
	err := svc.store.DB().QueryRowContext(ctx,
		"INSERT INTO machine_relays (machine_id, board_id, relay_number, is_active) VALUES (?, ?, ?, 1) RETURNING id",
		machineID, boardID, relayNumber,
	).Scan(&id)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return 0, ErrBindingConflict
		}
		return 0, fmt.Errorf("failed to bind machine %d to board %d relay %d: %w", machineID, boardID, relayNumber, err)
	}
	return id, nil
}

// Unbind deactivates a machine_relays row, freeing the machine to be bound
// to a different board/channel.
func (svc *RelaysService) Unbind(ctx context.Context, id int64) error {
	_, err := svc.store.DB().ExecContext(ctx,
		"UPDATE machine_relays SET is_active = 0 WHERE id = ?", id,
	)
	if err != nil {
		return fmt.Errorf("failed to unbind relay binding %d: %w", id, err)
	}
	return nil
}
