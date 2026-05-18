// Package workflow parses and validates orch workflow YAML.
//
// Orch is not a workflow runtime — sesh's task model is. This package
// owns the YAML → typed-AST → compile-time-validated form. Compilation
// to sesh `task add` invocations and apply-against-a-scope live in the
// command layer (cmd/orch-workflow) and depend on this package's
// validated model.
//
// The public surface is small:
//
//   - Parse / ParseFile produce a *Workflow from YAML bytes.
//   - Validate(*Workflow) returns a Report — typed diagnostics with
//     stable codes, never panics. Empty report = workflow is valid.
//   - Compile(*Workflow, ...Option) produces a Plan — the flattened
//     task DAG plus the substitutions planned for each phase. Used by
//     `orch workflow compile --print` for diagnostic inspection.
//
// Per Proposal 0007 (docs/proposals/0007-workflow-yaml-compiled-to-task-dag.md)
// the validator IS the interface-contract test: invalid workflows must
// be unrepresentable at submission, not discovered at runtime.
package workflow
