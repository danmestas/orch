package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// runSpy is `orch spy`. Replaces bin/orch-spy.
//
//	orch spy <target> <mission words...>
//	orch spy <target> --mission-file <path>
//	orch spy <target> -                       # mission from stdin
//
// Spawns a `claude --outfit stasi --cut wait-watch` observer pane and
// sends it a standardised brief envelope pointing at the target's
// transcript JSONL, harness state, and reporting-back instructions.
//
// Target: either "operator" / "op" (the worker with metadata.role ==
// "operator") or "%<pane_id>".
//
// Output contract: stdout is exactly one line — the new spy's pane id.
// Diagnostics on stderr.
var spyPaneRE = regexp.MustCompile(`^%[0-9]+$`)

func runSpy(args []string) error {
	// Re-order argv so flags that appear after the positional target
	// (e.g. `orch spy operator --mission-file FOO`) still parse — Go's
	// flag package stops at the first non-flag, but bin/orch-spy allowed
	// flags anywhere. Walk args once, separate into flag tokens (and
	// their values) vs positional tokens, then concatenate flags first.
	args = reorderSpyArgs(args)

	fs := flag.NewFlagSet("spy", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var (
		quiet       = fs.Bool("quiet", false, "suppress stderr; stdout still carries pane id on success")
		headed      = fs.Bool("headed", false, "spawn in current tmux window (default: headless)")
		headless    = fs.Bool("headless", false, "(default) spawn headless")
		missionFile = fs.String("mission-file", "", "read mission text from file (overrides positional)")
		dryRun      = fs.Bool("dry-run-brief", false, "print the brief instead of spawning the spy")
		natsURL     = fs.String("nats", "", "NATS URL override")
	)
	_ = headless // accepted for symmetry with bin/orch-spy; default is headless
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "orch spy:", err)
		return &exitError{code: 1}
	}
	if *quiet {
		// Redirect stderr to /dev/null so diagnostic prints below
		// drop on the floor. We can't set os.Stderr=nil because the
		// fmt.Fprintln calls would panic on a nil writer.
		if devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0); err == nil {
			os.Stderr = devNull
		}
	}

	rest := fs.Args()
	if len(rest) < 1 {
		fmt.Fprintln(os.Stderr, "orch spy: target required (use 'operator' or '%<pane_id>')")
		return &exitError{code: 1}
	}
	target := rest[0]
	missionParts := rest[1:]

	// Validate target shape before doing the registry lookup. bin/orch-spy
	// accepted only "operator" / "op" / "%<digits>" — anything else got
	// a clear "target must be 'operator' or %pane_id" error rather than
	// the generic registry miss. Preserve that UX.
	if target != "operator" && target != "op" && !spyPaneRE.MatchString(target) {
		fmt.Fprintf(os.Stderr, "orch spy: target must be 'operator' or %%<pane_id> (got: %s)\n", target)
		return &exitError{code: 1}
	}

	// Mission resolution: --mission-file > stdin (single "-") > positional words.
	// Done BEFORE the registry snapshot so a missing mission file fails fast
	// with the path-specific error, even when the registry is empty.
	var mission string
	switch {
	case *missionFile != "":
		b, err := os.ReadFile(*missionFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "orch spy: mission file not found:", *missionFile)
			return &exitError{code: 1}
		}
		mission = string(b)
	case len(missionParts) == 1 && missionParts[0] == "-":
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, "orch spy: read stdin:", err)
			return &exitError{code: 1}
		}
		mission = string(b)
	default:
		mission = strings.Join(missionParts, " ")
	}
	if strings.TrimSpace(mission) == "" {
		fmt.Fprintln(os.Stderr, "orch spy: mission text required (positional, --mission-file, or -)")
		return &exitError{code: 1}
	}

	// Pre-flight checks. The bash equivalent gated on `suit` being on
	// PATH so the stasi outfit could be prepared; we keep that gate
	// unless the operator-set escape hatch is on (ORCH_SPY_SKIP_PRECHECK).
	if os.Getenv("ORCH_SPY_SKIP_PRECHECK") != "1" {
		if _, err := exec.LookPath("suit"); err != nil {
			fmt.Fprintln(os.Stderr, "orch spy: suit not on PATH — install @agent-ops/suit and an outfit pack containing the 'stasi' outfit (see README 'Optional outfit support')")
			return &exitError{code: 1}
		}
	}
	if _, err := exec.LookPath("orch-spawn"); err != nil {
		fmt.Fprintln(os.Stderr, "orch spy: orch-spawn not on PATH")
		return &exitError{code: 1}
	}

	// Resolve target via the in-process registry (replaces the bash
	// `orch-registry lookup` shell-out in bin/orch-spy).
	nc, err := connectNATS(*natsURL, "orch-spy")
	if err != nil {
		return err
	}
	if nc != nil {
		defer nc.Close()
	}
	workers, err := snapshotOnce(context.Background(), nc)
	if err != nil {
		return fmt.Errorf("registry snapshot: %w", err)
	}
	worker, ok := lookupTarget(workers, target)
	if !ok {
		if target == "operator" || target == "op" {
			fmt.Fprintln(os.Stderr, "orch spy: no operator agent in the orch registry — set ORCH_ROLE=operator in your shell so orch-agent-shim registers it")
		} else {
			fmt.Fprintf(os.Stderr, "orch spy: %s not in the orch registry — confirm orch-agent-shim is running for that pane\n", target)
		}
		return &exitError{code: 1}
	}
	if worker.CWD == "" {
		fmt.Fprintf(os.Stderr, "orch spy: %s has no metadata.cwd\n", worker.PaneID)
		return &exitError{code: 1}
	}
	transcript, _ := latestTranscriptForCWD(worker.CWD)
	if transcript == "" {
		fmt.Fprintf(os.Stderr, "orch spy: no Claude transcript JSONL under %s/<encoded %s>\n", projectsDir(), worker.CWD)
		return &exitError{code: 1}
	}

	// Build the standard brief envelope.
	role := worker.Role
	if role == "" {
		role = "worker"
	}
	spyPane := "<unspawned>"
	brief := buildSpyBrief(spyBriefInputs{
		Mission:    mission,
		TargetKind: role,
		TargetPane: worker.PaneID,
		Transcript: transcript,
		TargetCWD:  worker.CWD,
		SpyPane:    spyPane,
		SendLog:    sendLogPath(),
		StopDir:    stopDirPath(),
	})

	if *dryRun {
		fmt.Println(brief)
		return nil
	}

	// Spawn the spy pane (claude + stasi/wait-watch). ADR-0002 / orch-spawn
	// auto-tags role=observer when the outfit is stasi.
	spawnArgs := []string{"claude", "--outfit", "stasi", "--cut", "wait-watch", "--no-fleet"}
	// Default to headless unless --headed.
	if !*headed {
		spawnArgs = append(spawnArgs, "--headless")
	}
	spawnArgs = append(spawnArgs, "--cwd", worker.CWD)
	out, err := exec.Command("orch-spawn", spawnArgs...).CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "orch spy: orch-spawn failed: %s\n", strings.TrimSpace(string(out)))
		return &exitError{code: 1}
	}
	spyPane = strings.TrimSpace(string(out))
	if !spyPaneRE.MatchString(spyPane) {
		fmt.Fprintf(os.Stderr, "orch spy: orch-spawn returned non-pane: %s\n", spyPane)
		return &exitError{code: 1}
	}

	// Re-render the brief with the real spy pane id (kept as a second
	// pass so the dry-run path can show the canonical envelope before
	// spawning).
	brief = buildSpyBrief(spyBriefInputs{
		Mission:    mission,
		TargetKind: role,
		TargetPane: worker.PaneID,
		Transcript: transcript,
		TargetCWD:  worker.CWD,
		SpyPane:    spyPane,
		SendLog:    sendLogPath(),
		StopDir:    stopDirPath(),
	})

	// Send the brief via the in-process tell handler.
	if err := runTell([]string{spyPane, brief}); err != nil {
		fmt.Fprintf(os.Stderr, "orch spy: orch tell to %s failed (spy spawned but unbriefed)\n", spyPane)
		return &exitError{code: 1}
	}

	fmt.Println(spyPane)
	return nil
}

type spyBriefInputs struct {
	Mission    string
	TargetKind string
	TargetPane string
	Transcript string
	TargetCWD  string
	SpyPane    string
	SendLog    string
	StopDir    string
}

// buildSpyBrief assembles the standard brief envelope. The text is
// byte-identical to bin/orch-spy's heredoc (modulo whitespace) so
// downstream agents whose prompts grep for marker lines (== Mission ==,
// target_pane_id, etc.) keep working.
func buildSpyBrief(in spyBriefInputs) string {
	return fmt.Sprintf(`You are a stasi/wait-watch observer spawned via orch spy.

You are role=observer on $SRV.INFO.agents (the Synadia Agent Protocol
service registry). Bus subscribers (including the operator) filter
observers out by metadata.role, so your events do not wake the
operator's default subscriber. Workers cannot tell you (worker→observer
is refused unless --force). You report to the operator via
`+"`"+`orch tell`+"`"+`; you do not redirect the target.

== Mission ==
%s

== Target ==
target_kind:             %s
target_pane_id:          %s
target_transcript_jsonl: %s
target_cwd:              %s

== Harness state pointers (read-only, useful for cross-checks) ==
agent_registry:          $SRV.INFO.agents (NATS micro service)
send_log:                %s
event_log_dir:           %s

== Your pane ==
spy_pane_id:             %s

== Reporting back ==
Tail the target transcript JSONL with a Monitor for line-by-line push.
When you observe something the operator should act on, push via
`+"`"+`orch tell <operator-pane> "<finding>"`+"`"+` (use the operator agent on
$SRV.INFO.agents — metadata.role=="operator" — if target_kind != operator).
Do not auto-redirect anything.

Begin.
`,
		in.Mission,
		in.TargetKind, in.TargetPane, in.Transcript, in.TargetCWD,
		in.SendLog, in.StopDir,
		in.SpyPane,
	)
}

func sendLogPath() string {
	if p := os.Getenv("ORCH_SEND_LOG"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "orch-send.log")
}

func stopDirPath() string {
	if p := os.Getenv("ORCH_STOP_DIR"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "orch-stop")
}

// spyFlagsWithValue lists the spy flags that take a value (consume the
// next argv token). Anything not in this set is treated as a bool flag
// (no value to consume) by reorderSpyArgs.
var spyFlagsWithValue = map[string]bool{
	"--mission-file": true,
	"--nats":         true,
}

// reorderSpyArgs walks the argv once and groups flag tokens at the
// front, positional tokens at the back. bin/orch-spy used a while-loop
// with `case $1 in --flag) ... ;; *) ARGS+=("$1") ;; esac` to allow
// flags anywhere in argv. Go's flag package stops parsing at the first
// non-flag, so we replicate the bash UX by moving flags forward.
//
// Inputs like `["--dry-run-brief", "operator", "--mission-file", "M"]`
// become `["--dry-run-brief", "--mission-file", "M", "operator"]`.
func reorderSpyArgs(args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			// Conventional argv terminator — everything after is positional.
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(a) > 0 && a[0] == '-' && a != "-" {
			flags = append(flags, a)
			// If this flag takes a value, consume the next token too.
			// --flag=value form already carries the value in-line.
			base := a
			if eq := strings.IndexByte(a, '='); eq >= 0 {
				base = a[:eq]
			}
			if spyFlagsWithValue[base] && !strings.ContainsRune(a, '=') && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positional = append(positional, a)
	}
	return append(flags, positional...)
}
