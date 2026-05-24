package persistence

import (
	"fmt"
	"sort"
	"sync"

	"github.com/danmestas/orch/internal/instance"
)

// Engine is the narrow contract every persistence backend (tmux, cmux,
// future zmx) implements. The interface is the Rule-of-Three extract
// triggered by zmx's arrival on the queue — Phase 1 of the zmx work
// pulls the seam around the existing tmux + cmux implementations;
// Phase 2 plugs zmx in as the third citizen.
//
// Per the Ousterhout review on Proposal 0008, the surface is
// intentionally narrow:
//
//   - Start owns pane creation and engine-specific gating (--headless,
//     --verify); per-engine differences surface as typed errors, not as
//     a richer interface.
//   - Attach / List exist so future `orch attach <slug>` / `orch list`
//     callers don't have to engine-switch — but neither has a caller in
//     Phase 1, so implementations may return ErrNotImplemented.
//   - Wrap construction, slug labeling, shim launch all stay above the
//     engine: they're caller-side concerns, not engine concerns.
type Engine interface {
	// Name returns the registry key ("tmux", "cmux", future "zmx").
	Name() string

	// Start spawns a worker pane per the spec and returns a StartResult.
	// The error is non-nil for unrecoverable engine failures (binary
	// missing, engine doesn't support a requested flag). Verify failures
	// surface via StartResult.RC; the handle is still returned so the
	// caller can clean up.
	Start(spec StartSpec) (StartResult, error)

	// Attach returns the handle for an existing pane keyed by slug.
	// Phase 1 has no caller; implementations may return
	// ErrNotImplemented.
	Attach(slug string) (instance.Handle, error)

	// List enumerates all live worker panes the engine knows about.
	// Phase 1 has no caller; implementations may return
	// ErrNotImplemented.
	List() ([]instance.Handle, error)
}

// StartSpec is the engine-agnostic spawn intent. Caller-side responsibilities
// (wrap construction, slug regex validation, cwd resolution) have already
// happened by the time the engine sees this — the engine just executes.
type StartSpec struct {
	// Slug is the worker identity (may be empty for legacy pre-#181
	// callers). Engines use it for diagnostics; the alias-file write
	// stays caller-side.
	Slug string

	// WrapFunc lazily produces the fully-built shell command string that
	// will run inside the pane. Lazy so that engines can run their own
	// flag-rejection checks (e.g. cmux rejects --headless) BEFORE
	// reporting wrap-construction errors — preserves the pre-extraction
	// error-priority order.
	WrapFunc func() (string, error)

	// Agent is the harness name ("claude" | "pi" | "codex" | "gemini").
	// The engine forwards this to its readiness probe; engines without
	// a probe may ignore it.
	Agent string

	// Position is the layout hint (right|left|above|below). Engines map
	// it onto their native split vocabulary. Unknown values fall back to
	// "right" — same convention as the bash predecessor.
	Position string

	// Headless requests a detached, no-attached-tty pane (orch-headless
	// in tmux). cmux does not support this — Start returns an error when
	// the engine can't honor the flag.
	Headless bool

	// Verify asks the engine to poll the agent until ready before
	// returning. tmux supports this via tmuxctl.Verify; cmux defers it
	// (StartResult.RC=1 with a clear stderr line). Future zmx is
	// expected to defer until a readiness probe lands.
	Verify bool
}

// StartResult is the engine's reply. Handle is non-nil whenever a pane
// was created (even if verify subsequently failed); RC carries the
// readiness outcome (0=ok, 1=verify failed). This split mirrors the
// pre-extraction behavior in cmd/orch/spawn.go where verify failures
// returned (paneID, 1, nil) — same shape, typed now.
type StartResult struct {
	Handle instance.Handle
	RC     int
}

var (
	registryMu sync.RWMutex
	registry   = map[string]Engine{}
)

// Register installs an engine in the package-level registry. Engines
// self-register from their package init(); cmd/orch imports the engine
// packages for their side effects.
//
// Register panics on duplicate names — a duplicate registration is a
// programmer error (two packages claiming the same engine identity),
// not a runtime condition.
func Register(e Engine) {
	registryMu.Lock()
	defer registryMu.Unlock()
	name := e.Name()
	if _, dup := registry[name]; dup {
		panic("persistence: duplicate engine registration for " + name)
	}
	registry[name] = e
}

// Get returns the engine registered under name, or an error listing
// the registered names when no such engine exists.
func Get(name string) (Engine, error) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	if e, ok := registry[name]; ok {
		return e, nil
	}
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return nil, fmt.Errorf("persistence: no engine named %q (registered: %v)", name, names)
}

// Registered returns the sorted list of registered engine names. Used
// for diagnostics and tests.
func Registered() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
