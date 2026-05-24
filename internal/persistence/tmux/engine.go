// Package tmux is the tmux persistence engine — the live-since-Phase-A
// backend that spawns worker panes via `tmux split-window` (or
// `tmux new-session -d` in headless mode) and verifies readiness via
// internal/tmuxctl.
//
// Self-registers in init() so cmd/orch only needs a blank import to
// activate the engine:
//
//	import _ "github.com/danmestas/orch/internal/persistence/tmux"
//
// Refactor history: this code was inline in cmd/orch/spawn_tmux.go
// until Phase 1 of the zmx work pulled the persistence.Engine seam
// around it. Behavior is byte-for-byte the same as the pre-extraction
// version; the only test-visible change is the StartResult.RC field
// replacing the bare `(paneID, rc, err)` tuple.
package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/danmestas/orch/internal/instance"
	"github.com/danmestas/orch/internal/persistence"
	"github.com/danmestas/orch/internal/tmuxctl"
)

func init() {
	persistence.Register(&Engine{})
}

// Engine implements persistence.Engine atop tmux. Stateless — all
// engine state lives in tmux itself.
type Engine struct{}

// Name returns "tmux".
func (*Engine) Name() string { return "tmux" }

// Start spawns a tmux pane per the spec, runs verify when requested,
// and returns a StartResult with the pane handle and readiness rc.
//
// Mirrors the pre-extraction spawnPane logic from cmd/orch/spawn_tmux.go
// exactly: split-window vs new-session by spec.Headless, optional
// tmuxctl.Verify probe, same stderr lines on each outcome.
func (e *Engine) Start(spec persistence.StartSpec) (persistence.StartResult, error) {
	// tmux's pre-extraction order: buildWrap first, then dispatch to
	// launchSplit/launchHeadless. Preserves the (paneID="", rc=1)
	// shape for unknown-agent errors caught at wrap construction.
	wrap, err := spec.WrapFunc()
	if err != nil {
		return persistence.StartResult{RC: 1}, err
	}

	var paneID string
	if spec.Headless {
		paneID, err = launchHeadless(spec.Agent, wrap)
	} else {
		paneID, err = launchSplit(spec.Position, wrap)
	}
	if err != nil {
		return persistence.StartResult{RC: 1}, err
	}

	handle := NewHandle(spec.Slug, paneID)

	if !spec.Verify {
		return persistence.StartResult{Handle: handle, RC: 0}, nil
	}

	timeout := tmuxctl.EnvVerifyTimeout()
	backoff, err := tmuxctl.EnvVerifyBackoff()
	if err != nil {
		return persistence.StartResult{Handle: handle, RC: 1}, err
	}
	res := tmuxctl.Verify(tmuxctl.VerifyOpts{
		PaneID:  paneID,
		Agent:   spec.Agent,
		Timeout: timeout,
		Backoff: backoff,
		Tmux:    &tmuxctl.RealTmux{},
	})
	totalAttempts := len(backoff)
	switch res.State {
	case tmuxctl.StateReady:
		fmt.Fprintf(os.Stderr, "orch spawn: agent ready in pane %s (attempt %d/%d, %ds)\n",
			paneID, res.Attempts, totalAttempts, int(res.Elapsed.Seconds()))
		return persistence.StartResult{Handle: handle, RC: 0}, nil
	case tmuxctl.StateDied:
		fmt.Fprintf(os.Stderr, "orch spawn: agent failed to start in %s (pane died, attempt %d/%d)\n",
			paneID, res.Attempts, totalAttempts)
		return persistence.StartResult{Handle: handle, RC: 1}, nil
	case tmuxctl.StateMissingBinary:
		fmt.Fprintf(os.Stderr, "orch spawn: agent failed to start in %s (%s binary missing — install the harness CLI; verify failed after %d attempts)\n",
			paneID, spec.Agent, res.Attempts)
		return persistence.StartResult{Handle: handle, RC: 1}, nil
	default:
		fmt.Fprintf(os.Stderr, "orch spawn: agent failed to start in %s (verify failed after %d attempts, timeout after %ds; set ORCH_VERIFY_TIMEOUT or ORCH_VERIFY_BACKOFF, or pass --no-verify to skip)\n",
			paneID, res.Attempts, int(res.Elapsed.Seconds()))
		return persistence.StartResult{Handle: handle, RC: 1}, nil
	}
}

// Attach is unimplemented in Phase 1 — no caller yet. Returns
// persistence.ErrNotImplemented so future `orch attach <slug>` work has
// a typed sentinel to switch on.
func (*Engine) Attach(slug string) (instance.Handle, error) {
	return nil, persistence.ErrNotImplemented
}

// List is unimplemented in Phase 1 — same rationale as Attach.
func (*Engine) List() ([]instance.Handle, error) {
	return nil, persistence.ErrNotImplemented
}

// launchHeadless creates / appends to the orch-headless tmux session
// and returns the new pane id.
func launchHeadless(agent, wrap string) (string, error) {
	session := os.Getenv("ORCH_HEADLESS_SESSION")
	if session == "" {
		session = "orch-headless"
	}
	hasErr := exec.Command("tmux", "has-session", "-t", session).Run()
	if hasErr == nil {
		out, err := exec.Command("tmux", "new-window", "-d", "-t", session+":", "-n", agent, "-P", "-F", "#{pane_id}", wrap).Output()
		if err != nil {
			return "", fmt.Errorf("orch spawn: tmux new-window: %w", err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	out, err := exec.Command("tmux", "new-session", "-d", "-s", session, "-n", agent, "-P", "-F", "#{pane_id}", wrap).Output()
	if err != nil {
		return "", fmt.Errorf("orch spawn: tmux new-session: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// launchSplit splits the current pane according to position and returns
// the new pane id.
func launchSplit(position, wrap string) (string, error) {
	cur := os.Getenv("TMUX_PANE")
	if cur == "" {
		out, err := exec.Command("tmux", "display", "-p", "#{pane_id}").Output()
		if err != nil {
			return "", fmt.Errorf("orch spawn: tmux display: %w", err)
		}
		cur = strings.TrimSpace(string(out))
	}
	var splitArgs []string
	switch position {
	case "right":
		splitArgs = []string{"-h"}
	case "left":
		splitArgs = []string{"-h", "-b"}
	case "above":
		splitArgs = []string{"-v", "-b"}
	case "below":
		splitArgs = []string{"-v"}
	default:
		splitArgs = []string{"-h"}
	}
	args := append([]string{"split-window", "-d"}, splitArgs...)
	args = append(args, "-P", "-F", "#{pane_id}", "-t", cur, wrap)
	out, err := exec.Command("tmux", args...).Output()
	if err != nil {
		return "", fmt.Errorf("orch spawn: tmux split-window: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
