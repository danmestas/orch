// Package cmux is the cmux persistence engine — the Phase B addition
// (issue #207) that spawns worker surfaces via `cmux new-pane` and
// drives the wrap into them with `cmux send`.
//
// Self-registers in init() so cmd/orch only needs a blank import to
// activate the engine:
//
//	import _ "github.com/danmestas/orch/internal/persistence/cmux"
//
// Refactor history: this code was inline in cmd/orch/spawn_cmux.go
// until Phase 1 of the zmx work pulled the persistence.Engine seam
// around it. Behavior is byte-for-byte the same; the only test-visible
// change is the StartResult.RC field replacing the bare
// `(paneID, rc, err)` tuple.
//
// Engine-level constraints (unchanged from Phase B):
//   - --headless is rejected (cmux has no headless-session concept).
//   - --verify defers: a cmuxctl readiness probe earns its keep when a
//     second cmux operator workflow asks for it.
package cmux

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/danmestas/orch/internal/instance"
	"github.com/danmestas/orch/internal/persistence"
)

func init() {
	persistence.Register(&Engine{})
}

// Engine implements persistence.Engine atop cmux. Stateless.
type Engine struct{}

// Name returns "cmux".
func (*Engine) Name() string { return "cmux" }

// Start creates a cmux surface, sends the wrap into it, and returns a
// StartResult. Mirrors spawnPaneCmux from the pre-extraction code.
//
// Returns an error (not just RC=1) when:
//   - spec.Headless is set (cmux can't honor it)
//   - cmux is not on PATH
//   - cmux new-pane / send fails
//
// When verify is requested, surfaces stderr guidance and returns RC=1
// without erroring out — same shape as the pre-extraction code.
func (e *Engine) Start(spec persistence.StartSpec) (persistence.StartResult, error) {
	if spec.Headless {
		return persistence.StartResult{RC: 1}, fmt.Errorf(
			"orch spawn: --headless is not supported with --persistence=cmux " +
				"(cmux has no headless-session concept; rerun without --headless or use --persistence=tmux)")
	}

	if _, err := exec.LookPath("cmux"); err != nil {
		return persistence.StartResult{RC: 1}, fmt.Errorf(
			"orch spawn: cmux not on PATH — install cmux from https://cmux.app or add /Applications/cmux.app/Contents/Resources/bin to PATH")
	}

	// Wrap is built lazily so an unknown-agent error doesn't pre-empt
	// the --headless / cmux-PATH diagnostics above. Matches the order
	// the pre-extraction spawnPaneCmux used.
	wrap, err := spec.WrapFunc()
	if err != nil {
		return persistence.StartResult{RC: 1}, err
	}

	surface, err := launchSplit(spec.Position)
	if err != nil {
		return persistence.StartResult{RC: 1}, err
	}

	if err := sendWrap(surface, wrap); err != nil {
		// Pane exists but we couldn't feed the wrap. Best-effort cleanup
		// so we don't leak an empty pane.
		_ = exec.Command("cmux", "close-surface", "--surface", surface).Run()
		return persistence.StartResult{RC: 1}, err
	}

	handle := NewHandle(spec.Slug, surface)

	if !spec.Verify {
		return persistence.StartResult{Handle: handle, RC: 0}, nil
	}

	// --verify on cmux: deferred. tmuxctl.Verify is tmux-specific (probes
	// via list-panes / display -p / capture-pane with the tmux runner). A
	// cmuxctl equivalent earns its keep when we have a second cmux
	// operator workflow asking for it.
	fmt.Fprintf(os.Stderr,
		"orch spawn: --verify is not yet implemented for --persistence=cmux (pane %s spawned, but readiness probe skipped; pass --no-verify explicitly to silence this, or use --persistence=tmux for verify support)\n",
		surface)
	return persistence.StartResult{Handle: handle, RC: 1}, nil
}

// Attach is unimplemented in Phase 1 — no caller yet.
func (*Engine) Attach(slug string) (instance.Handle, error) {
	return nil, persistence.ErrNotImplemented
}

// List is unimplemented in Phase 1 — same rationale.
func (*Engine) List() ([]instance.Handle, error) {
	return nil, persistence.ErrNotImplemented
}

// launchSplit calls `cmux new-pane --direction <mapped> --focus false`
// and returns the new surface ref ("surface:N"). cmux's stdout format
// is `OK surface:N pane:N workspace:N`; we extract the surface token
// because send / capture-pane / close-surface all key off it.
func launchSplit(position string) (string, error) {
	dir := mapDirection(position)
	args := []string{"new-pane", "--direction", dir, "--focus", "false"}
	out, err := exec.Command("cmux", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("orch spawn: cmux new-pane: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	surface, parseErr := parseSurface(string(out))
	if parseErr != nil {
		return "", fmt.Errorf("orch spawn: cmux new-pane returned unexpected output: %s (%w)", strings.TrimSpace(string(out)), parseErr)
	}
	return surface, nil
}

// mapDirection maps orch's --position (right|left|above|below) onto
// cmux's --direction (right|left|up|down). Unknown positions fall back
// to "right" — same convention as the tmux engine.
func mapDirection(position string) string {
	switch position {
	case "right":
		return "right"
	case "left":
		return "left"
	case "above":
		return "up"
	case "below":
		return "down"
	default:
		return "right"
	}
}

// parseSurface extracts "surface:N" from cmux new-pane output.
// Expected shape: "OK surface:30 pane:25 workspace:2\n".
func parseSurface(out string) (string, error) {
	out = strings.TrimSpace(out)
	if out == "" {
		return "", fmt.Errorf("empty output")
	}
	for _, tok := range strings.Fields(out) {
		if strings.HasPrefix(tok, "surface:") {
			if len(tok) == len("surface:") {
				return "", fmt.Errorf("surface token missing id")
			}
			return tok, nil
		}
	}
	return "", fmt.Errorf("no surface:<id> token found")
}

// sendWrap feeds the WRAP string into the target surface followed by a
// newline. cmux send takes `\n` as the literal escape sequence per
// `cmux send --help`.
func sendWrap(surface, wrap string) error {
	if surface == "" {
		return fmt.Errorf("sendWrap: empty surface")
	}
	payload := wrap + `\n`
	out, err := exec.Command("cmux", "send", "--surface", surface, "--", payload).CombinedOutput()
	if err != nil {
		return fmt.Errorf("orch spawn: cmux send: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
