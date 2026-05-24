package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

// runAsk is `orch ask` — request/reply with chunk streaming.
//
// Replaces bin/orch-ask (56 lines bash), which was itself a thin wrapper
// around bin/orch-tell --collect. Same behaviour, same exit codes
// (mapped via internal/synadia.ExitCodeForServiceError).
//
//	orch ask [--quiet] [--timeout N] <pane|alias> <prompt>
//	orch ask <pane|alias> -                          # prompt from stdin
func runAsk(args []string) error {
	fs := flag.NewFlagSet("ask", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var (
		timeout = fs.Int("timeout", 30, "inactivity window between chunks (seconds)")
		quiet   = fs.Bool("quiet", false, "suppress status lines on stderr")
		natsURL = fs.String("nats", "", "NATS URL override")
	)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "orch ask:", err)
		return &exitError{code: 1}
	}
	rest := fs.Args()
	if len(rest) < 2 {
		fmt.Fprintln(os.Stderr, "orch ask: usage: orch ask [--timeout N] [--quiet] <pane|alias> <prompt>")
		return &exitError{code: 1}
	}

	if !*quiet {
		fmt.Fprintf(os.Stderr, "orch ask: sending to %s via NATS\n", rest[0])
	}

	// Delegate to the tell handler in --collect mode. Building the
	// argv ourselves (rather than recursing into runTell) keeps the
	// CLI parser's "see what was actually invoked" property intact
	// for downstream diagnostic logs.
	tellArgs := []string{"--collect", "--timeout", fmt.Sprintf("%d", *timeout)}
	if *natsURL != "" {
		tellArgs = append(tellArgs, "--nats", *natsURL)
	}
	tellArgs = append(tellArgs, rest...)
	return runTell(tellArgs)
}
