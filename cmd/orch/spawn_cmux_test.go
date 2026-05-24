package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/danmestas/orch/internal/persistence"
)

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

func TestCmuxDirectionMapping(t *testing.T) {
	cases := map[string]string{
		"right":   "right",
		"left":    "left",
		"above":   "up",
		"below":   "down",
		"":        "right", // default fallback
		"unknown": "right",
	}
	for in, want := range cases {
		if got := cmuxDirection(in); got != want {
			t.Errorf("cmuxDirection(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseCmuxSurfaceHappyPath(t *testing.T) {
	out := "OK surface:30 pane:25 workspace:2\n"
	got, err := parseCmuxSurface(out)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "surface:30" {
		t.Errorf("got %q, want surface:30", got)
	}
}

func TestParseCmuxSurfaceTolerantOrdering(t *testing.T) {
	// cmux is not contractually committed to a token order; the
	// extractor walks all whitespace-separated tokens looking for the
	// surface prefix.
	out := "pane:25 surface:30 workspace:2"
	got, err := parseCmuxSurface(out)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "surface:30" {
		t.Errorf("got %q, want surface:30", got)
	}
}

func TestParseCmuxSurfaceRejectsEmpty(t *testing.T) {
	if _, err := parseCmuxSurface(""); err == nil {
		t.Error("empty stdout should produce an error")
	}
	if _, err := parseCmuxSurface("   \n"); err == nil {
		t.Error("whitespace-only stdout should produce an error")
	}
}

func TestParseCmuxSurfaceRejectsMissingToken(t *testing.T) {
	// cmux replied OK but didn't emit a surface token — e.g. browser
	// pane variant. We don't want to silently mis-target a later send.
	if _, err := parseCmuxSurface("OK pane:25 workspace:2"); err == nil {
		t.Error("output without surface token should produce an error")
	}
}

func TestParseCmuxSurfaceRejectsBarePrefix(t *testing.T) {
	if _, err := parseCmuxSurface("OK surface: pane:25"); err == nil {
		t.Error("surface: with no id should produce an error")
	}
}

func TestCmuxSendWrapRejectsEmptySurface(t *testing.T) {
	if err := cmuxSendWrap("", "echo hi"); err == nil {
		t.Error("empty surface should produce an error before exec")
	}
}

// SpawnPaneCmux gates --headless because cmux has no headless-session
// equivalent. This must be a clean operator-facing error, not a panic
// downstream.
func TestSpawnPaneCmuxRejectsHeadless(t *testing.T) {
	o := &spawnOpts{Agent: "claude", Cwd: "/tmp", Headless: true, Position: "right"}
	_, rc, err := o.spawnPaneCmux()
	if err == nil {
		t.Fatal("--headless with cmux should error")
	}
	if rc != 1 {
		t.Errorf("rc=%d want 1", rc)
	}
	if !strings.Contains(err.Error(), "--headless is not supported with --persistence=cmux") {
		t.Errorf("error missing operator-facing guidance: %v", err)
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
