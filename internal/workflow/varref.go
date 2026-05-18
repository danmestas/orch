package workflow

import (
	"regexp"
	"strings"
)

// VarRef is one resolved reference inside an interpolated string.
//
// Category determines when the substitution happens:
//
//   - CategoryEnv     — $ENV.NAME           resolved at compile time
//   - CategoryStatic  — $WORKFLOW.x         resolved at compile time
//   - CategoryNode    — $nodeId.output[...] resolved at task-pull time
//
// The decision is "resolve at the phase where the data is actually
// available" — matches Proposal 0007's locked decision (mixed-time
// substitution).
type VarRef struct {
	Raw      string      // the literal text matched in the source string
	Category RefCategory // env / static / node
	Name     string      // ENV var name, WORKFLOW field name, or referenced node id
	Path     []string    // dotted path after .output for CategoryNode refs (else nil)
}

// RefCategory identifies which substitution phase owns a reference.
type RefCategory int

const (
	CategoryUnknown RefCategory = iota
	CategoryEnv                 // $ENV.X
	CategoryStatic              // $WORKFLOW.X
	CategoryNode                // $nodeId.output[.json.path]
)

func (c RefCategory) String() string {
	switch c {
	case CategoryEnv:
		return "env"
	case CategoryStatic:
		return "static"
	case CategoryNode:
		return "node"
	default:
		return "unknown"
	}
}

// varRefRegexp matches Archon-style `$head.field[.field…]` interpolations.
// The capture intentionally requires a leading dollar followed by an
// identifier head, then one or more `.<ident>` segments. Bare `$foo`
// (no dot) is NOT a variable reference — it's left as a literal so
// shell-style $VAR in bash bodies survives untouched.
//
// We also reject matches where the preceding rune is `\` so users can
// write `\$plan.output` as a literal.
var varRefRegexp = regexp.MustCompile(`\$([A-Za-z_][A-Za-z0-9_]*)((?:\.[A-Za-z_][A-Za-z0-9_]*)+)`)

// ExtractRefs scans s for $-style interpolations and returns the parsed
// VarRefs in source order. Duplicates are preserved — callers that want
// to dedupe by Name should do so themselves.
func ExtractRefs(s string) []VarRef {
	if s == "" {
		return nil
	}
	matches := varRefRegexp.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]VarRef, 0, len(matches))
	for _, m := range matches {
		start := m[0]
		if start > 0 && s[start-1] == '\\' {
			// Operator escaped the reference — skip.
			continue
		}
		raw := s[m[0]:m[1]]
		head := s[m[2]:m[3]]
		rest := s[m[4]:m[5]] // starts with "."
		fields := strings.Split(strings.TrimPrefix(rest, "."), ".")
		ref := VarRef{Raw: raw}
		switch head {
		case "ENV":
			ref.Category = CategoryEnv
			if len(fields) > 0 {
				ref.Name = fields[0]
			}
			// Sub-fields after $ENV.X.Y are not meaningful — treat as
			// part of the name for diagnostic clarity but not as path.
			if len(fields) > 1 {
				ref.Name = strings.Join(fields, ".")
			}
		case "WORKFLOW":
			ref.Category = CategoryStatic
			ref.Name = strings.Join(fields, ".")
		default:
			ref.Category = CategoryNode
			ref.Name = head
			// $nodeId.output[.path...] — strip leading "output" if present.
			if len(fields) > 0 && fields[0] == "output" {
				ref.Path = fields[1:]
			} else {
				// $nodeId.foo (without .output) is still a node ref;
				// validator surfaces "missing .output" as a separate
				// concern rather than ignoring the ref.
				ref.Path = fields
			}
		}
		out = append(out, ref)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// NodeRefs returns only the cross-task (pull-time) references — useful
// for the validator's dangling-reference check.
func NodeRefs(s string) []VarRef {
	all := ExtractRefs(s)
	if len(all) == 0 {
		return nil
	}
	out := all[:0]
	for _, r := range all {
		if r.Category == CategoryNode {
			out = append(out, r)
		}
	}
	return out
}
