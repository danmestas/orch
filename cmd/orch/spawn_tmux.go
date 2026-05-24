package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/danmestas/orch/internal/tmuxctl"
)

// Engine-side concerns (split-window / new-session, verify polling)
// live in internal/persistence/tmux. This file now holds only the
// caller-side helpers that are engine-independent:
//
//   - buildWrap / claudeWrap / piWrap / codexWrap / geminiWrap — per-agent
//     command string assembly
//   - labelSlug / readAliasEntry / writeAliasEntry — slug → pane alias
//     bookkeeping (tmux-specific in practice but reachable by name from
//     any engine; future zmx may want its own keying)
//   - maybeLaunchShim — orch-agent-shim launch alongside the pane
//
// buildWrap composes the per-agent WRAP command string the tmux pane
// will run. Mirrors executors/tmux/spawn.sh lines 51-149.
func (o *spawnOpts) buildWrap() (string, error) {
	var wrap string
	switch o.Agent {
	case "claude":
		wrap = o.claudeWrap()
	case "pi":
		wrap = o.piWrap()
	case "codex":
		w, err := o.codexWrap()
		if err != nil {
			return "", err
		}
		wrap = w
	case "gemini":
		wrap = o.geminiWrap()
	default:
		return "", fmt.Errorf("orch spawn: unknown agent: %s (expected claude|pi|codex|gemini)", o.Agent)
	}

	// Wrapper tail: by default, pause-and-shell after the agent exits.
	// ORCH_NO_PAUSE_ON_EXIT=1 drops the tail so CI / test contexts close
	// cleanly when the agent dies (closes #178).
	if os.Getenv("ORCH_NO_PAUSE_ON_EXIT") != "1" {
		wrap = fmt.Sprintf(`%s; echo; echo '[%s exited — press enter]'; read; exec $SHELL -l`, wrap, o.Agent)
	}
	return wrap, nil
}

// claudeWrap builds the claude WRAP string. Two shapes:
//   - With OUTFIT bundle: cwd = project, --add-dir = bundle, merged
//     CLAUDE.md as --append-system-prompt-file, EXIT trap to clean up.
//   - Without OUTFIT: cwd = caller-resolved cwd, no bundle, fleet
//     prompt appended (unless --no-fleet).
//
// Bridge=synadia-plugin appends --dangerously-load-development-channels
// at the end of either shape.
func (o *spawnOpts) claudeWrap() string {
	var wrap string
	if o.Bundle != "" {
		// Merge CLAUDE.md from the bundle + fleet doctrine into one file.
		merged := filepath.Join(o.Bundle, ".orch-merged-prompt.md")
		writeMergedPrompt(merged, o.Bundle, o.NoFleet)

		wrap = fmt.Sprintf(
			`trap 'rm -rf "%s"' EXIT; export ORCH_PANE_ID=$TMUX_PANE;%s%s cd "%s" && claude --dangerously-skip-permissions --add-dir "%s"`,
			o.Bundle, slugExports(o.Slug), goalExports(), o.Cwd, o.Bundle,
		)
		if st, err := os.Stat(merged); err == nil && st.Size() > 0 {
			wrap += fmt.Sprintf(` --append-system-prompt-file "%s"`, merged)
		}
	} else {
		wrap = fmt.Sprintf(
			`export ORCH_PANE_ID=$TMUX_PANE;%s%s cd "%s" && claude --dangerously-skip-permissions`,
			slugExports(o.Slug), goalExports(), o.Cwd,
		)
		if !o.NoFleet {
			wrap += fmt.Sprintf(` --append-system-prompt-file %s`, fleetPromptPath())
		}
	}
	if o.Bridge == "synadia-plugin" {
		wrap += ` --dangerously-load-development-channels 'plugin:nats-channel@synadia-plugins'`
	}
	return wrap
}

// piWrap builds the pi WRAP. PI_TELEMETRY=0 suppresses the post-update
// install-telemetry ping; --offline skips startup HTTP probes.
func (o *spawnOpts) piWrap() string {
	wrap := fmt.Sprintf(
		`export ORCH_PANE_ID=$TMUX_PANE PI_TELEMETRY=0;%s%s cd "%s" && pi --offline`,
		slugExports(o.Slug), goalExports(), o.Cwd,
	)
	if !o.NoFleet {
		wrap += fmt.Sprintf(` --append-system-prompt %s`, fleetPromptPath())
	}
	return wrap
}

// codexWrap builds the codex WRAP. Pre-stages the canonical project
// trust key in ~/.codex/config.toml (idempotent) so codex's trust
// gate doesn't fire on fresh envs. Mirrors executors/tmux/spawn.sh
// lines 95-135.
func (o *spawnOpts) codexWrap() (string, error) {
	canonCwd, err := filepath.EvalSymlinks(o.Cwd)
	if err != nil {
		canonCwd = o.Cwd
	}
	configToml := filepath.Join(os.Getenv("HOME"), ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configToml), 0o755); err != nil {
		return "", err
	}
	if _, err := os.Stat(configToml); os.IsNotExist(err) {
		if f, err := os.Create(configToml); err == nil {
			f.Close()
		}
	}
	needle := fmt.Sprintf("[projects.\"%s\"]", canonCwd)
	if !fileContains(configToml, needle) {
		f, err := os.OpenFile(configToml, os.O_APPEND|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "\n[projects.\"%s\"]\ntrust_level = \"trusted\"\n", canonCwd)
			f.Close()
		}
	}
	return fmt.Sprintf(
		`export ORCH_PANE_ID=$TMUX_PANE;%s%s cd "%s" && codex --disable external_migration --dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust`,
		slugExports(o.Slug), goalExports(), o.Cwd,
	), nil
}

// geminiWrap builds the gemini WRAP. --yolo bypasses confirmation,
// --skip-trust avoids the Folder Trust dialog.
func (o *spawnOpts) geminiWrap() string {
	return fmt.Sprintf(
		`export ORCH_PANE_ID=$TMUX_PANE;%s%s cd "%s" && gemini --yolo --skip-trust`,
		slugExports(o.Slug), goalExports(), o.Cwd,
	)
}

// fileContains returns true if path's contents contain needle.
// Best-effort: errors return false.
func fileContains(path, needle string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(b), needle)
}

// writeMergedPrompt populates the merged prompt file with bundle's
// CLAUDE.md followed by the fleet doctrine. Best-effort.
func writeMergedPrompt(merged, bundle string, noFleet bool) {
	f, err := os.Create(merged)
	if err != nil {
		return
	}
	defer f.Close()
	if bundleClaude, err := os.ReadFile(filepath.Join(bundle, "CLAUDE.md")); err == nil {
		f.Write(bundleClaude)
	}
	if !noFleet {
		fleet := fleetPromptPath()
		if fleetData, err := os.ReadFile(fleet); err == nil {
			// Add a blank-line separator only if the bundle's
			// CLAUDE.md produced content.
			st, _ := f.Stat()
			if st != nil && st.Size() > 0 {
				f.WriteString("\n\n")
			}
			f.Write(fleetData)
		}
	}
}

// labelSlug applies the three layers of slug labeling: pane title,
// alias file write, collision check. Mirrors bin/orch-spawn lines
// 844-901. Returns nil when slug is empty (preserves legacy unsluggy
// shape).
func (o *spawnOpts) labelSlug(paneID string) error {
	if o.Slug == "" {
		return nil
	}
	// Layer 1: pane title — only meaningful for tmux pane ids.
	if strings.HasPrefix(paneID, "%") {
		_ = exec.Command("tmux", "select-pane", "-t", paneID, "-T", o.Slug).Run()
	}
	// Layer 2: alias file. Collision check first.
	aliasesFile := os.Getenv("ORCH_ALIASES_FILE")
	if aliasesFile == "" {
		aliasesFile = filepath.Join(os.Getenv("HOME"), ".config", "orch-aliases")
	}
	if err := os.MkdirAll(filepath.Dir(aliasesFile), 0o755); err != nil {
		return err
	}
	// Touch the file so the awk read below has something to scan.
	if _, err := os.Stat(aliasesFile); os.IsNotExist(err) {
		if f, err := os.Create(aliasesFile); err == nil {
			f.Close()
		}
	}
	existingPane := readAliasEntry(aliasesFile, o.Slug)
	if existingPane != "" && existingPane != paneID && !o.ForceSlug {
		return fmt.Errorf(
			"slug %q is already in use by %s (alias file: %s). Pass --force-slug to take over the alias (the previous worker keeps running but loses its name), or pick a different --instance-id",
			o.Slug, existingPane, aliasesFile,
		)
	}
	return writeAliasEntry(aliasesFile, o.Slug, paneID)
}

// readAliasEntry returns the pane id mapped to slug in path, or empty
// when no such entry exists.
func readAliasEntry(path, slug string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	prefix := slug + "="
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	return ""
}

// writeAliasEntry replaces any prior entry for slug with slug=paneID.
// Atomic via rename-from-tempfile.
func writeAliasEntry(path, slug, paneID string) error {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	lines := strings.Split(string(existing), "\n")
	prefix := slug + "="
	kept := lines[:0]
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, prefix) {
			continue
		}
		kept = append(kept, line)
	}
	kept = append(kept, slug+"="+paneID)
	out := strings.Join(kept, "\n") + "\n"
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(out), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// maybeLaunchShim starts orch-agent-shim alongside the pane unless
// --no-shim or bridge=synadia-plugin (which provides the bridge
// in-process). Mirrors bin/orch-spawn lines 930-1001.
func (o *spawnOpts) maybeLaunchShim(paneID string) {
	if o.Bridge == "synadia-plugin" {
		return // plugin runs inside claude; no sidecar shim
	}
	if o.NoShim {
		return
	}
	shimBin, err := exec.LookPath("orch-agent-shim")
	if err != nil {
		fmt.Fprintln(os.Stderr, "orch spawn: orch-agent-shim not on PATH — spawning without shim; install via npm install @agent-ops/orch to enable bus integration")
		return
	}
	shimAgent := tmuxctl.CanonicalAgentName(o.Agent)
	status := tmuxctl.ProbeAdapter(shimBin, shimAgent)
	if status == tmuxctl.AdapterMissing {
		fmt.Fprintf(os.Stderr, "orch spawn: no shim adapter for %s (adapter-less harness) — spawning without shim; bus integration is disabled for this pane\n", o.Agent)
		return
	}

	owner := os.Getenv("SESH_OWNER")
	if owner == "" {
		owner = os.Getenv("USER")
	}
	if owner == "" {
		if out, err := exec.Command("id", "-un").Output(); err == nil {
			owner = strings.TrimSpace(string(out))
		}
	}
	session := os.Getenv("SESH_SESSION")
	natsURL := os.Getenv("NATS_URL")
	paneSafe := strings.ReplaceAll(paneID, "%", "pct")
	logDir := filepath.Join(os.Getenv("HOME"), ".cache", "orch-shim")
	_ = os.MkdirAll(logDir, 0o755)
	logPath := filepath.Join(logDir, paneSafe+".log")

	shimArgs := []string{"--agent", shimAgent, "--pane", paneID, "--cwd", o.Cwd}
	if o.Slug != "" {
		shimArgs = append(shimArgs, "--instance-id", o.Slug)
	}

	cmd := exec.Command(shimBin, shimArgs...)
	cmd.Env = append(os.Environ(),
		"ORCH_OWNER="+owner,
		"ORCH_OUTFIT="+o.Outfit,
		"ORCH_ROLE="+o.Role,
		"SESH_ROLE="+o.Role,
		"SESH_CLASS="+o.Class,
		"SESH_SESSION="+session,
		"NATS_URL="+natsURL,
	)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		// Fall back to discarding the shim's output entirely.
		cmd.Stdout = nil
		cmd.Stderr = nil
	} else {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "orch spawn: shim launch failed:", err)
		if logFile != nil {
			logFile.Close()
		}
		return
	}
	// Detach so the shim survives the orch-spawn process exit. We
	// intentionally do NOT Wait(); the shim's pane-watchdog manages
	// its own lifetime.
	go func() {
		_ = cmd.Wait()
		if logFile != nil {
			logFile.Close()
		}
	}()
}
