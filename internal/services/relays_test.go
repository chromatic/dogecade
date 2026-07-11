package services

import (
	"context"
	"errors"
	"testing"
)

func TestRelaysServiceBoardCRUD(t *testing.T) {
	s := newTestStore(t)
	svc := NewRelaysService(s)
	ctx := context.Background()

	id, err := svc.CreateBoard(ctx, "relay1", "http://relay1.lan")
	if err != nil {
		t.Fatalf("CreateBoard failed: %v", err)
	}

	_, err = svc.CreateBoard(ctx, "relay1", "http://other.lan")
	if !errors.Is(err, ErrBoardNameTaken) {
		t.Fatalf("expected ErrBoardNameTaken, got %v", err)
	}

	boards, err := svc.ListBoards(ctx)
	if err != nil {
		t.Fatalf("ListBoards failed: %v", err)
	}
	if len(boards) != 1 || !boards[0].IsActive {
		t.Fatalf("expected one active board, got %+v", boards)
	}

	if err := svc.SetBoardActive(ctx, id, false); err != nil {
		t.Fatalf("SetBoardActive failed: %v", err)
	}
	boards, _ = svc.ListBoards(ctx)
	if boards[0].IsActive {
		t.Fatalf("expected board to be inactive after SetBoardActive(false)")
	}
}

func TestRelaysServiceBindingConflict(t *testing.T) {
	s := newTestStore(t)
	svc := NewRelaysService(s)
	ctx := context.Background()

	machineID := seedMachine(t, ctx, s, "machine-1", true)
	boardID, err := svc.CreateBoard(ctx, "relay1", "http://relay1.lan")
	if err != nil {
		t.Fatalf("CreateBoard failed: %v", err)
	}

	if _, err := svc.Bind(ctx, machineID, boardID, 1); err != nil {
		t.Fatalf("Bind failed: %v", err)
	}

	if _, err := svc.Bind(ctx, machineID, boardID, 2); !errors.Is(err, ErrBindingConflict) {
		t.Fatalf("expected ErrBindingConflict for a second active binding, got %v", err)
	}

	bindings, err := svc.ListBindings(ctx)
	if err != nil {
		t.Fatalf("ListBindings failed: %v", err)
	}
	if len(bindings) != 1 || bindings[0].MachineName != "machine-1" || bindings[0].BoardName != "relay1" {
		t.Fatalf("unexpected bindings: %+v", bindings)
	}

	if err := svc.Unbind(ctx, bindings[0].ID); err != nil {
		t.Fatalf("Unbind failed: %v", err)
	}
	if _, err := svc.Bind(ctx, machineID, boardID, 2); err != nil {
		t.Fatalf("expected re-bind to succeed after unbind, got %v", err)
	}
}
