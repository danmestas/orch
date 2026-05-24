package tmux

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEngineName(t *testing.T) {
	if (&Engine{}).Name() != "tmux" {
		t.Errorf("Engine.Name() = %q, want tmux", (&Engine{}).Name())
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
	h := NewHandle("worker-1", "%37")
	if h.ID() != "worker-1" {
		t.Errorf("ID()=%q want worker-1", h.ID())
	}
	if h.Locator() != "%37" {
		t.Errorf("Locator()=%q want %%37", h.Locator())
	}
}

func TestHandleKillEmptyPane(t *testing.T) {
	h := NewHandle("", "")
	if err := h.Kill(); err == nil {
		t.Error("Kill on empty pane id should error")
	}
}

func TestHandleGracefulShutdownEmptyPane(t *testing.T) {
	h := NewHandle("", "")
	if err := h.GracefulShutdown(context.Background()); err == nil {
		t.Error("GracefulShutdown on empty pane id should error")
	}
}

// TestHandleGracefulShutdownDispatchesSendKeys verifies the handle
// calls `tmux send-keys -t <pane> C-c` via PATH.
func TestHandleGracefulShutdownDispatchesSendKeys(t *testing.T) {
	tmp := t.TempDir()
	logFile := filepath.Join(tmp, "tmux.log")
	stubScript := "#!/usr/bin/env bash\nprintf 'tmux: %s\\n' \"$*\" >> \"" + logFile + "\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(tmp, "tmux"), []byte(stubScript), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))

	h := NewHandle("worker-1", "%42")
	if err := h.GracefulShutdown(context.Background()); err != nil {
		t.Fatalf("GracefulShutdown: %v", err)
	}
	b, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	got := string(b)
	if !strings.Contains(got, "send-keys -t %42 C-c") {
		t.Errorf("expected send-keys argv; got %s", got)
	}
}

// TestHandleGracefulShutdownIdempotent confirms a non-zero stub exit
// (mimicking "pane already gone") is swallowed.
func TestHandleGracefulShutdownIdempotent(t *testing.T) {
	tmp := t.TempDir()
	logFile := filepath.Join(tmp, "tmux.log")
	stubScript := "#!/usr/bin/env bash\nprintf 'tmux: %s\\n' \"$*\" >> \"" + logFile + "\"\necho \"can't find pane\" >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(tmp, "tmux"), []byte(stubScript), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))

	h := NewHandle("worker-1", "%999")
	if err := h.GracefulShutdown(context.Background()); err != nil {
		t.Errorf("GracefulShutdown should swallow non-zero exit; got %v", err)
	}
}

func TestSplitLines(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"a\n", 1},
		{"a\nb\nc\n", 3},
		{"a\n\nb\n", 2},
	}
	for _, tc := range cases {
		got := splitLines(tc.in)
		if len(got) != tc.want {
			t.Errorf("splitLines(%q) -> %d lines, want %d", tc.in, len(got), tc.want)
		}
	}
}
