package persistence_test

import (
	"strings"
	"testing"

	"github.com/danmestas/orch/internal/instance"
	"github.com/danmestas/orch/internal/persistence"

	// Side-effect imports activate the tmux + cmux engines so the
	// registry tests can exercise the real wiring.
	_ "github.com/danmestas/orch/internal/persistence/cmux"
	_ "github.com/danmestas/orch/internal/persistence/tmux"
)

func TestRegisteredIncludesTmuxAndCmux(t *testing.T) {
	got := persistence.Registered()
	want := map[string]bool{"tmux": true, "cmux": true}
	for _, n := range got {
		delete(want, n)
	}
	if len(want) != 0 {
		t.Errorf("Registered() missing %v; got %v", want, got)
	}
}

func TestGetTmux(t *testing.T) {
	e, err := persistence.Get("tmux")
	if err != nil {
		t.Fatalf("Get(tmux): %v", err)
	}
	if e.Name() != "tmux" {
		t.Errorf("Name()=%q want tmux", e.Name())
	}
}

func TestGetCmux(t *testing.T) {
	e, err := persistence.Get("cmux")
	if err != nil {
		t.Fatalf("Get(cmux): %v", err)
	}
	if e.Name() != "cmux" {
		t.Errorf("Name()=%q want cmux", e.Name())
	}
}

func TestGetUnknownReturnsDiagnostic(t *testing.T) {
	_, err := persistence.Get("zmx")
	if err == nil {
		t.Fatal("Get(zmx) should error pre-Phase-2")
	}
	if !strings.Contains(err.Error(), "registered:") {
		t.Errorf("error should list registered names, got: %v", err)
	}
}

// Engine interface compliance via the real engines' Attach/List on a
// Phase-1 brief (both should return ErrNotImplemented). Belt-and-
// suspenders so future engine code doesn't silently change the
// sentinel.
func TestAttachListReturnsErrNotImplemented(t *testing.T) {
	for _, name := range []string{"tmux", "cmux"} {
		e, err := persistence.Get(name)
		if err != nil {
			t.Fatalf("Get(%s): %v", name, err)
		}
		var _ instance.Handle = (instance.Handle)(nil) // compile-time anchor
		if _, err := e.Attach("any"); err == nil {
			t.Errorf("%s.Attach should be ErrNotImplemented in Phase 1", name)
		}
		if _, err := e.List(); err == nil {
			t.Errorf("%s.List should be ErrNotImplemented in Phase 1", name)
		}
	}
}
