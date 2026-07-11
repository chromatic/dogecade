package services

import (
	"context"
	"testing"

	"github.com/chromatic/dogecade/internal/config"
)

func TestNodeRPCConfigGetSetRoundtrip(t *testing.T) {
	s := newTestStore(t)
	svc := NewSettingsService(s)
	ctx := context.Background()

	empty, err := svc.GetNodeRPCConfig(ctx)
	if err != nil {
		t.Fatalf("GetNodeRPCConfig failed: %v", err)
	}
	if empty != (NodeRPCConfig{}) {
		t.Fatalf("expected empty config before any set, got %+v", empty)
	}

	want := NodeRPCConfig{RPCURL: "http://127.0.0.1:22555", RPCUser: "user", RPCPass: "pass", ZMQAddr: "tcp://127.0.0.1:28332"}
	if err := svc.SetNodeRPCConfig(ctx, want); err != nil {
		t.Fatalf("SetNodeRPCConfig failed: %v", err)
	}

	got, err := svc.GetNodeRPCConfig(ctx)
	if err != nil {
		t.Fatalf("GetNodeRPCConfig failed: %v", err)
	}
	if got != want {
		t.Fatalf("expected %+v, got %+v", want, got)
	}
}

func TestSeedFromEnvSeedsNodeRPCConfigOnce(t *testing.T) {
	s := newTestStore(t)
	svc := NewSettingsService(s)
	ctx := context.Background()

	cfg := config.Config{DogecoinRPCURL: "http://node.example:22555", DogecoinRPCUser: "envuser", DogecoinRPCPass: "envpass", DogecoinZMQAddr: "tcp://node.example:28332"}
	if err := svc.SeedFromEnv(ctx, cfg); err != nil {
		t.Fatalf("SeedFromEnv failed: %v", err)
	}

	got, err := svc.GetNodeRPCConfig(ctx)
	if err != nil {
		t.Fatalf("GetNodeRPCConfig failed: %v", err)
	}
	want := NodeRPCConfig{RPCURL: cfg.DogecoinRPCURL, RPCUser: cfg.DogecoinRPCUser, RPCPass: cfg.DogecoinRPCPass, ZMQAddr: cfg.DogecoinZMQAddr}
	if got != want {
		t.Fatalf("expected seeded config %+v, got %+v", want, got)
	}

	// An admin edit afterward must survive a later re-seed (e.g. on restart).
	edited := NodeRPCConfig{RPCURL: "http://admin-edited:22555", RPCUser: "envuser", RPCPass: "envpass", ZMQAddr: cfg.DogecoinZMQAddr}
	if err := svc.SetNodeRPCConfig(ctx, edited); err != nil {
		t.Fatalf("SetNodeRPCConfig failed: %v", err)
	}
	if err := svc.SeedFromEnv(ctx, cfg); err != nil {
		t.Fatalf("second SeedFromEnv failed: %v", err)
	}
	got, err = svc.GetNodeRPCConfig(ctx)
	if err != nil {
		t.Fatalf("GetNodeRPCConfig failed: %v", err)
	}
	if got.RPCURL != edited.RPCURL {
		t.Fatalf("expected admin edit to survive re-seed, got %+v", got)
	}
}
