// Package tmuxctl provides the tmux helpers needed by cmd/orch/spawn
// (the Go subcommand that replaced bin/orch-spawn + executors/tmux/spawn.sh
// in #189 friction point #2):
//
//   - Readiness polling (title-rename + banner detection with
//     comma-separated backoff sequence, matching the legacy
//     ORCH_VERIFY_TIMEOUT / ORCH_VERIFY_BACKOFF env-var contract).
//   - Adapter probe (orch-agent-shim --agent <foo> --help; exit code 2
//     means "no adapter compiled for that agent" per
//     internal/synadia.AdapterMissingExitCode).
//   - Pane-aliveness / capture helpers used by the readiness loop.
//
// Per the 2026-05-23 design call: no Executor interface yet (defer until
// WASM/CF lands a second backend), so tmuxctl is a flat helper package,
// not an engine. The Phase-A engine scaffolding (internal/persistence/tmux,
// internal/layout/tmux, internal/instance) was deleted along with
// executors/tmux/spawn.sh in this PR — those packages bridged to bash
// that no longer exists, and the seam they defined will be re-introduced
// (with a second concrete implementation) when WASM/CF lands.
package tmuxctl

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/danmestas/orch/internal/synadia"
)

// DefaultVerifyTimeout is the readiness-poll budget when ORCH_VERIFY_TIMEOUT
// is unset. Matches the bash default in executors/tmux/spawn.sh.
const DefaultVerifyTimeout = 60 * time.Second

// DefaultBackoff is the wait sequence between readiness probes when
// ORCH_VERIFY_BACKOFF is unset. Exponential up to the timeout —
// tolerates slow cold starts (heavy outfits + many MCP servers) without
// dragging the failure case out to the full budget.
var DefaultBackoff = []time.Duration{
	1 * time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
}

// agentBanners is the per-agent banner substring used as a second
// readiness signal when the process-title rename lags behind the
// interactive REPL. Banners are matched against the pane's visible
// buffer via tmux capture-pane.
//
// Mirrors the case statement in executors/tmux/spawn.sh. pi has no
// banner verified empirically past its first-run dialog; it falls
// through to title-rename only.
var agentBanners = map[string]string{
	"claude": "Claude Code",
	"gemini": "Gemini CLI",
	"codex":  "OpenAI Codex",
	"pi":     "",
}

// shellNames is the set of process-current-command values tmux reports
// before an agent has renamed its process title. While the pane shows
// one of these, the agent is still booting (or never launched).
var shellNames = map[string]bool{
	"":     true,
	"zsh":  true,
	"bash": true,
	"sh":   true,
	"fish": true,
	"dash": true,
	"ksh":  true,
}

// VerifyState describes how the readiness loop terminated.
type VerifyState int

const (
	// StateReady — the pane's current command moved off the known
	// shell list OR a known banner appeared. Spawn succeeded.
	StateReady VerifyState = iota
	// StateDied — the pane disappeared from tmux mid-poll. The WRAP
	// itself crashed; retrying won't help.
	StateDied
	// StateMissingBinary — the captured pane buffer contained
	// "command not found" for the agent binary. Retrying won't summon
	// the missing harness.
	StateMissingBinary
	// StateTimedOut — exhausted all backoff attempts without a
	// readiness signal. Operator should bump ORCH_VERIFY_TIMEOUT or
	// re-check the spawn args.
	StateTimedOut
)

// VerifyResult is the outcome of a Verify call.
type VerifyResult struct {
	State    VerifyState
	Attempts int           // number of backoff attempts actually made
	Elapsed  time.Duration // wall time from Verify entry to terminal state
}

// TmuxRunner abstracts the tmux invocations Verify needs. Production
// uses RealTmux (exec.Command wrapper); tests inject mock runners that
// return scripted pane states.
type TmuxRunner interface {
	// ListPaneIDs returns the set of currently-alive pane ids
	// ("%64", …) reported by `tmux list-panes -a -F '#{pane_id}'`.
	ListPaneIDs() ([]string, error)
	// CurrentCommand returns pane_current_command for the given
	// pane id. Empty string for a freshly-spawned pane that's still
	// at the shell.
	CurrentCommand(paneID string) (string, error)
	// CapturePane returns the pane's visible buffer text.
	CapturePane(paneID string) (string, error)
}

// RealTmux is the production TmuxRunner — shells out to the tmux binary.
type RealTmux struct {
	// Bin is the tmux binary path. Empty defaults to "tmux".
	Bin string
}

func (r *RealTmux) bin() string {
	if r.Bin != "" {
		return r.Bin
	}
	return "tmux"
}

// ListPaneIDs implements TmuxRunner.
func (r *RealTmux) ListPaneIDs() ([]string, error) {
	out, err := exec.Command(r.bin(), "list-panes", "-a", "-F", "#{pane_id}").Output()
	if err != nil {
		return nil, fmt.Errorf("tmux list-panes: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	ids := make([]string, 0, len(lines))
	for _, l := range lines {
		if l != "" {
			ids = append(ids, l)
		}
	}
	return ids, nil
}

// CurrentCommand implements TmuxRunner.
func (r *RealTmux) CurrentCommand(paneID string) (string, error) {
	out, err := exec.Command(r.bin(), "display", "-p", "-t", paneID, "#{pane_current_command}").Output()
	if err != nil {
		return "", fmt.Errorf("tmux display: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// CapturePane implements TmuxRunner.
func (r *RealTmux) CapturePane(paneID string) (string, error) {
	out, err := exec.Command(r.bin(), "capture-pane", "-p", "-J", "-t", paneID).Output()
	if err != nil {
		return "", fmt.Errorf("tmux capture-pane: %w", err)
	}
	return string(out), nil
}

// VerifyOpts configures a readiness poll. Zero-value-friendly: empty
// Backoff falls back to DefaultBackoff; zero Timeout falls back to
// DefaultVerifyTimeout. PaneID, Agent, and Tmux are required.
type VerifyOpts struct {
	// PaneID is the tmux pane id ("%64") of the freshly-spawned pane.
	PaneID string

	// Agent is the harness name (claude|pi|codex|gemini). Used to pick
	// the readiness banner and to compose the missing-binary regex.
	Agent string

	// Timeout caps the cumulative wall time across all attempts.
	// Zero means DefaultVerifyTimeout.
	Timeout time.Duration

	// Backoff is the wait sequence between attempts. Empty means
	// DefaultBackoff. Each entry is the time slept BEFORE the
	// corresponding attempt; total wall time is bounded by Timeout
	// (the last entry may be truncated to fit).
	Backoff []time.Duration

	// Tmux is the tmux runner used to inspect the pane. RealTmux for
	// production; mocks for tests.
	Tmux TmuxRunner

	// now is an injectable clock for tests. Production code leaves it
	// nil and Verify uses time.Now.
	now func() time.Time

	// sleep is an injectable sleeper for tests. Production code leaves
	// it nil and Verify uses time.Sleep.
	sleep func(time.Duration)
}

// Verify polls the pane for a readiness signal (title-rename or known
// banner) until one of:
//   - StateReady: pane's current command left the shell-name set OR the
//     known banner appeared in the pane buffer.
//   - StateDied: the pane vanished from tmux mid-poll.
//   - StateMissingBinary: the pane buffer carried a shell "command not
//     found" line for Agent.
//   - StateTimedOut: exhausted Backoff without a readiness signal.
//
// Mirrors the verify loop in executors/tmux/spawn.sh lines 193-314.
func Verify(opts VerifyOpts) VerifyResult {
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultVerifyTimeout
	}
	backoff := opts.Backoff
	if len(backoff) == 0 {
		backoff = DefaultBackoff
	}
	now := opts.now
	if now == nil {
		now = time.Now
	}
	sleep := opts.sleep
	if sleep == nil {
		sleep = time.Sleep
	}

	banner := agentBanners[opts.Agent]
	start := now()
	deadline := start.Add(opts.Timeout)

	var (
		state    VerifyState = StateTimedOut
		attempts int
	)

	for _, waitFor := range backoff {
		attempts++
		// Cap the sleep so we never go past the deadline.
		remaining := deadline.Sub(now())
		if remaining <= 0 {
			break
		}
		eff := waitFor
		if eff > remaining {
			eff = remaining
		}
		sleep(eff)

		// Fail-fast: pane gone from tmux.
		if !paneAlive(opts.Tmux, opts.PaneID) {
			state = StateDied
			break
		}

		// Readiness probe — title rename OR banner match.
		cmd, _ := opts.Tmux.CurrentCommand(opts.PaneID)
		if !shellNames[cmd] {
			state = StateReady
			break
		}
		// Still at the shell — check banner.
		buf, _ := opts.Tmux.CapturePane(opts.PaneID)
		if banner != "" && strings.Contains(buf, banner) {
			state = StateReady
			break
		}
		// Fail-fast: missing harness binary surfaces as a shell error.
		if hasMissingBinaryError(buf, opts.Agent) {
			state = StateMissingBinary
			break
		}

		// Bail before the next iter's sleep if we've already missed
		// the deadline.
		if !now().Before(deadline) {
			break
		}
	}

	return VerifyResult{
		State:    state,
		Attempts: attempts,
		Elapsed:  now().Sub(start),
	}
}

// paneAlive returns true if paneID is in the current list-panes output.
// Helper kept separate so the test mock can stub it cheaply.
func paneAlive(tx TmuxRunner, paneID string) bool {
	ids, err := tx.ListPaneIDs()
	if err != nil {
		return false
	}
	for _, id := range ids {
		if id == paneID {
			return true
		}
	}
	return false
}

// hasMissingBinaryError matches the four shell "command not found"
// shapes documented in executors/tmux/spawn.sh line 287:
//   - "<agent>: command not found"           (bash)
//   - "command not found: <agent>"           (zsh)
//   - "<agent>: not found"                   (dash/sh)
//   - "No such file or directory.*<agent>"   (the macOS variant)
func hasMissingBinaryError(buf, agent string) bool {
	if buf == "" || agent == "" {
		return false
	}
	candidates := []string{
		agent + ": command not found",
		"command not found: " + agent,
		agent + ": not found",
	}
	for _, c := range candidates {
		if strings.Contains(buf, c) {
			return true
		}
	}
	// macOS form — "No such file or directory" then somewhere on the
	// same shell error line is the agent name. Cheap substring; if both
	// land in the captured buffer, that's the shape.
	if strings.Contains(buf, "No such file or directory") && strings.Contains(buf, agent) {
		return true
	}
	return false
}

// ParseBackoff parses a comma-separated wait sequence (the
// ORCH_VERIFY_BACKOFF env var contract) into a []time.Duration.
// Accepts integer and decimal seconds; rejects anything else (matches
// the strict check in executors/tmux/spawn.sh lines 236-241).
// Empty input returns DefaultBackoff.
func ParseBackoff(spec string) ([]time.Duration, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return DefaultBackoff, nil
	}
	parts := strings.Split(spec, ",")
	out := make([]time.Duration, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// strconv.ParseFloat handles both "1" and "1.5". The bash
		// validator allows decimals via the awk fallback; we mirror it.
		f, err := strconv.ParseFloat(p, 64)
		if err != nil || f < 0 {
			return nil, fmt.Errorf("ORCH_VERIFY_BACKOFF must be comma-separated non-negative numbers (got: %q)", spec)
		}
		out = append(out, time.Duration(f*float64(time.Second)))
	}
	if len(out) == 0 {
		return DefaultBackoff, nil
	}
	return out, nil
}

// ParseTimeout parses an integer-seconds string (the ORCH_VERIFY_TIMEOUT
// env var contract) into a time.Duration. Empty / unparseable returns
// DefaultVerifyTimeout. Rejecting bad values would surprise operators
// who set the var defensively — match bash's lenient default behaviour.
func ParseTimeout(spec string) time.Duration {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return DefaultVerifyTimeout
	}
	n, err := strconv.Atoi(spec)
	if err != nil || n <= 0 {
		return DefaultVerifyTimeout
	}
	return time.Duration(n) * time.Second
}

// AdapterStatus is the result of ProbeAdapter.
type AdapterStatus int

const (
	// AdapterOK means the shim has a compiled adapter for the agent.
	AdapterOK AdapterStatus = iota
	// AdapterMissing means the shim exited with
	// synadia.AdapterMissingExitCode (2) — no adapter compiled. The
	// spawn proceeds without a shim sidecar and emits a warning.
	AdapterMissing
	// AdapterUnknown means the probe failed for some other reason
	// (binary not on PATH, shim crashed). The caller treats this as
	// "try anyway; the shim's log will surface real errors" — same
	// fallback the bash dispatcher uses.
	AdapterUnknown
)

// ProbeAdapter invokes `orch-agent-shim --agent <agent> --help` and
// classifies the result. Mirrors the probe in bin/orch-spawn lines
// 942-956. The shim's stdout/stderr are discarded — this is a typed
// boolean check, not a diagnostic.
//
// shimBin is the path to the shim binary. Empty means "look up
// orch-agent-shim on PATH"; if not found, returns AdapterUnknown so
// the caller can skip the shim launch entirely (matching bin/orch-spawn
// line 998).
func ProbeAdapter(shimBin, agent string) AdapterStatus {
	if shimBin == "" {
		path, err := exec.LookPath("orch-agent-shim")
		if err != nil {
			return AdapterUnknown
		}
		shimBin = path
	}
	cmd := exec.Command(shimBin, "--agent", agent, "--help")
	cmd.Stdout = nil
	cmd.Stderr = nil
	err := cmd.Run()
	if err == nil {
		return AdapterOK
	}
	if ee, ok := err.(*exec.ExitError); ok {
		if ee.ExitCode() == synadia.AdapterMissingExitCode {
			return AdapterMissing
		}
		// Unexpected exit — fall through to "try anyway".
		return AdapterUnknown
	}
	return AdapterUnknown
}

// CanonicalAgentName maps the orch CLI's harness names to the
// Synadia-canonical names baked into orch-agent-shim's adapter map.
// orch-spawn accepts "claude" as the harness arg; Synadia knows it as
// "claude-code". Mirrors the case statement in bin/orch-spawn lines
// 937-940.
func CanonicalAgentName(agent string) string {
	if agent == "claude" {
		return "claude-code"
	}
	return agent
}

// Env-var lookup helpers. Centralised so the spawn command can use a
// single import for the env contract and tests can override via t.Setenv.

// EnvVerifyTimeout returns the parsed ORCH_VERIFY_TIMEOUT, falling
// back to DefaultVerifyTimeout.
func EnvVerifyTimeout() time.Duration {
	return ParseTimeout(os.Getenv("ORCH_VERIFY_TIMEOUT"))
}

// EnvVerifyBackoff returns the parsed ORCH_VERIFY_BACKOFF, falling
// back to DefaultBackoff. An invalid spec surfaces as an error so the
// spawn fails before any pane work.
func EnvVerifyBackoff() ([]time.Duration, error) {
	return ParseBackoff(os.Getenv("ORCH_VERIFY_BACKOFF"))
}
