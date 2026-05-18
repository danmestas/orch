// orch-registry — operator-side worker registry CLI + optional sidecar.
//
// One-shot:
//
//	orch-registry snapshot              # JSON array of all known workers
//	orch-registry lookup <name|%pane>   # single worker JSON, exit 1 on miss
//
// Sidecar:
//
//	orch-registry serve                 # publishes orch.registry.snapshot every N
//
// All commands read $SRV.INFO.agents from NATS plus the operator's
// ~/.config/orch-aliases. NATS URL resolution mirrors the shim:
// --nats → $NATS_URL → ~/.sesh/hub.url → nats://127.0.0.1:4222.
//
// Consumers:
//
//   - bin/orch-peek shells out to `orch-registry snapshot` for its worker list
//   - bin/orch-tell / orch-ask / orch-spy use `orch-registry lookup` for target resolution
//   - UI / dashboards subscribe to orch.registry.snapshot (sidecar mode) or
//     poll `orch-registry snapshot` (one-shot)
//
// See docs/proposals/0005-operator-registry-consolidation.md.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/danmestas/orch/internal/registry"
	"github.com/danmestas/orch/internal/registry/sources"
	"github.com/danmestas/orch/internal/shim"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "orch-registry: %v\n", err)
		os.Exit(exitCodeFor(err))
	}
}

func run() error {
	if len(os.Args) < 2 {
		usage()
		return errors.New("subcommand required")
	}
	sub, args := os.Args[1], os.Args[2:]
	switch sub {
	case "snapshot":
		return cmdSnapshot(args)
	case "lookup":
		return cmdLookup(args)
	case "serve":
		return cmdServe(args)
	case "-h", "--help", "help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown subcommand: %s", sub)
	}
}

func usage() {
	const help = "orch-registry — operator-side worker registry\n" +
		"\n" +
		"Subcommands:\n" +
		"  snapshot [--nats URL]                  JSON array of all workers (one-shot)\n" +
		"  lookup <name|PERCENTpane> [--nats URL] Single worker JSON; exit 4 on miss\n" +
		"  serve   [--nats URL] [--interval N]    Publish orch.registry.snapshot every N seconds (default 5s)\n" +
		"\n" +
		"Common flags:\n" +
		"  --nats URL          NATS URL override (default $NATS_URL or ~/.sesh/hub.url)\n" +
		"  --alias-file PATH   override ~/.config/orch-aliases\n" +
		"  --operator-file PATH override ~/.cache/orch-operator.json\n" +
		"  --hb-window D       alive threshold (default 90s)\n"
	fmt.Fprint(os.Stderr, help)
}

// exitCodeFor maps a small set of well-known error types to stable exit
// codes. Bin scripts depend on these (orch-tell uses 4 for "not found").
func exitCodeFor(err error) int {
	if errors.Is(err, errNotFound) {
		return 4
	}
	return 1
}

var errNotFound = errors.New("not found")

// commonFlags returns the four readers (and the lifetime context).
// Centralised so every subcommand consults the same env precedence.
func commonFlags(fs *flag.FlagSet, args []string) (*nats.Conn, registry.Readers, time.Duration, func(), error) {
	var (
		natsURL     = fs.String("nats", "", "NATS URL override")
		aliasPath   = fs.String("alias-file", "", "override ~/.config/orch-aliases")
		operatorPth = fs.String("operator-file", "", "override ~/.cache/orch-operator.json")
		hbWindow    = fs.Duration("hb-window", registry.DefaultHeartbeatWindow, "alive threshold")
	)
	if err := fs.Parse(args); err != nil {
		return nil, registry.Readers{}, 0, func() {}, err
	}
	url := shim.ReadNATSURL(*natsURL)
	nc, err := nats.Connect(url, nats.Name("orch-registry"))
	if err != nil {
		return nil, registry.Readers{}, 0, func() {}, fmt.Errorf("connect %s: %w", url, err)
	}
	natsSrc := sources.New(nc, sources.NATSOptions{})
	readers := registry.Readers{
		Agents:     natsSrc,
		Heartbeats: natsSrc,
		Aliases:    sources.NewAliasFile(*aliasPath),
		Operator:   sources.NewOperatorFile(*operatorPth),
	}
	cleanup := func() { nc.Close() }
	return nc, readers, *hbWindow, cleanup, nil
}

// --- snapshot ---------------------------------------------------------

func cmdSnapshot(args []string) error {
	fs := flag.NewFlagSet("snapshot", flag.ContinueOnError)
	_, readers, hbWindow, cleanup, err := commonFlags(fs, args)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	workers, errs := registry.Snapshot(ctx, readers, hbWindow)
	// Non-fatal source errors → stderr, but still emit whatever joined.
	for src, e := range errs {
		fmt.Fprintf(os.Stderr, "orch-registry: source %q: %v\n", src, e)
	}
	if errs.HasFatal() {
		return errs["agents"]
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(toJSONWorkers(workers))
}

// --- lookup -----------------------------------------------------------

func cmdLookup(args []string) error {
	fs := flag.NewFlagSet("lookup", flag.ContinueOnError)
	_, readers, hbWindow, cleanup, err := commonFlags(fs, args)
	if err != nil {
		return err
	}
	defer cleanup()

	rest := fs.Args()
	if len(rest) < 1 {
		return errors.New("usage: orch-registry lookup <name|%pane>")
	}
	target := rest[0]

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	workers, srcErrs := registry.Snapshot(ctx, readers, hbWindow)
	for src, e := range srcErrs {
		fmt.Fprintf(os.Stderr, "orch-registry: source %q: %v\n", src, e)
	}
	if srcErrs.HasFatal() {
		return srcErrs["agents"]
	}

	for _, w := range workers {
		if matches(w, target) {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(toJSONWorker(w))
		}
	}
	return fmt.Errorf("%w: %s", errNotFound, target)
}

func matches(w registry.Worker, target string) bool {
	if len(target) > 0 && target[0] == '%' {
		return w.PaneID == target
	}
	// Special role aliases. "operator" / "op" resolves to whichever agent
	// advertises metadata.role=="operator" (the operator shell sets
	// ORCH_ROLE=operator before starting the session); orch-spy depends
	// on this lookup form.
	if target == "operator" || target == "op" {
		return w.Role == "operator"
	}
	if w.Name == target {
		return true
	}
	// Allow lookup by session even when the alias shadowed Name (some
	// callers know the worker by its operator-set session label).
	if w.Session != "" && w.Session == target {
		return true
	}
	return false
}

// --- serve ------------------------------------------------------------

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	interval := fs.Duration("interval", 5*time.Second, "snapshot publish interval")
	subject := fs.String("subject", "orch.registry.snapshot", "publish subject")
	nc, readers, hbWindow, cleanup, err := commonFlags(fs, args)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Spin up the NATSSource heartbeat subscription so Live's snapshots
	// carry live HB data.
	if hbReader, ok := readers.Heartbeats.(*sources.NATSSource); ok {
		go func() { _ = hbReader.Run(ctx) }()
	}

	live := registry.NewLive(readers, registry.LiveOptions{
		RefreshInterval: *interval,
		HeartbeatWindow: hbWindow,
	})
	if err := live.Start(ctx); err != nil {
		return fmt.Errorf("start registry: %w", err)
	}
	defer live.Close() //nolint:errcheck

	fmt.Fprintf(os.Stderr, "orch-registry: serving on %s every %s\n", *subject, *interval)

	// Publish on every refresh tick. The Live registry runs its own
	// refresh loop; we tick alongside it at the same cadence.
	t := time.NewTicker(*interval)
	defer t.Stop()

	publish := func() {
		workers := live.Snapshot()
		payload, err := json.Marshal(toJSONWorkers(workers))
		if err != nil {
			fmt.Fprintf(os.Stderr, "orch-registry: marshal: %v\n", err)
			return
		}
		if err := nc.Publish(*subject, payload); err != nil {
			fmt.Fprintf(os.Stderr, "orch-registry: publish: %v\n", err)
		}
	}
	publish() // first snapshot immediate

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			publish()
		}
	}
}

// --- JSON shape -------------------------------------------------------

// jsonWorker is the wire shape. Stable across releases; consumers
// (bin/orch-peek, bin/orch-tell, UI) parse this exact layout.
type jsonWorker struct {
	PaneID     string            `json:"pane_id"`
	InstanceID string            `json:"instance_id,omitempty"`
	Name       string            `json:"name"`
	Role       string            `json:"role"`
	Outfit     string            `json:"outfit,omitempty"`
	Agent      string            `json:"agent"`
	CWD        string            `json:"cwd,omitempty"`
	Owner      string            `json:"owner,omitempty"`
	Session    string            `json:"session,omitempty"`
	LastHB     string            `json:"last_hb,omitempty"`
	Alive      bool              `json:"alive"`
	Subjects   jsonSubjects      `json:"subjects"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

type jsonSubjects struct {
	Prompt string `json:"prompt,omitempty"`
	Status string `json:"status,omitempty"`
	HB     string `json:"hb,omitempty"`
}

func toJSONWorker(w registry.Worker) jsonWorker {
	jw := jsonWorker{
		PaneID:     w.PaneID,
		InstanceID: w.InstanceID,
		Name:       w.Name,
		Role:       w.Role,
		Outfit:     w.Outfit,
		Agent:      w.Agent,
		CWD:        w.CWD,
		Owner:      w.Owner,
		Session:    w.Session,
		Alive:      w.Alive,
		Subjects: jsonSubjects{
			Prompt: w.Subjects.Prompt,
			Status: w.Subjects.Status,
			HB:     w.Subjects.HB,
		},
		Metadata: w.Metadata,
	}
	if !w.LastHB.IsZero() {
		jw.LastHB = w.LastHB.UTC().Format(time.RFC3339)
	}
	return jw
}

func toJSONWorkers(ws []registry.Worker) []jsonWorker {
	out := make([]jsonWorker, len(ws))
	for i, w := range ws {
		out[i] = toJSONWorker(w)
	}
	return out
}
