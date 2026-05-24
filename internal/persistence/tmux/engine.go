// Package tmux is the reference implementation of persistence.Engine.
//
// Per Proposal 0008's Decision 5 (gradual cutover): the engine wraps
// the existing executors/tmux/spawn.sh bash script via os/exec rather
// than reimplementing tmux orchestration in Go. The bash script is the
// well-tested incumbent; the Go layer is the seam future engines (cmux)
// will compose against.
//
// The engine is NOT yet on the orch-spawn hot path in Phase A — bash
// orch-spawn still calls spawn.sh directly via resolve_executor (PR
// #200). This package exists so that:
//
//  1. Unit tests can exercise the persistence interface via a mock
//     instance.Handle, locking the contract.
//  2. The future cmux engine has a sibling to compose against.
//  3. Phase C (orch-spawn cutover) can swap bash → Go calls without
//     interface churn.
package tmux

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/danmestas/orch/internal/instance"
	"github.com/danmestas/orch/internal/persistence"
)

// Engine is the tmux persistence.Engine. New zero-value is unusable;
// always construct via New.
type Engine struct {
	// repoRoot is the orch repo root. The engine resolves
	// executors/tmux/spawn.sh relative to it.
	repoRoot string

	// spawnScript is the path to executors/tmux/spawn.sh. Set by
	// New from repoRoot; tests may override.
	spawnScript string

	// tmuxBin is the tmux binary path. Defaults to "tmux"; tests
	// may override via WithTmuxBin.
	tmuxBin string
}

// Option configures an Engine.
type Option func(*Engine)

// WithSpawnScript overrides the path to executors/tmux/spawn.sh. Used
// by tests; production code should rely on the repo-root default.
func WithSpawnScript(path string) Option {
	return func(e *Engine) { e.spawnScript = path }
}

// WithTmuxBin overrides the tmux binary path. Defaults to "tmux"
// resolved via PATH.
func WithTmuxBin(path string) Option {
	return func(e *Engine) { e.tmuxBin = path }
}

// New constructs a tmux persistence engine rooted at repoRoot (the orch
// repo root, used to locate executors/tmux/spawn.sh).
func New(repoRoot string, opts ...Option) *Engine {
	e := &Engine{
		repoRoot:    repoRoot,
		spawnScript: filepath.Join(repoRoot, "executors", "tmux", "spawn.sh"),
		tmuxBin:     "tmux",
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Name implements persistence.Engine.
func (e *Engine) Name() string { return "tmux" }

// Start implements persistence.Engine.
//
// The implementation shells out to executors/tmux/spawn.sh with the
// same env-var contract bin/orch-spawn uses today (AGENT, CWD, ROLE,
// HEADLESS, POSITION, VERIFY, OUTFIT, BUNDLE, NO_FLEET, NO_SHIM,
// BRIDGE, SLUG_EXPORTS, GOAL_EXPORTS). stdout from the script is
// exactly one line — the tmux pane id (e.g. "%64"). That becomes the
// Handle's Locator.
func (e *Engine) Start(spec persistence.StartSpec) (instance.Handle, error) {
	if _, err := os.Stat(e.spawnScript); err != nil {
		return nil, fmt.Errorf("tmux engine: spawn script not found at %s: %w", e.spawnScript, err)
	}

	cmd := exec.Command("bash", e.spawnScript)
	cmd.Env = e.buildEnv(spec)
	cmd.Stderr = os.Stderr // pass diagnostics straight through

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("tmux engine: spawn script failed: %w", err)
	}

	paneID := strings.TrimSpace(string(out))
	if paneID == "" {
		return nil, errors.New("tmux engine: spawn script produced empty pane id")
	}
	if !strings.HasPrefix(paneID, "%") {
		return nil, fmt.Errorf("tmux engine: spawn script produced non-pane-id output: %q", paneID)
	}

	return &tmuxHandle{
		slug:    spec.Slug,
		paneID:  paneID,
		tmuxBin: e.tmuxBin,
	}, nil
}

// Attach implements persistence.Engine. For tmux the "attach by slug"
// path consults the alias file (~/.config/orch-aliases) since that's
// where bin/orch-spawn writes slug→pane mappings today. The alias-file
// resolver lives in internal/registry; this engine intentionally does
// NOT re-implement it — Phase A keeps the seam thin.
//
// Returns persistence.ErrNotFound when the slug is unknown to tmux.
func (e *Engine) Attach(slug string) (instance.Handle, error) {
	if slug == "" {
		return nil, errors.New("tmux engine: attach requires a non-empty slug")
	}
	// Phase A: no in-engine alias resolution. Callers (e.g.
	// orch-tell, orch-peek) already do alias lookup in bash/registry
	// code and hand the engine a pane id directly. Engine.Attach
	// will earn its keep when cmux lands and the alias lookup is
	// engine-specific.
	return nil, fmt.Errorf("tmux engine: attach by slug not implemented in Phase A (slug=%q): %w", slug, persistence.ErrNotFound)
}

// List implements persistence.Engine. Returns handles for every tmux
// pane currently labelled with a slug (via select-pane -T). Phase A
// returns an empty list; List earns its keep in Phase C when the
// dispatcher needs to enumerate workers without going through the
// alias file.
func (e *Engine) List() ([]instance.Handle, error) {
	// Phase A: empty enumeration. Callers use the bash alias file
	// and orch-registry today; the engine's List is a placeholder
	// that locks the interface shape.
	return nil, nil
}

// buildEnv translates a StartSpec into the env-var contract spawn.sh
// reads. Mirrors bin/orch-spawn's pre-dispatch env-export block (around
// line 725 in the post-#200 script). Engine-set values override parent
// env entries with the same key.
func (e *Engine) buildEnv(spec persistence.StartSpec) []string {
	// Engine-controlled overrides, accumulated into a map then merged
	// over the parent env at the end. A map gives O(1) override
	// semantics; rebuilding the slice once at the end is cheaper than
	// scanning it per-set.
	overrides := map[string]string{
		"AGENT":    spec.Agent,
		"CWD":      spec.Cwd,
		"ROLE":     spec.Role,
		"OUTFIT":   spec.Outfit,
		"BUNDLE":   spec.Bundle,
		"BRIDGE":   spec.Bridge,
		"HEADLESS": boolEnv(spec.Headless),
		"NO_FLEET": boolEnv(spec.NoFleet),
		"NO_SHIM":  boolEnv(spec.NoShim),
		"VERIFY":   boolEnv(spec.Verify),
	}

	// SLUG_EXPORTS is the shell fragment spawn.sh inlines into the
	// pane's wrap command. Engine owns the ORCH_INSTANCE_ID
	// propagation contract.
	if spec.Slug != "" {
		overrides["SLUG_EXPORTS"] = fmt.Sprintf(" export ORCH_INSTANCE_ID=%s;", shellQuote(spec.Slug))
	} else {
		overrides["SLUG_EXPORTS"] = ""
	}

	// Caller-supplied passthrough (GOAL_EXPORTS, POSITION, any
	// future field) wins over the engine defaults — keeps the
	// signature stable as new vars are added.
	for k, v := range spec.Env {
		overrides[k] = v
	}

	// Build the final env: parent entries, with overrides replacing
	// any matching keys, then any overrides not seen in the parent
	// appended.
	parent := os.Environ()
	out := make([]string, 0, len(parent)+len(overrides))
	seen := make(map[string]bool, len(overrides))
	for _, entry := range parent {
		key := entry
		if i := strings.IndexByte(entry, '='); i >= 0 {
			key = entry[:i]
		}
		if v, ok := overrides[key]; ok {
			out = append(out, key+"="+v)
			seen[key] = true
		} else {
			out = append(out, entry)
		}
	}
	for k, v := range overrides {
		if !seen[k] {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// boolEnv stringifies a bool into the "0" / "1" form spawn.sh expects.
func boolEnv(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// shellQuote single-quotes a value for safe inclusion in a shell
// fragment. Matches the %q semantics bin/orch-spawn uses (which is
// printf's %q in bash). For our purposes — slugs are DNS-label-shaped
// per Proposal 0009 so this is conservative — but we keep the quoting
// honest in case the slug rules ever relax.
func shellQuote(s string) string {
	// Replace any single-quote in s with the canonical escape
	// "'\''" (close quote, escaped quote, reopen quote). Safe for
	// any input.
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
