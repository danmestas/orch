// orch-workflow — Phase A CLI for the workflow YAML compiler.
//
// Subcommands:
//
//	orch-workflow validate <file>           — parse + validate; exit 0 if valid
//	orch-workflow compile <file> [--print]  — validate + emit the planned task DAG
//
// Phase A scope: validator + diagnostic compile-print only. The apply /
// status / cancel subcommands described in Proposal 0007 land in Phase
// B once orch#141 (SpawnSpec) and orch#145 (Topology) are wired in.
// Until then this binary explicitly refuses those subcommands with a
// stable error code so harness code can detect "feature not ready".
package main

import (
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
	case "compile":
		return cmdCompile(rest)
	case "apply", "status", "cancel":
		return fmt.Errorf("%w: %s (waiting on orch#141 SpawnSpec + orch#145 Topology — see Proposal 0007 Phase B)", errNotImplemented, sub)
	case "-h", "--help", "help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown subcommand: %s", sub)
	}
}

func usage() {
	const help = `orch-workflow — compile + validate workflow YAML (Proposal 0007)

Subcommands:
  validate <file>             Parse + run compile-time DAG validation.
                              Exit 0 on valid; 2 on invalid; 1 on parse / IO error.
  compile  <file> [--print]   Validate then emit the planned task DAG.
                              With --print, writes pretty JSON to stdout.

  apply / status / cancel     Reserved for Phase B (orch#141 + orch#145).

Common flags:
  --fleet name1,name2,...     Enforce assign-target check against this fleet list.
                              Without it, assign references to non-spawn targets are
                              not validated (Phase A default).
`
	fmt.Fprint(os.Stderr, help)
}

func cmdValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fleet := fs.String("fleet", "", "comma-separated worker names to enforce assign targets against")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("validate: exactly one yaml path required")
	}
	wf, err := workflow.ParseFile(fs.Arg(0))
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
		fmt.Fprintf(os.Stderr, "%s: ok\n", fs.Arg(0))
	} else {
		fmt.Fprintf(os.Stderr, "%s: ok (%d warning(s))\n", fs.Arg(0), len(rpt.Warnings()))
	}
	return nil
}

func cmdCompile(args []string) error {
	fs := flag.NewFlagSet("compile", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	print := fs.Bool("print", false, "print compiled task DAG as JSON to stdout")
	fleet := fs.String("fleet", "", "comma-separated worker names for assign-target check")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("compile: exactly one yaml path required")
	}
	wf, err := workflow.ParseFile(fs.Arg(0))
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
	fmt.Fprintf(os.Stderr, "%s: compiled %d task(s) for workflow %q\n", fs.Arg(0), len(plan.Tasks), plan.Workflow)
	return nil
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
