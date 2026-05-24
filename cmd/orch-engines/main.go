// Command orch-engines is the Go-side dispatcher surface for the
// pluggable persistence / layout engines introduced by Proposal 0008.
//
// Phase A subcommands:
//
//	orch-engines validate <persistence> <layout>
//	    Exits 0 when the composition is in the closed registry;
//	    exits 1 with a diagnostic on stderr otherwise. Intended for
//	    bash bin/orch-spawn to call at flag-parse time.
//
//	orch-engines list
//	    Prints supported (persistence, layout) pairs, one per line,
//	    as `persistence=tmux layout=tmux`. Useful in docs and tests.
//
// Phase C will add a `spawn` subcommand that drives the engine chain
// end-to-end. We deliberately stop short of that here per the brief —
// the seam earns its keep without owning the spawn hot path yet.
package main

import (
	"fmt"
	"os"

	"github.com/danmestas/orch/internal/persistence"
)

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "validate":
		os.Exit(runValidate(os.Args[2:]))
	case "list":
		os.Exit(runList())
	case "-h", "--help", "help":
		usage(os.Stdout)
		return
	default:
		fmt.Fprintf(os.Stderr, "orch-engines: unknown subcommand %q\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(2)
	}
}

func usage(w *os.File) {
	fmt.Fprintln(w, "usage:")
	fmt.Fprintln(w, "  orch-engines validate <persistence> <layout>")
	fmt.Fprintln(w, "  orch-engines list")
}

func runValidate(args []string) int {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "orch-engines validate: need exactly <persistence> <layout>")
		return 2
	}
	if err := persistence.RequirePair(args[0], args[1]); err != nil {
		fmt.Fprintf(os.Stderr, "orch-engines: %v\n", err)
		return 1
	}
	return 0
}

func runList() int {
	for _, p := range persistence.SupportedPairs() {
		fmt.Printf("persistence=%s layout=%s\n", p.Persistence, p.Layout)
	}
	return 0
}
