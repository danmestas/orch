// orch-workflow — workflow YAML CLI.
//
// Subcommands:
//
//	orch-workflow validate <file>           — parse + validate; exit 0 if valid
//	orch-workflow compile  <file> [--print] — validate + emit the planned task DAG
//	orch-workflow apply    <file> [flags]   — seed compiled tasks into a sesh scope
//	orch-workflow status   <workflow-id>    — render live DAG progress for a workflow
//	orch-workflow cancel   <workflow-id>    — mark all pending/blocked tasks cancelled
//
// Phase B (this binary) wires the apply/status/cancel verbs onto a
// sesh-ops backend. The default backend shells out to the `sesh-ops`
// binary on $PATH; --server / --session / --scope are forwarded
// verbatim so this CLI inherits sesh-ops's resolution rules.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/danmestas/orch/internal/workflow"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, errInvalid) {
			// Validation failures already printed their diagnostics.
			os.Exit(2)
		}
		if errors.Is(err, errNotImplemented) {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		fmt.Fprintln(os.Stderr, "orch-workflow:", err)
		os.Exit(1)
	}
}

var (
	errInvalid        = errors.New("workflow is invalid")
	errNotImplemented = errors.New("subcommand not implemented")
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
	case "compile":
		return cmdCompile(rest)
	case "apply":
		return cmdApply(rest)
	case "status":
		return cmdStatus(rest)
	case "cancel":
		return cmdCancel(rest)
	case "-h", "--help", "help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown subcommand: %s", sub)
	}
}

func usage() {
	const help = `orch-workflow — compile + run workflow YAML (Proposal 0007)

Subcommands:
  validate <file>             Parse + run compile-time DAG validation.
                              Exit 0 on valid; 2 on invalid; 1 on parse / IO error.
  compile  <file> [--print]   Validate then emit the planned task DAG.
                              With --print, writes pretty JSON to stdout.
  apply    <file> [flags]     Compile + seed tasks into a sesh scope.
                              Idempotent: re-applying unchanged YAML is a no-op.
  status   <workflow-id>      Show live DAG progress for an applied workflow.
  cancel   <workflow-id>      Cancel all pending/blocked tasks in the workflow.
                              In-flight tasks are NOT killed (see #180).

Common flags:
  --fleet name1,name2,...     Enforce assign-target check against this fleet list.
                              Without it, assign references to non-spawn targets are
                              not validated.

Sesh-routing flags (apply / status / cancel):
  --server URL                NATS URL (forwarded to sesh-ops; env $SESH_OPS_SERVER).
  --session NAME              Sesh session name (forwarded to sesh-ops).
  --scope NAME                Memory scope (default "workflow").
  --scope-id ID               Scope identifier (apply uses workflow.scope-id if blank).
  --sesh-ops PATH             Override the sesh-ops binary (default: lookup on $PATH).
`
	fmt.Fprint(os.Stderr, help)
}

func cmdValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fleet := fs.String("fleet", "", "comma-separated worker names to enforce assign targets against")
	path, err := parseOnePositional(fs, args)
	if err != nil {
		return err
	}
	if path == "" {
		fs.Usage()
		return errors.New("validate: exactly one yaml path required")
	}
	wf, err := workflow.ParseFile(path)
	if err != nil {
		return err
	}
	rpt := workflow.Validate(wf, fleetOpts(*fleet)...)
	if s := rpt.String(); s != "" {
		fmt.Fprintln(os.Stderr, s)
	}
	if !rpt.Valid() {
		return errInvalid
	}
	if len(rpt.Warnings()) == 0 {
		fmt.Fprintf(os.Stderr, "%s: ok\n", path)
	} else {
		fmt.Fprintf(os.Stderr, "%s: ok (%d warning(s))\n", path, len(rpt.Warnings()))
	}
	return nil
}

func cmdCompile(args []string) error {
	fs := flag.NewFlagSet("compile", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	print := fs.Bool("print", false, "print compiled task DAG as JSON to stdout")
	fleet := fs.String("fleet", "", "comma-separated worker names for assign-target check")
	path, err := parseOnePositional(fs, args)
	if err != nil {
		return err
	}
	if path == "" {
		fs.Usage()
		return errors.New("compile: exactly one yaml path required")
	}
	wf, err := workflow.ParseFile(path)
	if err != nil {
		return err
	}
	rpt := workflow.Validate(wf, fleetOpts(*fleet)...)
	if !rpt.Valid() {
		fmt.Fprintln(os.Stderr, rpt.String())
		return errInvalid
	}
	plan, err := workflow.Compile(wf)
	if err != nil {
		return err
	}
	if *print {
		buf, err := plan.JSON()
		if err != nil {
			return err
		}
		fmt.Println(string(buf))
		return nil
	}
	fmt.Fprintf(os.Stderr, "%s: compiled %d task(s) for workflow %q\n", path, len(plan.Tasks), plan.Workflow)
	return nil
}

func cmdApply(args []string) error {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fleet := fs.String("fleet", "", "comma-separated worker names for assign-target check")
	sesh := seshFlags(fs)
	scopeIDOverride := fs.String("scope-id", "", "override workflow.scope-id (routes apply into a specific subtree)")

	path, err := parseOnePositional(fs, args)
	if err != nil {
		return err
	}
	if path == "" {
		fs.Usage()
		return errors.New("apply: exactly one yaml path required")
	}
	wf, err := workflow.ParseFile(path)
	if err != nil {
		return err
	}
	rpt := workflow.Validate(wf, fleetOpts(*fleet)...)
	if !rpt.Valid() {
		fmt.Fprintln(os.Stderr, rpt.String())
		return errInvalid
	}
	if w := rpt.Warnings(); len(w) > 0 {
		fmt.Fprintln(os.Stderr, rpt.String())
	}
	client := sesh.client()
	out, err := workflow.Apply(context.Background(), wf, client, workflow.ApplyOptions{
		ScopeID: *scopeIDOverride,
	})
	if err != nil {
		return err
	}
	fmt.Print(out.String())
	return nil
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	sesh := seshFlags(fs)
	scopeID := fs.String("scope-id", "", "scope identifier (required)")

	workflowID, err := parseOnePositional(fs, args)
	if err != nil {
		return err
	}
	if workflowID == "" {
		fs.Usage()
		return errors.New("status: workflow id required (positional)")
	}
	if *scopeID == "" {
		return errors.New("status: --scope-id is required")
	}
	report, err := workflow.Status(context.Background(), workflowID, *scopeID, sesh.client())
	if err != nil {
		return err
	}
	fmt.Print(report.String())
	return nil
}

func cmdCancel(args []string) error {
	fs := flag.NewFlagSet("cancel", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	sesh := seshFlags(fs)
	scopeID := fs.String("scope-id", "", "scope identifier (required)")

	workflowID, err := parseOnePositional(fs, args)
	if err != nil {
		return err
	}
	if workflowID == "" {
		fs.Usage()
		return errors.New("cancel: workflow id required (positional)")
	}
	if *scopeID == "" {
		return errors.New("cancel: --scope-id is required")
	}
	report, err := workflow.Cancel(context.Background(), workflowID, *scopeID, sesh.client())
	if err != nil {
		return err
	}
	fmt.Print(report.String())
	return nil
}

// seshFlagSet bundles the four sesh-ops routing flags. We register
// them on every sesh-touching subcommand so operators don't need to
// learn a per-command flag layout. The .client() helper returns a
// configured SeshClient at execution time.
type seshFlagSet struct {
	binary  *string
	server  *string
	session *string
	scope   *string
}

func seshFlags(fs *flag.FlagSet) *seshFlagSet {
	return &seshFlagSet{
		binary:  fs.String("sesh-ops", "", "sesh-ops binary override (default: lookup on $PATH)"),
		server:  fs.String("server", "", "NATS URL forwarded to sesh-ops"),
		session: fs.String("session", "", "sesh session name forwarded to sesh-ops"),
		scope:   fs.String("scope", "workflow", "memory scope forwarded to sesh-ops"),
	}
}

func (s *seshFlagSet) client() *workflow.ExecSeshClient {
	c := workflow.NewExecSeshClient()
	if *s.binary != "" {
		c.Binary = *s.binary
	}
	c.Server = *s.server
	c.SessionFile = *s.session
	c.Scope = *s.scope
	return c
}

// parseOnePositional lets flags appear before OR after the single
// positional argument. Go's stdlib flag.Parse stops at the first
// non-flag, so a Unix-style invocation like
// `orch-workflow compile foo.yaml --print` would otherwise fail.
//
// Returns the positional and any parse error. Returns "" if no
// positional was supplied (caller's job to surface usage). Returns an
// error if a second positional shows up after re-parsing the trailing
// flags.
func parseOnePositional(fs *flag.FlagSet, args []string) (string, error) {
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() == 0 {
		return "", nil
	}
	positional := fs.Arg(0)
	remaining := fs.Args()[1:]
	if len(remaining) == 0 {
		return positional, nil
	}
	if err := fs.Parse(remaining); err != nil {
		return "", err
	}
	if fs.NArg() > 0 {
		return "", fmt.Errorf("unexpected positional arg(s) after %q: %v", positional, fs.Args())
	}
	return positional, nil
}

func fleetOpts(csv string) []workflow.ValidateOption {
	if csv == "" {
		return nil
	}
	names := splitCSV(csv)
	return []workflow.ValidateOption{workflow.WithFleet(names)}
}

// splitCSV splits on commas and trims whitespace around each element.
// Empty elements (between consecutive commas or after trimming) are
// dropped so `--fleet=" alice ,bob,"` matches workers "alice" and
// "bob" without surfacing a phantom "" name.
func splitCSV(s string) []string {
	out := make([]string, 0, 4)
	for _, part := range strings.Split(s, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
