package subtree

import (
	"fmt"
	"sort"
	"strings"
)

// Diagnostic is one validation finding.
//
// Code is a stable machine-readable identifier (e.g. `missing-name`,
// `executor-xor`); operators and CI can grep for codes. Message is the
// human-readable explanation. Path is the YAML dotted path the
// diagnostic anchors to ("workers[2].name", "sesh", ""). Line is the
// YAML source line if known (0 if not).
//
// Mirrors internal/workflow/diagnostic.go (Proposal 0007 alignment —
// the workflow validator uses the same shape; subtree adopts it so
// operator tooling sees a single Diagnostic surface across both
// validators).
type Diagnostic struct {
	Code     string
	Severity Severity
	Path     string
	Line     int
	Message  string
}

// Severity classifies a diagnostic. The subtree validator emits only
// Error today; Warning/Info are kept for symmetry with the workflow
// validator so future checks (e.g. "no workers defined → warn") slot
// in without an API change.
type Severity int

const (
	SeverityError Severity = iota
	SeverityWarning
	SeverityInfo
)

func (s Severity) String() string {
	switch s {
	case SeverityError:
		return "error"
	case SeverityWarning:
		return "warning"
	case SeverityInfo:
		return "info"
	default:
		return "unknown"
	}
}

// String formats a Diagnostic as `severity code: path:line: message`,
// suitable for one-line CLI output. Empty fields are omitted.
func (d Diagnostic) String() string {
	var b strings.Builder
	b.WriteString(d.Severity.String())
	b.WriteByte(' ')
	b.WriteString(d.Code)
	b.WriteString(": ")
	if d.Path != "" {
		b.WriteString(d.Path)
		if d.Line > 0 {
			fmt.Fprintf(&b, ":%d", d.Line)
		}
		b.WriteString(": ")
	} else if d.Line > 0 {
		fmt.Fprintf(&b, "line %d: ", d.Line)
	}
	b.WriteString(d.Message)
	return b.String()
}

// Report bundles diagnostics from one Validate call.
//
// Valid() is the canonical "is this topology accepted?" check — true
// iff no Error diagnostics.
type Report struct {
	Diagnostics []Diagnostic
}

// Valid returns true when no error-severity diagnostics were collected.
func (r *Report) Valid() bool {
	for _, d := range r.Diagnostics {
		if d.Severity == SeverityError {
			return false
		}
	}
	return true
}

// Errors returns the slice of error-severity diagnostics (may be empty).
func (r *Report) Errors() []Diagnostic { return r.bySeverity(SeverityError) }

// Warnings returns the slice of warning-severity diagnostics.
func (r *Report) Warnings() []Diagnostic { return r.bySeverity(SeverityWarning) }

func (r *Report) bySeverity(s Severity) []Diagnostic {
	out := make([]Diagnostic, 0, len(r.Diagnostics))
	for _, d := range r.Diagnostics {
		if d.Severity == s {
			out = append(out, d)
		}
	}
	return out
}

// String renders the report deterministically: errors first (sorted by
// line then code), warnings second. Empty report renders as the empty
// string so callers can `if rpt.String() != ""` print noisefree output.
func (r *Report) String() string {
	if len(r.Diagnostics) == 0 {
		return ""
	}
	sorted := append([]Diagnostic(nil), r.Diagnostics...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Severity != sorted[j].Severity {
			return sorted[i].Severity < sorted[j].Severity
		}
		if sorted[i].Line != sorted[j].Line {
			return sorted[i].Line < sorted[j].Line
		}
		return sorted[i].Code < sorted[j].Code
	})
	var b strings.Builder
	for i, d := range sorted {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(d.String())
	}
	return b.String()
}

// Add appends a diagnostic to the report (small helper to keep
// validator call sites tight).
func (r *Report) Add(d Diagnostic) { r.Diagnostics = append(r.Diagnostics, d) }

// Err collapses the Report into an error suitable for callers that
// already use the `err != nil` idiom (apply.go, status.go). Returns
// nil when Valid(). The error string is the Report's String() so
// nothing is lost vs. the prior concatenated-error output.
func (r *Report) Err() error {
	if r.Valid() {
		return nil
	}
	return fmt.Errorf("subtree: validation failed:\n%s", r.String())
}

// Stable list of recognised diagnostic codes. Tests + docs reference
// these constants instead of string literals to catch typos. The list
// mirrors the spirit of internal/workflow/diagnostic.go: one code per
// distinct failure mode, not one code per error message.
const (
	// Topology-level codes.
	CodeMissingName  = "missing-name"
	CodeMissingSesh  = "missing-sesh"
	CodeBadDNSLabel  = "bad-dns-label"
	CodeSeshXOR      = "sesh-xor"
	CodeSeshBadScope = "sesh-bad-scope"

	// Worker-level codes.
	CodeDuplicateWorker    = "duplicate-worker"
	CodeMissingWorkerName  = "missing-worker-name"
	CodeMissingWorkerAgent = "missing-worker-agent"
	CodeMissingExecutor    = "missing-executor"
	CodeExecutorXOR        = "executor-xor"
	CodeBadAgent           = "bad-agent"
	CodeBadWorkerDNSLabel  = "bad-worker-dns-label"
	CodeSpawnSpecInvalid   = "spawnspec-invalid"

	// State-seed codes.
	CodeMissingStateScope    = "missing-state-scope"
	CodeMissingStateScopeID  = "missing-state-scope-id"
	CodeMissingTaskTitle     = "missing-task-title"
	CodeMissingGoalObjective = "missing-goal-objective"
	CodeNegativeMaxAttempts  = "negative-max-attempts"
	CodeNegativeBudgetTokens = "negative-budget-tokens"
)
