// orch-subtree — CLI for Proposal 0006's subtree topology YAML.
//
// Phase A (already shipped, #158): parse + validate + diff + list.
// Phase B (this binary): apply + status + destroy + watch wired to
// live infrastructure (NATS, sesh, sesh-ops, orch-spawn).
//
// Subcommands:
//
//	orch-subtree validate <file>    Parse + validate; exit 0 if valid.
//	orch-subtree diff <file>        Print what apply WOULD change.
//	orch-subtree list               List cached subtree names.
//	orch-subtree apply <file>       Run the 5-phase pipeline live.
//	orch-subtree status <name>      Compare cached vs live registry.
//	orch-subtree destroy <name>     Kill workers + clean up cache.
//	orch-subtree watch <name>       Stream agents.events.> filtered to subtree.
//
// Common flags:
//
//	--cache-dir <path>              Override the cache directory.
//	--nats-url <url>                NATS URL (default $ORCH_NATS_URL or the
//	                                cached subtree's resolved_nats).
//	--purge-state                   destroy only — purge KV state too
//	                                (currently refuses up front, issue #159
//	                                — sesh-ops scope-purge verb still
//	                                pending).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/danmestas/orch/internal/subtree"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		switch {
		case errors.Is(err, errInvalid):
			os.Exit(2)
		default:
			fmt.Fprintln(os.Stderr, "orch-subtree:", err)
			os.Exit(1)
		}
	}
}

var errInvalid = errors.New("topology is invalid")

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
	case "apply":
		return cmdApply(rest)
	case "status":
		return cmdStatus(rest)
	case "destroy":
		return cmdDestroy(rest)
	case "watch":
		return cmdWatch(rest)
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

Subcommands:
  validate <file>             Parse + validate the topology yaml.
                              Exit 0 on valid; 2 on invalid; 1 on parse / IO error.
  diff <file>                 Print what apply WOULD change vs the cached state.
  list                        List subtree names with a cached applied.yaml.
  apply <file>                Run the 5-phase pipeline (parse → resolve sesh →
                              spawn workers → seed state → persist).
  status <name>               Compare cached topology vs live bus registry.
  destroy <name>              Kill workers + clean up cache.
  watch <name>                Stream agents.events.> + agents.hb.> filtered to subtree.

Common flags:
  --cache-dir <path>          Override the cache directory. Default:
                              $ORCH_SUBTREE_CACHE_DIR or
                              $XDG_CACHE_HOME/orch-subtrees or
                              ~/.cache/orch-subtrees.
  --nats-url <url>            NATS URL. Defaults to $ORCH_NATS_URL; on status/
                              destroy/watch falls back to the cached
                              applied.yaml's resolved_nats.
  --purge-state               destroy only — purge KV state in addition to
                              killing workers (currently refuses; see issue #159).
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
	rpt := subtree.Validate(t)
	if s := rpt.String(); s != "" {
		fmt.Fprintln(os.Stderr, s)
	}
	if !rpt.Valid() {
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

func cmdApply(args []string) error {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cacheDir := fs.String("cache-dir", "", "override cache directory")
	natsURL := fs.String("nats-url", "", "NATS URL for live registry queries (defaults to $ORCH_NATS_URL or the resolved sesh URL)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("apply: exactly one yaml path required")
	}
	t, err := subtree.ParseFile(fs.Arg(0))
	if err != nil {
		return err
	}
	subtree.ResolveEnv(t, os.Getenv)

	// Resolve a NATS URL for the registry snapshot up front. The
	// engine itself re-resolves via SeshResolver for the spawn URL;
	// the registry snapshot needs a URL chosen BEFORE we know the
	// resolved sesh URL (so we can detect already-alive workers on
	// the operator's hub). Precedence: flag > $ORCH_NATS_URL >
	// topology's existing-sesh URL (if any).
	resolverURL := *natsURL
	if resolverURL == "" {
		resolverURL = os.Getenv("ORCH_NATS_URL")
	}
	if resolverURL == "" && t.Sesh.Existing != "" {
		resolverURL = t.Sesh.Existing
	}

	var registryImpl subtree.LiveRegistry = subtree.EmptyLiveRegistry()
	var nc *nats.Conn
	if resolverURL != "" {
		c, err := connectNATS(resolverURL)
		if err != nil {
			// Soft-fail: if we can't reach the bus, every worker
			// reads as "not alive" and apply will (re-)spawn them
			// all. That's the safe default — an unreachable bus
			// shouldn't make apply succeed silently.
			fmt.Fprintf(os.Stderr, "orch-subtree apply: registry connect %s: %v (treating all workers as missing)\n", resolverURL, err)
		} else {
			nc = c
			defer nc.Close()
			registryImpl = &subtree.NATSLiveRegistry{NC: nc}
		}
	}

	eng := &subtree.Engine{
		Sesh:     subtree.NewLiveSeshResolver(),
		Registry: registryImpl,
		Spawner:  subtree.NewOrchSpawnWorkerSpawner(),
		Seeder:   subtree.NewSeshOpsStateSeeder(),
		Cache:    subtree.NewFileCache(*cacheDir),
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	res, err := eng.Apply(ctx, t)
	if err != nil {
		return err
	}
	fmt.Printf("apply: subtree %q applied at %s\n", t.Name, time.Now().Format(time.RFC3339))
	fmt.Printf("  sesh: %s", res.ResolvedSesh.URL)
	if res.ResolvedSesh.WeSpawnedIt {
		fmt.Print(" (spawned by this apply)\n")
	} else {
		fmt.Print(" (joined existing)\n")
	}
	if len(res.Spawned) > 0 {
		fmt.Printf("  spawned: %s\n", strings.Join(res.Spawned, ", "))
	}
	if len(res.AlreadyRunning) > 0 {
		fmt.Printf("  already-running: %s\n", strings.Join(res.AlreadyRunning, ", "))
	}
	fmt.Printf("  tasks seeded: %d\n", res.TasksSeeded)
	fmt.Printf("  goals seeded: %d\n", res.GoalsSeeded)
	return nil
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cacheDir := fs.String("cache-dir", "", "override cache directory")
	natsURL := fs.String("nats-url", "", "NATS URL (defaults to the cached resolved_nats or $ORCH_NATS_URL)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("status: exactly one subtree name required")
	}
	name := fs.Arg(0)
	cache := subtree.NewFileCache(*cacheDir)
	applied, err := cache.Read(name)
	if err != nil {
		return err
	}

	url := *natsURL
	if url == "" {
		url = applied.ResolvedNATS
	}
	if url == "" {
		url = os.Getenv("ORCH_NATS_URL")
	}
	if url == "" {
		return fmt.Errorf("status: no NATS URL — set --nats-url or $ORCH_NATS_URL")
	}
	nc, err := connectNATS(url)
	if err != nil {
		return fmt.Errorf("status: NATS connect %s: %w", url, err)
	}
	defer nc.Close()

	eng := &subtree.Engine{
		Cache:    cache,
		Registry: &subtree.NATSLiveRegistry{NC: nc},
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	rep, err := eng.Status(ctx, name)
	if err != nil {
		return err
	}
	fmt.Printf("status: %s (nats: %s)\n", rep.Name, rep.ResolvedNATS)
	for _, w := range rep.Workers {
		state := "alive"
		switch {
		case w.Missing:
			state = "MISSING"
		case w.Extra:
			state = "EXTRA (not in topology)"
		}
		fmt.Printf("  %s\t%s\n", w.Name, state)
	}
	return nil
}

func cmdDestroy(args []string) error {
	fs := flag.NewFlagSet("destroy", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cacheDir := fs.String("cache-dir", "", "override cache directory")
	purge := fs.Bool("purge-state", false, "ALSO purge KV state (currently refuses; see issue #159)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("destroy: exactly one subtree name required")
	}
	name := fs.Arg(0)

	eng := &subtree.Engine{Cache: subtree.NewFileCache(*cacheDir)}
	killer := subtree.NewTmuxWorkerKiller()
	teardown := subtree.NewSeshDownTeardown()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	res, err := eng.Destroy(ctx, name, killer, teardown, subtree.DestroyOptions{PurgeState: *purge})
	if err != nil {
		return err
	}
	for _, n := range res.WorkersKilled {
		fmt.Printf("killed %s\n", n)
	}
	if res.SeshTornDown {
		fmt.Printf("sesh torn down\n")
	}
	if res.CacheRemoved {
		fmt.Printf("cache removed\n")
	}
	if len(res.WorkersKilled) == 0 && !res.CacheRemoved {
		fmt.Printf("(nothing to destroy — subtree %q not in cache)\n", name)
	}
	return nil
}

func cmdWatch(args []string) error {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cacheDir := fs.String("cache-dir", "", "override cache directory")
	natsURL := fs.String("nats-url", "", "NATS URL (defaults to cached resolved_nats or $ORCH_NATS_URL)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("watch: exactly one subtree name required")
	}
	name := fs.Arg(0)
	cache := subtree.NewFileCache(*cacheDir)
	applied, err := cache.Read(name)
	if err != nil {
		return err
	}
	url := *natsURL
	if url == "" {
		url = applied.ResolvedNATS
	}
	if url == "" {
		url = os.Getenv("ORCH_NATS_URL")
	}
	if url == "" {
		return fmt.Errorf("watch: no NATS URL — set --nats-url or $ORCH_NATS_URL")
	}
	nc, err := connectNATS(url)
	if err != nil {
		return fmt.Errorf("watch: NATS connect %s: %w", url, err)
	}
	defer nc.Close()

	stream := subtree.NewNATSEventStream(nc, cache)
	eng := &subtree.Engine{Cache: cache}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	ch, err := eng.Watch(ctx, name, stream)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "watching subtree %q on %s (Ctrl-C to stop)\n", name, url)
	for ev := range ch {
		fmt.Printf("%s\t%s\t%s\t%s\n",
			ev.SubtreeName, ev.Worker, ev.Kind, ev.Payload)
	}
	return nil
}

// connectNATS opens a short-lived NATS connection with a tight
// timeout so CLI commands fail fast on unreachable hubs.
func connectNATS(url string) (*nats.Conn, error) {
	return nats.Connect(url,
		nats.Name("orch-subtree"),
		nats.Timeout(3*time.Second),
		nats.MaxReconnects(0),
	)
}
