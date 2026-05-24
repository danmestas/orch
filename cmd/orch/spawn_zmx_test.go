package main

import (
	"errors"
	"testing"

	"github.com/danmestas/orch/internal/persistence"
)

// Phase 2 of the zmx work registers `{zmx, none}` in the composition
// table and self-registers the engine via blank import. These tests
// exercise the cmd/orch-side flag-parse + registry leg directly so
// they don't depend on a built orch-engines binary.

func TestParseSpawnArgsAcceptsZmxNoneComposition(t *testing.T) {
	o, err := parseSpawnArgs([]string{"claude", "--persistence", "zmx", "--layout", "none"})
	if err != nil {
		t.Fatalf("--persistence zmx --layout none should parse, got %v", err)
	}
	if o.Persistence != "zmx" || o.Layout != "none" {
		t.Errorf("persistence=%q layout=%q; want zmx/none", o.Persistence, o.Layout)
	}
}

func TestRegistryAcceptsZmxNone(t *testing.T) {
	if !persistence.IsSupported("zmx", "none") {
		t.Error("composition table missing {zmx, none}; Phase 2 (Proposal 0008 Phase C) should have registered it")
	}
	if err := persistence.RequirePair("zmx", "none"); err != nil {
		t.Errorf("RequirePair(zmx, none): unexpected err %v", err)
	}
}

// Cross-engine pairs with zmx are explicitly rejected — zmx is
// sessions-only, no layout forwarder exists.
func TestRegistryRejectsZmxCrossEngine(t *testing.T) {
	for _, tc := range []struct {
		p, l string
	}{
		{"zmx", "tmux"},
		{"zmx", "cmux"},
		{"zmx", "zmx"}, // common typo — `none`, not `zmx`
		{"tmux", "none"},
		{"cmux", "none"},
	} {
		if persistence.IsSupported(tc.p, tc.l) {
			t.Errorf("composition table accepted {%s, %s}; expected rejection", tc.p, tc.l)
		}
		err := persistence.RequirePair(tc.p, tc.l)
		if err == nil {
			t.Errorf("RequirePair(%s, %s) = nil, want error", tc.p, tc.l)
			continue
		}
		if !errors.Is(err, persistence.ErrUnsupportedComposition) {
			t.Errorf("RequirePair(%s, %s) err = %v, want errors.Is ErrUnsupportedComposition", tc.p, tc.l, err)
		}
	}
}

// `none` is a recognized layout name (DNS-label shape) for flag
// parsing purposes — the composition table is what gates whether the
// pair is actually supported. This guards against a regression where
// --layout=none might be rejected at parse time.
func TestParseSpawnArgsAcceptsLayoutNone(t *testing.T) {
	o, err := parseSpawnArgs([]string{"claude", "--persistence", "zmx", "--layout", "none"})
	if err != nil {
		t.Fatalf("--layout=none should parse for the zmx pair, got %v", err)
	}
	if o.Layout != "none" {
		t.Errorf("layout=%q want none", o.Layout)
	}
}
