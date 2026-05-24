// Package zmx is the zmx persistence engine — the Phase 2 addition
// to Proposal 0008 that spawns worker sessions via `zmx run -d` (or
// `zmx attach -d`) and addresses them by operator-supplied session
// name. zmx is sessions-only: no panes, no splits, no layout. The
// composition table pairs it with `layout=none` (the no-op layout
// surface; operator manages display via their own emulator window).
//
// Self-registers in init() so cmd/orch only needs a blank import to
// activate the engine:
//
//	import _ "github.com/danmestas/orch/internal/persistence/zmx"
//
// zmx CLI surface used (see https://zmx.sh for the upstream docs):
//
//   - zmx run <name> -d <cmd>     // headless detached spawn
//   - zmx attach <name> -d <cmd>  // attach-or-create, detached
//   - zmx history <name>          // scrollback dump (used by --verify)
//   - zmx list [--short]          // session enumeration
//   - zmx kill <name> --force     // terminate (idempotent)
//
// Engine-level constraints:
//   - --position is REJECTED (zmx has no in-session subdivision; the
//     layout axis is `none`).
//   - --headless MAPS to `zmx run -d` (the detached-by-design form).
//   - --verify IS supported via a zmx-history poll for the agent's
//     readiness markers. Cheaper than tmuxctl.Verify (no
//     capture-pane indirection) — zmx exposes scrollback directly.
package zmx

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/danmestas/orch/internal/instance"
	"github.com/danmestas/orch/internal/persistence"
)

func init() {
	persistence.Register(&Engine{})
}

// Engine implements persistence.Engine atop zmx. Stateless — session
// state lives in zmx itself.
//
// zmxBin overrides the resolved binary path; empty means "look up zmx
// on PATH". Tests set zmxBin to a stub script so they don't depend on
// a real zmx install.
type Engine struct {
	zmxBin string
}

// SetZmxBin overrides the zmx binary path for testing. Production
// callers leave this unset.
func (e *Engine) SetZmxBin(path string) { e.zmxBin = path }

// Name returns "zmx".
func (*Engine) Name() string { return "zmx" }

// Start spawns a zmx session per the spec and returns a StartResult.
//
// Identity: the session name is derived from spec.Slug (passed through
// directly — orch's slug regex is a strict subset of what zmx accepts
// as a session name). When spec.Slug is empty, we synthesize
// "orch-anon-<unix>" so collisions are unlikely; operators relying on
// stable identity should always pass --slug per Proposal 0009.
//
// Collision policy: if a session named like ours already exists, zmx's
// `run` reports an error. We surface it with operator-facing guidance.
//
// Returns an error (not just RC=1) when:
//   - spec.Position != "" (zmx has no in-session layout; --position
//     is a category error for this engine)
//   - zmx is not on PATH (and engine.zmxBin not set)
//   - zmx run / attach fails
//
// When verify is requested, polls zmx history for the agent's banner
// and returns RC=0 on success / RC=1 on timeout — same shape as the
// tmux engine.
func (e *Engine) Start(spec persistence.StartSpec) (persistence.StartResult, error) {
	// --position is meaningless under layout=none; reject before any
	// other work so the operator sees a precise error.
	if spec.Position != "" && spec.Position != "right" {
		// spawn.go defaults Position to "right" even when the operator
		// didn't pass --position. We can't tell from spec alone whether
		// "right" was set explicitly or by default, so we accept "right"
		// as the silent default and only reject explicit non-right.
		// (Operators who pass --position=right with --persistence=zmx
		// get the silent pass — same forgiving spirit as the tmux
		// engine's fallback-to-right on unknown positions.)
		return persistence.StartResult{RC: 1}, fmt.Errorf(
			"orch spawn: --position=%s is not supported with --persistence=zmx (zmx has no in-session layout; the operator opens their own emulator window or wraps zmx inside another multiplexer)",
			spec.Position,
		)
	}

	zmxBin, err := e.resolveZmxBin()
	if err != nil {
		return persistence.StartResult{RC: 1}, err
	}

	sessionName := deriveSessionName(spec.Slug)

	// Collision check: refuse to spawn into an existing session name.
	// zmx itself rejects this, but its error text is generic; we give
	// the operator a specific one.
	if e.sessionExists(zmxBin, sessionName) {
		return persistence.StartResult{RC: 1}, fmt.Errorf(
			"orch spawn: zmx session %q is already live (kill it via `zmx kill %s --force` or pick a different --slug)",
			sessionName, sessionName,
		)
	}

	// Wrap is built lazily so an unknown-agent error doesn't pre-empt
	// the engine-precondition diagnostics above (same order tmux/cmux
	// use).
	wrap, err := spec.WrapFunc()
	if err != nil {
		return persistence.StartResult{RC: 1}, err
	}

	// `zmx run <name> -d sh -c "<wrap>"` — sh -c lets us feed a single
	// shell-quoted command string instead of relying on zmx's
	// argument-tokenization of the WRAP. zmx sends the args verbatim to
	// the session PTY's stdin, so quoting is the operator's
	// responsibility; wrapping in sh -c is the simplest contract.
	//
	// --headless and the default path both use `zmx run -d` — zmx's
	// "attached vs detached" distinction is whether the operator's
	// terminal is wired to the session, and orch never attaches the
	// spawning process to the session directly (the operator opens
	// their own `zmx attach <name>` afterwards). So --headless is the
	// same wire as headed for zmx; we keep the flag accepted for
	// parity with the tmux engine, but it's a no-op shape difference.
	args := []string{"run", sessionName, "-d", "sh", "-c", wrap}
	if out, err := exec.Command(zmxBin, args...).CombinedOutput(); err != nil {
		return persistence.StartResult{RC: 1}, fmt.Errorf(
			"orch spawn: zmx run %s -d failed: %w (output: %s)",
			sessionName, err, strings.TrimSpace(string(out)),
		)
	}

	handle := NewHandle(spec.Slug, sessionName, zmxBin)

	if !spec.Verify {
		return persistence.StartResult{Handle: handle, RC: 0}, nil
	}

	// --verify: poll `zmx history <name>` for a readiness marker until
	// timeout. Tmuxctl.Verify uses a capture-pane probe through the
	// tmux server; for zmx we read scrollback directly via the CLI.
	// The agent-specific banner strings live in verifyMarkers.
	timeout := envVerifyTimeout()
	rc, verifyErr := e.verify(zmxBin, sessionName, spec.Agent, timeout)
	if verifyErr != nil {
		// Polling itself failed (zmx binary disappeared, etc.). Surface
		// it but keep the handle so the caller can clean up.
		fmt.Fprintf(os.Stderr, "orch spawn: verify polling errored for zmx session %s: %v\n", sessionName, verifyErr)
		return persistence.StartResult{Handle: handle, RC: 1}, nil
	}
	return persistence.StartResult{Handle: handle, RC: rc}, nil
}

// Attach returns the handle for an existing zmx session keyed by slug
// (which we use directly as the session name). Returns a diagnostic
// error when no such session exists. Unlike tmux + cmux (which return
// persistence.ErrNotImplemented for Attach in Phase 1), zmx ships a
// working Attach because the surface is cheap — `zmx list --short` is
// the only call needed, and orch doesn't yet have an attach caller
// but the operator-facing inspection workflow (which engine `orch ls`
// will eventually drive) wants this on every concrete engine.
func (e *Engine) Attach(slug string) (instance.Handle, error) {
	zmxBin, err := e.resolveZmxBin()
	if err != nil {
		return nil, err
	}
	sessionName := deriveSessionName(slug)
	if !e.sessionExists(zmxBin, sessionName) {
		return nil, fmt.Errorf("zmx.Attach: no session named %q", sessionName)
	}
	return NewHandle(slug, sessionName, zmxBin), nil
}

// List enumerates live zmx sessions and returns one handle per session.
// The slug is unknown to zmx (orch's identity layer); we use the
// session name as both ID and Locator.
func (e *Engine) List() ([]instance.Handle, error) {
	zmxBin, err := e.resolveZmxBin()
	if err != nil {
		return nil, err
	}
	names, err := listSessions(zmxBin)
	if err != nil {
		return nil, err
	}
	out := make([]instance.Handle, 0, len(names))
	for _, n := range names {
		out = append(out, NewHandle(n, n, zmxBin))
	}
	return out, nil
}

// resolveZmxBin returns the configured zmx binary path or looks it up
// on PATH. Returns an actionable error when neither is available.
func (e *Engine) resolveZmxBin() (string, error) {
	if e.zmxBin != "" {
		return e.zmxBin, nil
	}
	p, err := exec.LookPath("zmx")
	if err != nil {
		return "", fmt.Errorf(
			"orch spawn: zmx not on PATH — install zmx (https://zmx.sh) or pass --persistence=tmux/cmux",
		)
	}
	return p, nil
}

// sessionExists reports whether a session of the given name is live.
// Best-effort: any error from `zmx list --short` is treated as
// "doesn't exist" (zmx not running, no permissions, etc.) — we'd
// rather over-spawn than refuse on a transient list failure.
func (e *Engine) sessionExists(zmxBin, name string) bool {
	out, err := exec.Command(zmxBin, "list", "--short").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if strings.TrimSpace(line) == name {
			return true
		}
	}
	return false
}

// listSessions parses `zmx list --short` into a slice of session names.
func listSessions(zmxBin string) ([]string, error) {
	out, err := exec.Command(zmxBin, "list", "--short").Output()
	if err != nil {
		return nil, fmt.Errorf("zmx.List: zmx list --short: %w", err)
	}
	var names []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		s := strings.TrimSpace(line)
		if s != "" {
			names = append(names, s)
		}
	}
	return names, nil
}

// deriveSessionName maps an orch slug into a zmx session name. Empty
// slug gets a unique-ish stand-in so operators without --slug still
// get a working session (though they lose the alias indirection that
// makes slugs useful).
func deriveSessionName(slug string) string {
	if slug == "" {
		return fmt.Sprintf("orch-anon-%d", time.Now().UnixNano())
	}
	return slug
}

// verify polls `zmx history <name>` for one of the agent's readiness
// markers until timeout. Returns (0, nil) on ready, (1, nil) on
// timeout, (1, err) on transient errors that should still abort the
// poll.
//
// Backoff: fixed 1-second poll. zmx history is cheap (a file read on
// the server side), so we don't need exponential backoff.
func (e *Engine) verify(zmxBin, sessionName, agent string, timeout time.Duration) (int, error) {
	markers := verifyMarkers(agent)
	if len(markers) == 0 {
		// No banner known for this agent — skip verify, same as tmux's
		// behavior when the agent isn't in the verify table.
		fmt.Fprintf(os.Stderr, "orch spawn: --verify on zmx has no readiness marker for agent %q; treating as ready immediately\n", agent)
		return 0, nil
	}
	start := time.Now()
	deadline := start.Add(timeout)
	attempts := 0
	for time.Now().Before(deadline) {
		attempts++
		out, err := exec.Command(zmxBin, "history", sessionName).Output()
		if err == nil {
			scroll := string(out)
			for _, m := range markers {
				if strings.Contains(scroll, m) {
					fmt.Fprintf(os.Stderr, "orch spawn: agent ready in zmx session %s (attempt %d, %ds, marker %q)\n",
						sessionName, attempts, int(time.Since(start).Seconds()), m)
					return 0, nil
				}
			}
		}
		// Quick session-died check: if the session disappears mid-poll
		// the agent crashed before banner emit. Surface clearly.
		if !e.sessionExists(zmxBin, sessionName) {
			fmt.Fprintf(os.Stderr, "orch spawn: zmx session %s vanished during verify (agent failed to start; attempt %d)\n", sessionName, attempts)
			return 1, nil
		}
		time.Sleep(pollInterval())
	}
	fmt.Fprintf(os.Stderr, "orch spawn: agent failed to verify in zmx session %s (timeout after %ds, %d attempts; set ORCH_VERIFY_TIMEOUT, or pass --no-verify to skip)\n",
		sessionName, int(timeout.Seconds()), attempts)
	return 1, nil
}

// verifyMarkers returns the readiness banner substrings to look for in
// `zmx history` output for the given agent. Conservative: only banners
// the engine has confirmed appear early in the scrollback.
//
// Pulled out of verify() so tests can override per-agent expectations
// without driving a live agent.
func verifyMarkers(agent string) []string {
	switch agent {
	case "claude":
		// claude's TUI prints "Welcome to Claude Code" or a "?" help
		// hint within the first few hundred bytes. The "?" hint is the
		// most reliable cross-version marker.
		return []string{"Welcome to Claude", "for shortcuts"}
	case "pi":
		// pi's banner uses the prompt symbol after offline init.
		return []string{"pi>", "you can talk to pi"}
	case "codex":
		return []string{"codex>", "codex CLI"}
	case "gemini":
		return []string{"gemini>", "Gemini CLI"}
	default:
		return nil
	}
}

// envVerifyTimeout reads ORCH_VERIFY_TIMEOUT (seconds) with a 30-second
// default — same envvar the tmux engine uses, kept consistent so
// operators only learn one knob.
func envVerifyTimeout() time.Duration {
	if v := os.Getenv("ORCH_VERIFY_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v + "s"); err == nil {
			return d
		}
	}
	return 30 * time.Second
}

// pollInterval is the verify-loop sleep. 1 second matches the
// granularity zmx history's scrollback emit catches.
func pollInterval() time.Duration {
	return time.Second
}
