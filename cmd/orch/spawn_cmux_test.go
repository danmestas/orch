package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/danmestas/orch/internal/persistence"
)

// Phase 1 of the zmx work moved cmux engine internals
// (cmuxDirection, parseCmuxSurface, sendWrap, spawnPaneCmux) into
// internal/persistence/cmux. The tests for those internals moved with
// the code; what remains here are the cmd/orch-side assertions that
// still need to live next to the parse/dispatch path.

func TestParseSpawnArgsAcceptsCmuxComposition(t *testing.T) {
	o, err := parseSpawnArgs([]string{"claude", "--persistence", "cmux", "--layout", "cmux"})
	if err != nil {
		t.Fatalf("--persistence cmux --layout cmux should parse, got %v", err)
	}
	if o.Persistence != "cmux" || o.Layout != "cmux" {
		t.Errorf("persistence=%q layout=%q; want cmux/cmux", o.Persistence, o.Layout)
	}
}

// The mixed-pair rejection lives in internal/persistence's
// IsSupported. validateComposition shells out to orch-engines (which
// calls into the same registry). Here we exercise the registry leg
// directly so the test doesn't depend on a built binary.
func TestRegistryRejectsMixedTmuxCmux(t *testing.T) {
	if persistence.IsSupported("tmux", "cmux") {
		t.Error("composition table accepted {tmux, cmux}; cross-engine pairs must require an explicit forwarder")
	}
	if persistence.IsSupported("cmux", "tmux") {
		t.Error("composition table accepted {cmux, tmux}; cross-engine pairs must require an explicit forwarder")
	}
}

func TestRegistryAcceptsCmuxCmux(t *testing.T) {
	if !persistence.IsSupported("cmux", "cmux") {
		t.Error("composition table missing {cmux, cmux}; Phase B (#207) should have registered it")
	}
	if err := persistence.RequirePair("cmux", "cmux"); err != nil {
		t.Errorf("RequirePair(cmux, cmux): unexpected err %v", err)
	}
}

func TestRegistryRejectsCmuxCmuxMix(t *testing.T) {
	err := persistence.RequirePair("cmux", "tmux")
	if err == nil {
		t.Fatal("RequirePair(cmux, tmux) = nil, want error")
	}
	if !errors.Is(err, persistence.ErrUnsupportedComposition) {
		t.Errorf("RequirePair err = %v, want errors.Is ErrUnsupportedComposition", err)
	}
}

// The spawn usage string lists --persistence/--layout but should not
// accidentally claim cmux support that requires Phase C tooling.
// Cheap regression guard: usage line still includes the flag names.
func TestSpawnUsageMentionsPersistenceLayout(t *testing.T) {
	err := spawnUsageError()
	var ee *exitError
	if !errors.As(err, &ee) {
		t.Fatalf("want *exitError, got %T", err)
	}
	for _, want := range []string{"--persistence", "--layout"} {
		if !strings.Contains(ee.msg, want) {
			t.Errorf("usage missing %q: %s", want, ee.msg)
		}
	}
}
