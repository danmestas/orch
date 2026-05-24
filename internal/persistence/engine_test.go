package persistence_test

import (
	"strings"
	"testing"

	"github.com/danmestas/orch/internal/instance"
	"github.com/danmestas/orch/internal/persistence"

	// Side-effect imports activate the tmux + cmux + zmx engines so the
	// registry tests can exercise the real wiring.
	_ "github.com/danmestas/orch/internal/persistence/cmux"
	_ "github.com/danmestas/orch/internal/persistence/tmux"
	_ "github.com/danmestas/orch/internal/persistence/zmx"
)

func TestRegisteredIncludesTmuxCmuxAndZmx(t *testing.T) {
	got := persistence.Registered()
	want := map[string]bool{"tmux": true, "cmux": true, "zmx": true}
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
	// Pre-Phase-2 this test asked for "zmx" and expected ErrNotFound;
	// Phase 2 registers zmx so the unknown-name probe needs a still-
	// fictional engine. "libghostty" stays on the docs/proposals slate
	// (Open question #4) but has no Go impl, so it stands in here.
	_, err := persistence.Get("libghostty")
	if err == nil {
		t.Fatal("Get(libghostty) should error — engine is unregistered")
	}
	if !strings.Contains(err.Error(), "registered:") {
		t.Errorf("error should list registered names, got: %v", err)
	}
}

// Engine interface compliance via the real engines' Attach/List on a
// Phase-1 brief (tmux + cmux should return ErrNotImplemented). zmx
// has working Attach/List implementations (Phase 2 — earned by zmx's
// cheap list/attach semantics) so it's excluded from this sentinel
// check. Belt-and-suspenders so future engine code doesn't silently
// change the tmux/cmux contract.
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
