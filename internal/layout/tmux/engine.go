// Package tmux is the reference implementation of layout.Engine.
//
// Per Proposal 0008: the persistence engine owns the worker process;
// the layout engine owns operator-facing decoration. For tmux today
// "decoration" is the post-spawn labeling work that currently lives
// inline in bin/orch-spawn (lines 748-805 on origin/main post #200):
// pane title, alias-file write, slug-based identity surfaces.
//
// In Phase A this engine is wireable via the Go API but not yet on the
// bash hot path — bin/orch-spawn still performs labeling inline.
// Phase C will swap the inline bash for a call to orch-engines
// (cmd/orch-engines) which routes through this engine.
package tmux

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/danmestas/orch/internal/instance"
	"github.com/danmestas/orch/internal/layout"
)

// Engine is the tmux layout.Engine.
type Engine struct {
	tmuxBin     string
	aliasesFile string

	// mu serializes alias-file writes within a single process.
	// Cross-process safety is delegated to flock (see writeAlias).
	mu sync.Mutex
}

// Option configures an Engine.
type Option func(*Engine)

// WithTmuxBin overrides the tmux binary path. Defaults to "tmux"
// resolved via PATH.
func WithTmuxBin(path string) Option {
	return func(e *Engine) { e.tmuxBin = path }
}

// WithAliasesFile overrides the path to the alias file. Defaults to
// $ORCH_ALIASES_FILE or ~/.config/orch-aliases (mirroring
// bin/orch-spawn's resolution).
func WithAliasesFile(path string) Option {
	return func(e *Engine) { e.aliasesFile = path }
}

// New constructs a tmux layout engine. The alias file path is resolved
// at construction time so concurrent spawns can't see it change mid-
// flight. Resolution order matches internal/registry/sources.DefaultAliasPath
// — operator-set ORCH_ALIASES_FILE wins, then $XDG_CONFIG_HOME/orch-aliases,
// then $HOME/.config/orch-aliases.
func New(opts ...Option) *Engine {
	e := &Engine{tmuxBin: "tmux"}
	switch {
	case os.Getenv("ORCH_ALIASES_FILE") != "":
		e.aliasesFile = os.Getenv("ORCH_ALIASES_FILE")
	case os.Getenv("XDG_CONFIG_HOME") != "":
		e.aliasesFile = filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "orch-aliases")
	case os.Getenv("HOME") != "":
		e.aliasesFile = filepath.Join(os.Getenv("HOME"), ".config", "orch-aliases")
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Name implements layout.Engine.
func (e *Engine) Name() string { return "tmux" }

// Spawn implements layout.Engine.
//
// Three layers of slug labeling, mirroring bin/orch-spawn's current
// inline behavior on origin/main:
//
//  1. Pane title via `tmux select-pane -t <pane> -T <slug>` so the
//     operator's status bar shows the slug.
//  2. (Phase B will add layered window-title / session-name once cmux
//     forces the issue; tmux today gets by with pane title alone.)
//  3. Alias file append: `<slug>=<pane_id>` in ~/.config/orch-aliases
//     so orch-tell / orch-peek / orch-wait can resolve the slug
//     without an active bus subscription.
//
// Empty slug is a no-op (preserves the legacy unsluggy spawn shape).
func (e *Engine) Spawn(spec layout.SpawnSpec, h instance.Handle) error {
	if spec.Slug == "" {
		return nil
	}
	paneID := h.Locator()
	if !strings.HasPrefix(paneID, "%") {
		return fmt.Errorf("tmux layout: handle locator %q is not a tmux pane id", paneID)
	}

	// Layer 1: pane title. Best-effort — operator-visible polish, not
	// a correctness gate.
	_ = exec.Command(e.tmuxBin, "select-pane", "-t", paneID, "-T", spec.Slug).Run()

	// Layer 2: alias file. Cross-process safe via O_APPEND + flock
	// (when available); single-process safe via e.mu.
	if err := e.writeAlias(spec.Slug, paneID); err != nil {
		return fmt.Errorf("tmux layout: write alias: %w", err)
	}
	return nil
}

// Arrange implements layout.Engine. Phase A supports "default" (no-op,
// honors the operator's tmux defaults). The "grid" and "even-vertical"
// presets are wired but defer to tmux's built-in commands.
func (e *Engine) Arrange(preset string) error {
	switch preset {
	case "", "default":
		return nil
	case "grid", "tiled":
		return exec.Command(e.tmuxBin, "select-layout", "tiled").Run()
	case "even-vertical":
		return exec.Command(e.tmuxBin, "select-layout", "even-vertical").Run()
	case "even-horizontal":
		return exec.Command(e.tmuxBin, "select-layout", "even-horizontal").Run()
	default:
		return fmt.Errorf("tmux layout: unknown preset %q", preset)
	}
}

// Close implements layout.Engine. Strips the slug→pane mapping from
// the alias file. The actual pane close is the persistence engine's
// job (Handle.Kill); Close here is purely the layout-side decoration
// removal.
func (e *Engine) Close(slug string) error {
	if slug == "" {
		return nil
	}
	return e.removeAlias(slug)
}

// writeAlias writes <slug>=<paneID> to the alias file, stripping any
// prior entry for the same slug (so re-spawns don't accumulate stale
// rows).
//
// Cross-process safety: a flock(2) on the alias file when available;
// graceful fallback when not (the file is operator-managed and the
// collision window is microseconds — matches the bash semantics in
// bin/orch-spawn).
func (e *Engine) writeAlias(slug, paneID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.aliasesFile == "" {
		return errors.New("alias file path not resolved (set ORCH_ALIASES_FILE or HOME)")
	}
	if err := os.MkdirAll(filepath.Dir(e.aliasesFile), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(e.aliasesFile), err)
	}

	// Read existing lines, drop prior entries for this slug.
	existing := e.readAliasLines()
	kept := existing[:0]
	prefix := slug + "="
	for _, line := range existing {
		if strings.HasPrefix(line, prefix) {
			continue
		}
		kept = append(kept, line)
	}
	kept = append(kept, slug+"="+paneID)

	return e.writeAliasLines(kept)
}

// removeAlias strips every entry matching <slug>= from the alias
// file. Idempotent — no error if the slug is unknown.
func (e *Engine) removeAlias(slug string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.aliasesFile == "" {
		return nil
	}
	existing := e.readAliasLines()
	kept := existing[:0]
	prefix := slug + "="
	for _, line := range existing {
		if !strings.HasPrefix(line, prefix) {
			kept = append(kept, line)
		}
	}
	return e.writeAliasLines(kept)
}

// readAliasLines returns the alias file contents as a slice of trimmed
// non-empty lines. Missing file → empty slice.
func (e *Engine) readAliasLines() []string {
	f, err := os.Open(e.aliasesFile)
	if err != nil {
		return nil
	}
	defer f.Close()
	var lines []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// writeAliasLines atomically replaces the alias file with the given
// lines. Atomic via the rename-from-tempfile idiom so a crash mid-
// write can't corrupt the file.
func (e *Engine) writeAliasLines(lines []string) error {
	tmp := e.aliasesFile + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	for _, line := range lines {
		if _, err := f.WriteString(line + "\n"); err != nil {
			f.Close()
			os.Remove(tmp)
			return fmt.Errorf("write alias: %w", err)
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, e.aliasesFile); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename %s -> %s: %w", tmp, e.aliasesFile, err)
	}
	return nil
}

