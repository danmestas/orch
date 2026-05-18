package workflow

import (
	"fmt"
	"sort"
	"strings"
)

// Diagnostic is one validation finding.
//
// Code is a stable machine-readable identifier (e.g. `cycle`,
// `dangling-ref`); operators and CI can grep for codes. Message is the
// human-readable explanation. NodeID is the offending node when the
// finding is node-scoped (empty for workflow-level findings). Line is
// the YAML source line if known (0 if not).
type Diagnostic struct {
	Code     string
	Severity Severity
	NodeID   string
	Line     int
	Message  string
}

// Severity classifies a diagnostic. Validators return only Error or
// Warning; Info is reserved for `compile --print` annotations.
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

// String formats a Diagnostic as `code: nodeID:line: message`,
// suitable for one-line CLI output. Empty fields are omitted.
func (d Diagnostic) String() string {
	var b strings.Builder
	b.WriteString(d.Severity.String())
	b.WriteByte(' ')
	b.WriteString(d.Code)
	b.WriteString(": ")
	if d.NodeID != "" {
		b.WriteString(d.NodeID)
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
// Valid() is the canonical "is this workflow accepted?" check — true
// iff no Error diagnostics. Warnings do NOT invalidate the workflow.
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

// Stable list of recognised diagnostic codes. Tests + docs reference
// these constants instead of string literals to catch typos.
const (
	CodeMissingID         = "missing-id"
	CodeDuplicateID       = "duplicate-id"
	CodeMissingKind       = "missing-kind"
	CodeMultipleKind      = "multiple-kind"
	CodeMissingWorkflow   = "missing-workflow-name"
	CodeCycle             = "cycle"
	CodeUnknownDep        = "unknown-dependency"
	CodeUnreachable       = "unreachable"
	CodeDanglingRef       = "dangling-ref"
	CodeUnknownAssign     = "unknown-assign-target"
	CodeMissingSubField   = "missing-subfield"
	CodeInvalidIdentifier = "invalid-identifier"
	CodeJSONPathOnNonJSON = "json-path-on-non-json"
)
