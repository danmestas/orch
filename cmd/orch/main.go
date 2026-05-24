// orch — top-level subcommand dispatcher.
//
// Built-in Go subcommands (this binary handles them in-process):
//
//	orch tell  <worker> <prompt>     Publish a prompt to a worker
//	orch ask   <worker> <prompt>     Like tell --collect: stream response back
//	orch peek  [pane...]             Status snapshot of live workers
//	orch spy   <target> <mission>    Spawn an observer pane on a target
//	orch migrate-aliases             Suggest sed rewrites for shell config
//	                                 files that reference the retired
//	                                 bash CLIs (orch-tell, orch-peek, ...)
//
// Anything else is forwarded to a sibling binary named "orch-<sub>" via
// exec. This preserves the legacy dispatch shape:
//
//	orch up        → exec orch-up
//	orch down      → exec orch-down
//	orch spawn ... → exec orch-spawn ...
//	orch version   → exec orch-version
//	orch <new>     → exec orch-<new>  (any future sibling binary)
//
// The bash bin/orch dispatcher that previously did the same forwarding
// has been deleted; npm/package.json now installs this Go binary as
// bin/orch via the standard cmd/orch → bin/orch build hook.
//
// Issue #189 friction points 1 + 3:
//
//   - #1: collapse orch-tell / orch-peek / orch-spy / orch-ask bash
//     scripts (≈920 LoC) into Go subcommands here so filter/alias/
//     observer-exclusion rules concentrate in internal/registry rather
//     than being duplicated in every bash entrypoint.
//   - #3: protocol constants moved to internal/synadia; subcommands
//     import them rather than embedding magic numbers.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

func main() {
	if len(os.Args) < 2 {
		usage(os.Stdout)
		return
	}
	switch os.Args[1] {
	case "-h", "--help", "help":
		usage(os.Stdout)
		return
	}

	sub := os.Args[1]
	args := os.Args[2:]

	if err := dispatch(sub, args); err != nil {
		// Subcommands return errors with embedded exit codes via
		// *exitError; fall back to 1 otherwise.
		var ee *exitError
		if errors.As(err, &ee) {
			if ee.msg != "" {
				fmt.Fprintln(os.Stderr, "orch:", ee.msg)
			}
			os.Exit(ee.code)
		}
		fmt.Fprintln(os.Stderr, "orch:", err)
		os.Exit(1)
	}
}

func dispatch(sub string, args []string) error {
	switch sub {
	case "tell":
		return runTell(args)
	case "ask":
		return runAsk(args)
	case "peek":
		return runPeek(args)
	case "spy":
		return runSpy(args)
	case "migrate-aliases":
		return runMigrateAliases(args)
	case "spawn":
		return runSpawn(args)
	default:
		return execSibling(sub, args)
	}
}

// execSibling looks for orch-<sub> on PATH and execs it with the
// supplied args. This preserves the legacy dispatch model for the bash
// subcommands (orch up, orch down, orch spawn, orch version, ...).
func execSibling(sub string, args []string) error {
	target := "orch-" + sub
	path, err := exec.LookPath(target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "orch: unknown subcommand %q\n", sub)
		fmt.Fprintf(os.Stderr, "      (no built-in handler and no binary named %q on PATH)\n", target)
		fmt.Fprintf(os.Stderr, "      run 'orch --help' for available subcommands\n")
		return &exitError{code: 1}
	}
	// Use Cmd.Run so we forward signals correctly across platforms.
	// (os.Exec / syscall.Exec would be slightly leaner but tangles
	// signal handling on darwin's behavior with tmux split shells.)
	cmd := exec.Command(path, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return &exitError{code: ee.ExitCode()}
		}
		return &exitError{code: 1, msg: err.Error()}
	}
	return nil
}

// exitError lets subcommand handlers signal an exit code without losing
// the wrapped diagnostic. main inspects errors.As(*exitError) to set the
// process exit code.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

func usage(w *os.File) {
	const help = `orch — lightweight, extensible substrate for autonomous long-running
multi-agent coordination on tmux.

Built-in subcommands:
  orch tell <worker> <prompt>       Publish a prompt to a worker via NATS
  orch ask  <worker> <prompt>       Like tell --collect; stream response
                                    chunks back to stdout
  orch peek [--json] [pane...]      Status snapshot of live worker panes
  orch spy  <target> <mission>      Spawn a stasi/wait-watch observer
  orch spawn <agent> [flags...]     Launch a worker pane (claude|pi|codex|gemini)
  orch migrate-aliases              Print sed-style rewrites to migrate
                                    shell config files off the retired
                                    bin/orch-tell etc. bash CLIs

Forwarded subcommands (look for an "orch-<sub>" binary on PATH):
  orch up                           Complete the install on this machine
  orch down                         Tear down install state
  orch version                      Binary version + drift report
  orch <anything else>              exec orch-<anything else>

Run 'orch <subcommand> --help' for subcommand details.
`
	fmt.Fprint(w, help)
}
