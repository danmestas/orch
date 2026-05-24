package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// spawnPaneCmux is the cmux-engine leg of `orch spawn`. Mirrors
// spawn_tmux.go's spawnPane shape but drives cmux via its CLI.
//
// cmux conflates pane creation and command execution: `cmux new-pane`
// creates a blank terminal surface and prints `OK surface:N pane:N
// workspace:N` to stdout. After the pane exists we shell the WRAP into
// it with `cmux send --surface surface:N -- "<wrap>\n"`. Unlike tmux's
// `split-window <wrap>`, the WRAP and the surface creation are two
// separate calls — there's no "exec on spawn" verb.
//
// No Engine/Handle interface is extracted here; per Proposal 0008's
// updated status (interfaces deferred to Rule-of-Three when a third
// engine — zmx is the named candidate — lands). The composition table
// in internal/persistence/registry.go is the dispatch authority.
//
// Returns:
//   - paneID: cmux surface ref ("surface:N"), empty when spawn failed
//     before cmux produced one.
//   - spawnRC: exit code to honor (always 0 for the happy path; --verify
//     is not yet supported on cmux and returns 1 with a clear error).
//   - err: a non-recoverable error (cmux missing, unknown agent, etc.).
func (o *spawnOpts) spawnPaneCmux() (string, int, error) {
	// Flag-level validation first — these don't need cmux to be
	// installed and should give the same diagnostic regardless of
	// environment.
	if o.Headless {
		return "", 1, fmt.Errorf("orch spawn: --headless is not supported with --persistence=cmux (cmux has no headless-session concept; rerun without --headless or use --persistence=tmux)")
	}

	if _, err := exec.LookPath("cmux"); err != nil {
		return "", 1, fmt.Errorf("orch spawn: cmux not on PATH — install cmux from https://cmux.app or add /Applications/cmux.app/Contents/Resources/bin to PATH")
	}

	wrap, err := o.buildWrap()
	if err != nil {
		return "", 1, err
	}

	paneID, err := cmuxLaunchSplit(o.Position)
	if err != nil {
		return "", 1, err
	}

	if err := cmuxSendWrap(paneID, wrap); err != nil {
		// Pane exists but we couldn't feed the wrap. Best-effort
		// cleanup so we don't leak an empty pane.
		_ = exec.Command("cmux", "close-surface", "--surface", paneID).Run()
		return "", 1, err
	}

	if !o.Verify {
		return paneID, 0, nil
	}

	// --verify on cmux: deferred. tmuxctl.Verify is tmux-specific
	// (probes via list-panes / display -p / capture-pane with the tmux
	// runner). A cmuxctl equivalent earns its keep when we have a
	// second cmux operator workflow asking for it.
	fmt.Fprintf(os.Stderr,
		"orch spawn: --verify is not yet implemented for --persistence=cmux (pane %s spawned, but readiness probe skipped; pass --no-verify explicitly to silence this, or use --persistence=tmux for verify support)\n",
		paneID)
	return paneID, 1, nil
}

// cmuxLaunchSplit calls `cmux new-pane --direction <mapped> --focus
// false` and returns the new surface ref ("surface:N"). cmux's stdout
// format is `OK surface:N pane:N workspace:N`; we extract the surface
// token because send / capture-pane / close-surface all key off it.
func cmuxLaunchSplit(position string) (string, error) {
	dir := cmuxDirection(position)
	args := []string{"new-pane", "--direction", dir, "--focus", "false"}
	out, err := exec.Command("cmux", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("orch spawn: cmux new-pane: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	surface, parseErr := parseCmuxSurface(string(out))
	if parseErr != nil {
		return "", fmt.Errorf("orch spawn: cmux new-pane returned unexpected output: %s (%w)", strings.TrimSpace(string(out)), parseErr)
	}
	return surface, nil
}

// cmuxDirection maps orch's --position (right|left|above|below) onto
// cmux's --direction (right|left|up|down). Unknown positions fall back
// to "right" — same convention as launchSplit in spawn_tmux.go.
func cmuxDirection(position string) string {
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

// parseCmuxSurface extracts "surface:N" from cmux new-pane output.
// Expected shape: "OK surface:30 pane:25 workspace:2\n" (refs may be
// integers or UUIDs depending on --id-format; we don't pass that flag,
// so refs are the documented default).
func parseCmuxSurface(out string) (string, error) {
	out = strings.TrimSpace(out)
	if out == "" {
		return "", fmt.Errorf("empty output")
	}
	for _, tok := range strings.Fields(out) {
		if strings.HasPrefix(tok, "surface:") {
			// Reject the bare prefix with no id.
			if len(tok) == len("surface:") {
				return "", fmt.Errorf("surface token missing id")
			}
			return tok, nil
		}
	}
	return "", fmt.Errorf("no surface:<id> token found")
}

// cmuxSendWrap feeds the WRAP string into the target surface followed
// by a newline (cmux send takes `\n` as the literal escape sequence per
// `cmux send --help`).
//
// We pass the wrap via `--` so any leading dashes in the wrap don't
// confuse cmux's flag parser. The trailing `\n` triggers the shell to
// execute the line.
func cmuxSendWrap(surface, wrap string) error {
	if surface == "" {
		return fmt.Errorf("cmuxSendWrap: empty surface")
	}
	payload := wrap + `\n`
	out, err := exec.Command("cmux", "send", "--surface", surface, "--", payload).CombinedOutput()
	if err != nil {
		return fmt.Errorf("orch spawn: cmux send: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
