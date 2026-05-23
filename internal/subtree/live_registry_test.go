package subtree

import (
	"context"
	"testing"
)

// TestEmptyLiveRegistry covers the safe-default impl: every name
// reads as missing, so a fresh apply (with empty registry) spawns
// everything.
func TestEmptyLiveRegistry(t *testing.T) {
	r := EmptyLiveRegistry()
	got, err := r.AliveByName(context.Background())
	if err != nil {
		t.Fatalf("AliveByName: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty set; got %v", got)
	}
}

// TestNATSLiveRegistry_NilConn surfaces a clear error when the
// constructor wasn't given a connection — the CLI's soft-fail path
// substitutes EmptyLiveRegistry instead, so this should never reach
// the operator, but the defensive check is cheap.
func TestNATSLiveRegistry_NilConn(t *testing.T) {
	r := &NATSLiveRegistry{}
	_, err := r.AliveByName(context.Background())
	if err == nil {
		t.Fatal("expected error on nil connection")
	}
}
