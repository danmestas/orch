package tmux

import (
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
