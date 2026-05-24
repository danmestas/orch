package persistence_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/danmestas/orch/internal/persistence"
)

func TestIsSupportedTmuxTmux(t *testing.T) {
	if !persistence.IsSupported("tmux", "tmux") {
		t.Errorf("IsSupported(tmux, tmux) = false, want true (the Phase A default)")
	}
}

func TestIsSupportedCmuxCmux(t *testing.T) {
	if !persistence.IsSupported("cmux", "cmux") {
		t.Errorf("IsSupported(cmux, cmux) = false, want true (the Phase B addition, issue #207)")
	}
}

func TestIsSupportedRejectsCrossEngine(t *testing.T) {
	for _, tc := range []struct {
		p, l string
	}{
		{"tmux", "cmux"},      // rejected — cross-engine forwarder not built
		{"cmux", "tmux"},      // same
		{"tmux", "libghostty"},
		{"none", "tmux"},      // nonsense, registry rejects
		{"tmux", "none"},      // headless-noop deferred
		{"", ""},              // empty
		{"tmux", ""},          // partial empty
		{"", "tmux"},          // partial empty
		{"TMUX", "tmux"},      // case-sensitive
		{"CMUX", "cmux"},      // case-sensitive
		{"zmx", "zmx"},        // future engine, not landed yet
	} {
		if persistence.IsSupported(tc.p, tc.l) {
			t.Errorf("IsSupported(%q, %q) = true, want false", tc.p, tc.l)
		}
	}
}

func TestRequirePairOK(t *testing.T) {
	if err := persistence.RequirePair("tmux", "tmux"); err != nil {
		t.Errorf("RequirePair(tmux, tmux): unexpected err: %v", err)
	}
}

func TestRequirePairRejection(t *testing.T) {
	err := persistence.RequirePair("tmux", "cmux")
	if err == nil {
		t.Fatal("RequirePair(tmux, cmux) = nil, want error")
	}
	if !errors.Is(err, persistence.ErrUnsupportedComposition) {
		t.Errorf("RequirePair err = %v, want errors.Is ErrUnsupportedComposition", err)
	}
	// Diagnostic must name what to do (list supported pairs).
	if !strings.Contains(err.Error(), "supported:") {
		t.Errorf("RequirePair err message missing 'supported:' guidance: %v", err)
	}
	if !strings.Contains(err.Error(), "tmux") {
		t.Errorf("RequirePair err message missing the supported pair: %v", err)
	}
}

func TestSupportedPairsReturnsCopy(t *testing.T) {
	pairs := persistence.SupportedPairs()
	if len(pairs) == 0 {
		t.Fatal("SupportedPairs() returned empty slice")
	}
	// Caller mutation must not affect the registry.
	pairs[0] = persistence.Pair{Persistence: "bogus", Layout: "bogus"}
	if persistence.IsSupported("bogus", "bogus") {
		t.Error("caller mutation of SupportedPairs() leaked into the registry")
	}
}

func TestSupportedPairsContainsPhaseADefault(t *testing.T) {
	pairs := persistence.SupportedPairs()
	want := persistence.Pair{Persistence: "tmux", Layout: "tmux"}
	for _, p := range pairs {
		if p == want {
			return
		}
	}
	t.Errorf("SupportedPairs() missing %+v; got %+v", want, pairs)
}
