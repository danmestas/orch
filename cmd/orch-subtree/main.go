// orch-subtree — Phase A CLI for the subtree topology yaml dispatcher.
//
// Subcommands available in Phase A (parse + validate + diff + list):
//
//	orch-subtree validate <file>   Parse + validate; exit 0 if valid.
//	orch-subtree diff <file>       Print what apply WOULD change vs the cache.
//	orch-subtree list              List cached subtree names.
//
// Subcommands deferred to Phase B (need wired SeshResolver / WorkerSpawner /
// StateSeeder / LiveRegistry — all NATS-backed):
//
//	orch-subtree apply <file>      Run the 5-phase apply pipeline.
//	orch-subtree status <name>     Compare cached vs live registry.
//	orch-subtree destroy <name>    Kill workers + cleanup.
//	orch-subtree watch <name>      Stream agents.events.> filtered to subtree.
//
// Phase A returns errNotImplemented (exit 3) for the deferred verbs so
// harness code can stably detect "feature not yet wired" without
// parsing free-form messages.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/danmestas/orch/internal/subtree"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		switch {
		case errors.Is(err, errInvalid):
			os.Exit(2)
		case errors.Is(err, errNotImplemented):
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		default:
			fmt.Fprintln(os.Stderr, "orch-subtree:", err)
			os.Exit(1)
		}
	}
}

var (
	errInvalid        = errors.New("topology is invalid")
	errNotImplemented = errors.New("subcommand not implemented in Phase A")
)

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("subcommand required")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "validate":
		return cmdValidate(rest)
	case "diff":
		return cmdDiff(rest)
	case "list":
		return cmdList(rest)
	case "apply", "status", "destroy", "watch":
		return fmt.Errorf("%w: %s (waiting on Phase B — NATS-backed SeshResolver/WorkerSpawner/StateSeeder wiring; see Proposal 0006)", errNotImplemented, sub)
	case "-h", "--help", "help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown subcommand: %s", sub)
	}
}

func usage() {
	const help = `orch-subtree — apply + manage subtree topology YAML (Proposal 0006)

Subcommands (Phase A — parse / validate / diff / list):
  validate <file>             Parse + validate the topology yaml.
                              Exit 0 on valid; 2 on invalid; 1 on parse / IO error.
  diff <file>                 Print what apply WOULD change vs the cached state.
                              Output: one entry per add/remove (worker | task | goal).
  list                        List subtree names with a cached applied.yaml.

Subcommands (Phase B — reserved for the NATS-wired apply pipeline):
  apply <file>                Run the 5-phase pipeline (parse → resolve sesh →
                              spawn workers → seed state → persist).
  status <name>               Compare cached vs live registry.
  destroy <name>              Kill workers + clean up cache.
  watch <name>                Stream agents.events.> filtered to subtree.

Common flags:
  --cache-dir <path>          Override the cache directory. Default:
                              $ORCH_SUBTREE_CACHE_DIR or
                              $XDG_CACHE_HOME/orch-subtrees or
                              ~/.cache/orch-subtrees.
`
	fmt.Fprint(os.Stderr, help)
}

func cmdValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("validate: exactly one yaml path required")
	}
	t, err := subtree.ParseFile(fs.Arg(0))
	if err != nil {
		return err
	}
	subtree.ResolveEnv(t, os.Getenv)
	if err := subtree.Validate(t); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return errInvalid
	}
	fmt.Fprintf(os.Stderr, "%s: ok (%d worker(s), %d task seed(s), %d goal seed(s))\n",
		fs.Arg(0), len(t.Workers), len(t.State.Tasks), len(t.State.Goals))
	return nil
}

func cmdDiff(args []string) error {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cacheDir := fs.String("cache-dir", "", "override cache directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("diff: exactly one yaml path required")
	}
	t, err := subtree.ParseFile(fs.Arg(0))
	if err != nil {
		return err
	}
	subtree.ResolveEnv(t, os.Getenv)

	eng := &subtree.Engine{Cache: subtree.NewFileCache(*cacheDir)}
	entries, err := eng.Diff(t)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Println("(no changes — cached state matches proposed topology)")
		return nil
	}
	for _, e := range entries {
		op := strings.ToUpper(e.Op)
		fmt.Printf("%-6s %-6s %s\n", op, e.Kind, e.Name)
	}
	return nil
}

func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cacheDir := fs.String("cache-dir", "", "override cache directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return errors.New("list: no arguments expected")
	}
	eng := &subtree.Engine{Cache: subtree.NewFileCache(*cacheDir)}
	names, err := eng.List()
	if err != nil {
		return err
	}
	if len(names) == 0 {
		fmt.Fprintln(os.Stderr, "(no subtrees applied yet)")
		return nil
	}
	for _, n := range names {
		fmt.Println(n)
	}
	return nil
}
