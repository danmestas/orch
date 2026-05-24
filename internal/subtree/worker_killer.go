package subtree

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/danmestas/orch/internal/instance"
	"github.com/danmestas/orch/internal/persistence/cmux"
	"github.com/danmestas/orch/internal/persistence/tmux"
	"github.com/danmestas/orch/internal/persistence/zmx"
)

// TmuxWorkerKiller implements WorkerKiller for workers spawned through
// any of orch's pluggable persistence engines (tmux, cmux, zmx). The
// kill path mirrors the operator pattern: send the engine-native
// SIGINT-equivalent (Ctrl-C) so the agent gets a graceful interrupt +
// flush, wait a moment, then call the engine's terminate verb to
// reclaim the slot.
//
// The struct name is historical — pre-#210 only tmux was wired. The
// dispatch is engine-aware now via Handle.GracefulShutdown + Handle.Kill.
//
// Cached handles drive the kill (Executor + PaneID + Abort metadata).
// Workers spawned through legacy paths (no cached handle) cannot be
// killed by this killer — callers either resolve them via the live
// registry first or accept that legacy spawns must be torn down
// manually.
//
// CF Worker / CF Durable Object kill paths are not yet implemented;
// when invoked for those Executors the killer returns a clear "not
// implemented for executor=X" error so the operator gets a signal
// instead of a silent skip.
type TmuxWorkerKiller struct {
	// BinPath was the absolute path to tmux on the pre-#210 killer.
	// It is preserved on the struct for source-compat with callers
	// that set it (the engine.Handle layer no longer reads it — the
	// engine-native binary is resolved via $PATH or, for zmx, via the
	// engine's own resolver). Tests that need a stub binary should
	// prepend their fake-tmux dir to $PATH instead of setting BinPath.
	BinPath string

	// GracePeriod is the wait between GracefulShutdown and Kill.
	// Default 500ms — long enough for the agent to flush a trailing
	// line, short enough that destroy doesn't drag.
	GracePeriod time.Duration

	// Stderr receives diagnostic lines from the killer. Nil falls back
	// to os.Stderr.
	Stderr *os.File
}

// NewTmuxWorkerKiller constructs the production killer.
func NewTmuxWorkerKiller() *TmuxWorkerKiller {
	return &TmuxWorkerKiller{Stderr: os.Stderr, GracePeriod: 500 * time.Millisecond}
}

// Kill implements WorkerKiller. See type doc for behaviour.
func (k *TmuxWorkerKiller) Kill(ctx context.Context, name string, handle *WorkerHandleRef) error {
	if handle == nil {
		return fmt.Errorf("subtree kill %q: no cached handle — cannot determine pane id (legacy spawn?)", name)
	}

	h, err := buildEngineHandle(name, handle)
	if err != nil {
		return err
	}

	// Step 1: graceful interrupt. Errors are non-fatal — Kill still
	// runs (#210 review: "errors from GracefulShutdown don't prevent
	// Kill from running"). Engine impls already swallow "target gone"
	// internally; this defensive check covers future engines whose
	// GracefulShutdown might surface a real error.
	if err := h.GracefulShutdown(ctx); err != nil {
		fmt.Fprintf(k.devnull(), "subtree kill %q: graceful shutdown failed (continuing to kill): %v\n", name, err)
	}

	// Step 2: grace window so the agent flushes before Kill yanks the
	// slot.
	grace := k.GracePeriod
	if grace <= 0 {
		grace = 500 * time.Millisecond
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(grace):
	}

	// Step 3: reclaim the slot. Engine.Kill is best-effort idempotent
	// across all three engines — a missing pane / surface / session is
	// treated as success internally, so we propagate any non-nil error
	// (which would be a true exec failure) up to the operator.
	if err := h.Kill(); err != nil {
		return fmt.Errorf("subtree kill %q: %w", name, err)
	}
	return nil
}

// buildEngineHandle resolves the WorkerHandleRef to an instance.Handle
// from the matching persistence engine. The locator field is engine-
// specific:
//
//	tmux → PaneID (e.g. "%37")
//	cmux → PaneID stores the cmux surface ref ("surface:30")
//	zmx  → PaneID stores the zmx session name (slug-shaped)
//
// PaneID is overloaded as "engine-native locator" because spawnspec's
// WorkerHandle schema doesn't yet carry per-engine locator fields
// (the executor enum is also still {tmux | cf-worker | cf-durable-
// object}). Once cmux/zmx are added to the WorkerHandle.Executor enum
// + locator fields, this helper switches on Executor first.
//
// Executors with no in-process kill path (cf-worker, cf-durable-object)
// surface the existing "not yet supported" error.
func buildEngineHandle(name string, ref *WorkerHandleRef) (instance.Handle, error) {
	if ref.PaneID == "" {
		return nil, fmt.Errorf("subtree kill %q: handle has no locator (executor=%q has no pane/surface/session id)", name, ref.Executor)
	}
	switch ref.Executor {
	case "", "tmux":
		return tmux.NewHandle(ref.ID, ref.PaneID), nil
	case "cmux":
		return cmux.NewHandle(ref.ID, ref.PaneID), nil
	case "zmx":
		// zmx.NewHandle takes (slug, sessionName, zmxBin). Empty
		// zmxBin → resolve via $PATH at exec time (matches engine's
		// own default).
		return zmx.NewHandle(ref.ID, ref.PaneID, ""), nil
	default:
		return nil, fmt.Errorf("subtree kill %q: executor=%s not yet supported (cf-worker / cf-durable-object are Phase B+)", name, ref.Executor)
	}
}

func (k *TmuxWorkerKiller) devnull() *os.File {
	if k.Stderr != nil {
		return k.Stderr
	}
	// Discard; using stderr at least surfaces failures.
	return os.Stderr
}
