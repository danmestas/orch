package cmux

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danmestas/orch/internal/persistence"
)

func TestDirectionMapping(t *testing.T) {
	cases := map[string]string{
		"right":   "right",
		"left":    "left",
		"above":   "up",
		"below":   "down",
		"":        "right", // default fallback
		"unknown": "right",
	}
	for in, want := range cases {
		if got := mapDirection(in); got != want {
			t.Errorf("mapDirection(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseSurfaceHappyPath(t *testing.T) {
	out := "OK surface:30 pane:25 workspace:2\n"
	got, err := parseSurface(out)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "surface:30" {
		t.Errorf("got %q, want surface:30", got)
	}
}

func TestParseSurfaceTolerantOrdering(t *testing.T) {
	// cmux is not contractually committed to a token order; the
	// extractor walks all whitespace-separated tokens looking for the
	// surface prefix.
	out := "pane:25 surface:30 workspace:2"
	got, err := parseSurface(out)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "surface:30" {
		t.Errorf("got %q, want surface:30", got)
	}
}

func TestParseSurfaceRejectsEmpty(t *testing.T) {
	if _, err := parseSurface(""); err == nil {
		t.Error("empty stdout should produce an error")
	}
	if _, err := parseSurface("   \n"); err == nil {
		t.Error("whitespace-only stdout should produce an error")
	}
}

func TestParseSurfaceRejectsMissingToken(t *testing.T) {
	// cmux replied OK but didn't emit a surface token — e.g. browser
	// pane variant. We don't want to silently mis-target a later send.
	if _, err := parseSurface("OK pane:25 workspace:2"); err == nil {
		t.Error("output without surface token should produce an error")
	}
}

func TestParseSurfaceRejectsBarePrefix(t *testing.T) {
	if _, err := parseSurface("OK surface: pane:25"); err == nil {
		t.Error("surface: with no id should produce an error")
	}
}

func TestSendWrapRejectsEmptySurface(t *testing.T) {
	if err := sendWrap("", "echo hi"); err == nil {
		t.Error("empty surface should produce an error before exec")
	}
}

// Start gates --headless because cmux has no headless-session
// equivalent. This must be a clean operator-facing error, not a panic
// downstream.
func TestEngineStartRejectsHeadless(t *testing.T) {
	e := &Engine{}
	res, err := e.Start(persistence.StartSpec{
		Agent:    "claude",
		WrapFunc: func() (string, error) { return "true", nil },
		Position: "right",
		Headless: true,
	})
	if err == nil {
		t.Fatal("--headless with cmux should error")
	}
	if res.RC != 1 {
		t.Errorf("rc=%d want 1", res.RC)
	}
	if !strings.Contains(err.Error(), "--headless is not supported with --persistence=cmux") {
		t.Errorf("error missing operator-facing guidance: %v", err)
	}
}

func TestEngineName(t *testing.T) {
	if (&Engine{}).Name() != "cmux" {
		t.Errorf("Engine.Name() = %q, want cmux", (&Engine{}).Name())
	}
}

func TestEngineAttachListNotImplemented(t *testing.T) {
	e := &Engine{}
	if _, err := e.Attach("slug"); err == nil {
		t.Error("Attach should return ErrNotImplemented")
	}
	if _, err := e.List(); err == nil {
		t.Error("List should return ErrNotImplemented")
	}
}

func TestHandleAccessors(t *testing.T) {
	h := NewHandle("worker-1", "surface:30")
	if h.ID() != "worker-1" {
		t.Errorf("ID()=%q want worker-1", h.ID())
	}
	if h.Locator() != "surface:30" {
		t.Errorf("Locator()=%q want surface:30", h.Locator())
	}
}

func TestHandleKillEmptySurface(t *testing.T) {
	h := NewHandle("", "")
	if err := h.Kill(); err == nil {
		t.Error("Kill on empty surface should error")
	}
}

func TestHandleGracefulShutdownEmptySurface(t *testing.T) {
	h := NewHandle("", "")
	if err := h.GracefulShutdown(context.Background()); err == nil {
		t.Error("GracefulShutdown on empty surface should error")
	}
}

// TestHandleGracefulShutdownDispatchesSendKey verifies the handle
// calls `cmux send-key --surface <surf> ctrl+c` via PATH.
func TestHandleGracefulShutdownDispatchesSendKey(t *testing.T) {
	tmp := t.TempDir()
	logFile := filepath.Join(tmp, "cmux.log")
	stubScript := "#!/usr/bin/env bash\nprintf 'cmux: %s\\n' \"$*\" >> \"" + logFile + "\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(tmp, "cmux"), []byte(stubScript), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))

	h := NewHandle("worker-1", "surface:7")
	if err := h.GracefulShutdown(context.Background()); err != nil {
		t.Fatalf("GracefulShutdown: %v", err)
	}
	b, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	got := string(b)
	if !strings.Contains(got, "send-key --surface surface:7 ctrl+c") {
		t.Errorf("expected send-key argv; got %s", got)
	}
}
