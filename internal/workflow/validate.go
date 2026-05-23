package workflow

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/danmestas/orch/internal/spawnspec"
	"gopkg.in/yaml.v3"
)

// ValidateOption tunes a single Validate call. The zero set is the
// production default (no fleet info, strict by default).
type ValidateOption func(*validateOpts)

// WithFleet provides the set of known worker names so the validator
// can reject `assign:` references to workers that don't exist in the
// targeted topology. Pass nil to skip the assign-target check (Phase A
// default — Proposal 0006 isn't wired yet).
func WithFleet(workerNames []string) ValidateOption {
	return func(o *validateOpts) {
		o.fleet = make(map[string]struct{}, len(workerNames))
		for _, n := range workerNames {
			o.fleet[n] = struct{}{}
		}
		o.fleetProvided = true
	}
}

type validateOpts struct {
	fleet         map[string]struct{}
	fleetProvided bool
}

// Validate runs all compile-time checks against the parsed workflow.
// It NEVER panics — every issue lands as a Diagnostic on the returned
// Report. A nil Workflow returns a Report with one missing-workflow-name
// error so the caller doesn't have to nil-guard.
//
// The checks correspond to Proposal 0007's "rejected at compile" list:
//
//  1. cycles (CodeCycle)
//  2. dangling node references (CodeDanglingRef)
//  3. discriminator violations (CodeMissingKind / CodeMultipleKind)
//  4. unreachable nodes (CodeUnreachable)
//  5. required-field violations (CodeMissingID, CodeMissingWorkflow,
//     CodeMissingSubField)
//  6. assign-to-unknown worker (CodeUnknownAssign — only when WithFleet)
//  7. variable-substitution type mismatches (CodeJSONPathOnNonJSON,
//     emitted as warning per spec)
//
// Plus shape checks not separately enumerated but implied:
//
//   - duplicate node IDs (CodeDuplicateID)
//   - unknown depends_on targets (CodeUnknownDep — strictly a subset of
//     "dangling reference" but reported separately so the operator sees
//     a clear distinction between DAG-shape errors and string-interp
//     errors)
//   - identifier well-formedness (CodeInvalidIdentifier)
func Validate(wf *Workflow, opts ...ValidateOption) *Report {
	cfg := &validateOpts{}
	for _, opt := range opts {
		opt(cfg)
	}

	rpt := &Report{}
	if wf == nil {
		rpt.Add(Diagnostic{
			Code: CodeMissingWorkflow, Severity: SeverityError,
			Message: "workflow is nil",
		})
		return rpt
	}
	if wf.Name == "" {
		rpt.Add(Diagnostic{
			Code: CodeMissingWorkflow, Severity: SeverityError,
			Line:    wf.SourceLine,
			Message: "workflow `name` is required",
		})
	}

	idx := buildIndex(wf, rpt)
	if len(idx.nodes) == 0 {
		// Either there were no nodes, or all of them had missing IDs
		// (already reported). Nothing else to check.
		return rpt
	}

	checkDiscriminators(wf, idx, rpt)
	checkSubFields(wf, idx, rpt)
	checkSpawnBodies(wf, idx, rpt)
	checkDepsExist(wf, idx, rpt)
	checkCycles(wf, idx, rpt)
	checkRefs(wf, idx, rpt)
	checkUnreachable(wf, idx, rpt)
	checkAssign(wf, idx, cfg, rpt)
	checkJSONPathHints(wf, idx, rpt)
	return rpt
}

// nodeIndex caches the cross-cuts the rule checks all need.
type nodeIndex struct {
	nodes  map[string]*Node // valid (non-empty-id) nodes
	order  []string         // ids in declaration order
	spawns map[string]bool  // ids of `spawn:` nodes (assign targets)

	// cycleMembers is populated by checkCycles with every node id
	// participating in the detected cycle, so checkUnreachable can
	// skip them — cycle reporting makes the unreachability
	// self-evident and stacking both diagnostics on the same node is
	// noisy.
	cycleMembers map[string]bool
}

var identRegexp = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

func buildIndex(wf *Workflow, rpt *Report) *nodeIndex {
	idx := &nodeIndex{
		nodes:        make(map[string]*Node, len(wf.Nodes)),
		order:        make([]string, 0, len(wf.Nodes)),
		spawns:       make(map[string]bool),
		cycleMembers: make(map[string]bool),
	}
	seen := make(map[string]int) // id → first source line we saw
	for i := range wf.Nodes {
		n := &wf.Nodes[i]
		if n.ID == "" {
			rpt.Add(Diagnostic{
				Code: CodeMissingID, Severity: SeverityError, Line: n.SourceLine,
				Message: fmt.Sprintf("node at index %d is missing `id`", i),
			})
			continue
		}
		if !identRegexp.MatchString(n.ID) {
			rpt.Add(Diagnostic{
				Code: CodeInvalidIdentifier, Severity: SeverityError,
				NodeID: n.ID, Line: n.SourceLine,
				Message: fmt.Sprintf("node id %q is not a valid identifier (alnum/underscore/dash, must start with letter or underscore)", n.ID),
			})
			continue
		}
		if firstLine, dupe := seen[n.ID]; dupe {
			rpt.Add(Diagnostic{
				Code: CodeDuplicateID, Severity: SeverityError,
				NodeID: n.ID, Line: n.SourceLine,
				Message: fmt.Sprintf("duplicate node id %q (first declared at line %d)", n.ID, firstLine),
			})
			continue
		}
		seen[n.ID] = n.SourceLine
		idx.nodes[n.ID] = n
		idx.order = append(idx.order, n.ID)
		if n.Spawn != nil {
			idx.spawns[n.ID] = true
		}
	}
	return idx
}

// checkDiscriminators implements rule 3 (discriminator violations) and
// part of rule 5 (missing kind discriminator).
func checkDiscriminators(_ *Workflow, idx *nodeIndex, rpt *Report) {
	for _, id := range idx.order {
		n := idx.nodes[id]
		kinds := n.Kinds()
		switch {
		case len(kinds) == 0:
			rpt.Add(Diagnostic{
				Code: CodeMissingKind, Severity: SeverityError,
				NodeID: id, Line: n.SourceLine,
				Message: "node has no kind discriminator (need exactly one of: prompt, bash, script, command, loop, approval, spawn)",
			})
		case len(kinds) > 1:
			names := make([]string, len(kinds))
			for i, k := range kinds {
				names[i] = string(k)
			}
			rpt.Add(Diagnostic{
				Code: CodeMultipleKind, Severity: SeverityError,
				NodeID: id, Line: n.SourceLine,
				Message: fmt.Sprintf("node has multiple kind discriminators: %v (exactly one allowed)", names),
			})
		}
	}
}

// checkSubFields implements per-kind required field checks (rule 5
// continued). A node missing the required body field for its kind
// (e.g., `loop:` without a `prompt:`) is rejected.
func checkSubFields(_ *Workflow, idx *nodeIndex, rpt *Report) {
	for _, id := range idx.order {
		n := idx.nodes[id]
		switch n.Kind() {
		case KindScript:
			if n.Script.Name == "" {
				rpt.Add(missingSubfield(id, n.SourceLine, "script.name"))
			}
		case KindCommand:
			if n.Command.Name == "" {
				rpt.Add(missingSubfield(id, n.SourceLine, "command.name"))
			}
		case KindLoop:
			if n.Loop.Prompt == "" {
				rpt.Add(missingSubfield(id, n.SourceLine, "loop.prompt"))
			}
			if n.Loop.Until == "" {
				rpt.Add(missingSubfield(id, n.SourceLine, "loop.until"))
			}
		case KindApproval:
			if n.Approval.Prompt == "" {
				rpt.Add(missingSubfield(id, n.SourceLine, "approval.prompt"))
			}
		case KindSpawn:
			if n.Spawn.Name == "" {
				rpt.Add(missingSubfield(id, n.SourceLine, "spawn.name"))
			}
		}
	}
}

func missingSubfield(id string, line int, field string) Diagnostic {
	return Diagnostic{
		Code: CodeMissingSubField, Severity: SeverityError,
		NodeID: id, Line: line,
		Message: fmt.Sprintf("required field %q is missing", field),
	}
}

// checkSpawnBodies enforces the SpawnSpec contract on every `spawn:`
// node body. This is Phase B's "tighten spawn-body strictness" check
// (orch#157): the Spawn struct uses `yaml:",inline"` for everything
// except name, which defeats KnownFields strictness inside the spawn
// body — typos like `agnet:` or `outfite:` would otherwise pass
// silently through Phase A and surface as runtime errors deep inside
// orch-spawn.
//
// The check delegates to spawnspec.UnmarshalSpec (strict decode,
// unknown spec_version reject) + spawnspec.ValidateSpec (executor
// XOR, dns-label name, agent enum, env-key shape, etc.). See
// docs/executor-protocol.md for the validation contract.
//
// Nodes with no Spawn name are skipped — they already get a clearer
// CodeMissingSubField diagnostic from checkSubFields; piling a
// SpawnSpec validation error on top would just be noise.
func checkSpawnBodies(_ *Workflow, idx *nodeIndex, rpt *Report) {
	for _, id := range idx.order {
		n := idx.nodes[id]
		if n.Spawn == nil || n.Spawn.Name == "" {
			continue
		}
		buf, err := yaml.Marshal(n.Spawn)
		if err != nil {
			rpt.Add(Diagnostic{
				Code: CodeInvalidSpawn, Severity: SeverityError,
				NodeID: id, Line: n.SourceLine,
				Message: fmt.Sprintf("spawn body could not be re-encoded for validation: %v", err),
			})
			continue
		}
		spec, err := spawnspec.UnmarshalSpec(buf)
		if err != nil {
			rpt.Add(Diagnostic{
				Code: CodeInvalidSpawn, Severity: SeverityError,
				NodeID: id, Line: n.SourceLine,
				Message: fmt.Sprintf("spawn body rejected by spawnspec parser: %s", trimSpawnErr(err)),
			})
			continue
		}
		if err := spawnspec.ValidateSpec(spec); err != nil {
			rpt.Add(Diagnostic{
				Code: CodeInvalidSpawn, Severity: SeverityError,
				NodeID: id, Line: n.SourceLine,
				Message: fmt.Sprintf("spawn body failed spawnspec validation: %s", trimSpawnErr(err)),
			})
		}
	}
}

// trimSpawnErr strips the "spawnspec: " prefix that the spawnspec
// package puts on its errors, since the diagnostic message already
// names the source. Multi-line errors (struct-validator output) are
// flattened to a single line so the workflow diagnostic stays
// grep-able.
func trimSpawnErr(err error) string {
	msg := strings.TrimPrefix(err.Error(), "spawnspec: ")
	msg = strings.ReplaceAll(msg, "\n", "; ")
	return strings.TrimSpace(msg)
}

// checkDepsExist verifies every depends_on target resolves to a known
// node. Implemented as its own rule (separate from CodeDanglingRef)
// because operators distinguish "you depend on a typo'd node" from
// "your prompt interpolates a typo'd node".
func checkDepsExist(_ *Workflow, idx *nodeIndex, rpt *Report) {
	for _, id := range idx.order {
		n := idx.nodes[id]
		for _, dep := range n.DependsOn {
			if _, ok := idx.nodes[dep]; !ok {
				rpt.Add(Diagnostic{
					Code: CodeUnknownDep, Severity: SeverityError,
					NodeID: id, Line: n.SourceLine,
					Message: fmt.Sprintf("depends_on references unknown node %q", dep),
				})
			}
		}
	}
}

// checkCycles walks the depends_on graph and reports the first cycle
// it finds. We don't report every cycle separately — surfacing the
// path the operator can use to break the loop is enough; chasing every
// cycle in the SCC would just add noise.
func checkCycles(_ *Workflow, idx *nodeIndex, rpt *Report) {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(idx.nodes))
	var stack []string
	var dfs func(id string) (cycle []string, found bool)
	dfs = func(id string) ([]string, bool) {
		color[id] = gray
		stack = append(stack, id)
		n := idx.nodes[id]
		for _, dep := range n.DependsOn {
			if _, ok := idx.nodes[dep]; !ok {
				continue // already reported as unknown-dependency
			}
			switch color[dep] {
			case white:
				if cycle, found := dfs(dep); found {
					return cycle, true
				}
			case gray:
				// cycle: extract from `dep` to end of stack
				idxAt := 0
				for i, s := range stack {
					if s == dep {
						idxAt = i
						break
					}
				}
				cycle := append([]string(nil), stack[idxAt:]...)
				cycle = append(cycle, dep) // close the cycle visually
				return cycle, true
			}
		}
		color[id] = black
		stack = stack[:len(stack)-1]
		return nil, false
	}
	for _, id := range idx.order {
		if color[id] != white {
			continue
		}
		stack = stack[:0]
		if cycle, found := dfs(id); found {
			path := formatCycle(cycle)
			n := idx.nodes[cycle[0]]
			rpt.Add(Diagnostic{
				Code: CodeCycle, Severity: SeverityError,
				NodeID: cycle[0], Line: n.SourceLine,
				Message: fmt.Sprintf("cyclic dependency: %s", path),
			})
			// Record every node in the cycle (cycle[len-1] duplicates
			// cycle[0] to close the loop visually — the set handles it).
			for _, m := range cycle {
				idx.cycleMembers[m] = true
			}
			return // one cycle report is enough; operator fixes and re-validates
		}
	}
}

func formatCycle(cycle []string) string {
	if len(cycle) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(cycle[0])
	for _, n := range cycle[1:] {
		b.WriteString(" → ")
		b.WriteString(n)
	}
	return b.String()
}

// checkRefs implements rule 2 (dangling $nodeId.output references).
// String interpolation lives in prompt / loop.prompt / bash /
// approval.prompt bodies. We don't try to interpret $nodeId references
// inside `when:` expressions in Phase A — that's a separate parser.
func checkRefs(_ *Workflow, idx *nodeIndex, rpt *Report) {
	for _, id := range idx.order {
		n := idx.nodes[id]
		for _, body := range interpolatedBodies(n) {
			for _, ref := range NodeRefs(body) {
				if _, ok := idx.nodes[ref.Name]; !ok {
					rpt.Add(Diagnostic{
						Code: CodeDanglingRef, Severity: SeverityError,
						NodeID: id, Line: n.SourceLine,
						Message: fmt.Sprintf("references unknown node: %s", ref.Raw),
					})
				}
			}
		}
	}
}

func interpolatedBodies(n *Node) []string {
	var out []string
	if n.Prompt != "" {
		out = append(out, n.Prompt)
	}
	if n.Bash != "" {
		out = append(out, n.Bash)
	}
	if n.Loop != nil {
		out = append(out, n.Loop.Prompt)
	}
	if n.Approval != nil {
		out = append(out, n.Approval.Prompt)
	}
	return out
}

// checkUnreachable implements rule 4. A node is "unreachable" if it
// cannot start without a missing dependency — phrased differently, if
// its transitive depends_on closure includes a node that doesn't
// exist. We don't try to evaluate `when:` predicates in Phase A; the
// spec lists "behind always-false when:" as a future enhancement, not
// a v1 requirement. Each unreachable node is reported once.
//
// Implementation: BFS from all nodes with empty depends_on; anything
// not visited is unreachable.
func checkUnreachable(_ *Workflow, idx *nodeIndex, rpt *Report) {
	reverse := make(map[string][]string, len(idx.nodes)) // depended-on → depender list
	indegree := make(map[string]int, len(idx.nodes))
	for _, id := range idx.order {
		indegree[id] = 0
	}
	for _, id := range idx.order {
		n := idx.nodes[id]
		for _, dep := range n.DependsOn {
			if _, ok := idx.nodes[dep]; !ok {
				// missing dep — every downstream node becomes unreachable
				// via this branch. Track by injecting a sentinel.
				indegree[id]++ // dep doesn't exist; node can never start
				continue
			}
			reverse[dep] = append(reverse[dep], id)
			indegree[id]++
		}
	}
	queue := make([]string, 0)
	for _, id := range idx.order {
		if indegree[id] == 0 {
			queue = append(queue, id)
		}
	}
	visited := make(map[string]bool, len(idx.nodes))
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if visited[id] {
			continue
		}
		visited[id] = true
		for _, child := range reverse[id] {
			indegree[child]--
			if indegree[child] == 0 {
				queue = append(queue, child)
			}
		}
	}
	for _, id := range idx.order {
		if visited[id] {
			continue
		}
		// Skip nodes already implicated in a cycle — cycle reporting
		// makes the unreachability self-evident and stacking both
		// diagnostics on the same node is noisy. checkCycles populated
		// the membership set on idx for this check.
		if idx.cycleMembers[id] {
			continue
		}
		n := idx.nodes[id]
		rpt.Add(Diagnostic{
			Code: CodeUnreachable, Severity: SeverityError,
			NodeID: id, Line: n.SourceLine,
			Message: "node is unreachable (depends on an unknown node or sits behind a cycle)",
		})
	}
}

// checkAssign implements rule 6. The validator only enforces the
// assign target when callers supply a fleet via WithFleet. Without
// fleet info (Phase A default), the check is downgraded to "assign
// must reference a declared spawn node OR is unknowable here" — we
// still catch the case where assign points at a typo of a declared
// spawn node, but cannot reject assigns that target real workers
// outside the workflow.
func checkAssign(_ *Workflow, idx *nodeIndex, cfg *validateOpts, rpt *Report) {
	for _, id := range idx.order {
		n := idx.nodes[id]
		if n.Assign == "" {
			continue
		}
		if _, viaSpawn := spawnTargets(idx)[n.Assign]; viaSpawn {
			continue
		}
		if cfg.fleetProvided {
			if _, ok := cfg.fleet[n.Assign]; ok {
				continue
			}
			rpt.Add(Diagnostic{
				Code: CodeUnknownAssign, Severity: SeverityError,
				NodeID: id, Line: n.SourceLine,
				Message: fmt.Sprintf("assign references unknown worker: %q (not in topology fleet and not declared as a spawn node)", n.Assign),
			})
		}
		// no fleet info → silent (Phase A: topology isn't wired yet).
	}
}

// spawnTargets builds the set of `spawn:` node names — these are
// legitimate assign targets even without fleet info because the
// workflow itself provisions them.
func spawnTargets(idx *nodeIndex) map[string]struct{} {
	out := make(map[string]struct{})
	for id := range idx.spawns {
		n := idx.nodes[id]
		if n.Spawn.Name != "" {
			out[n.Spawn.Name] = struct{}{}
		}
	}
	return out
}

// checkJSONPathHints implements rule 7 — emitted as a warning.
// A node that declares `bash:` produces shell stdout (treated as
// non-JSON), so a downstream consumer doing `$thatNode.output.json.path`
// is suspicious. We warn rather than error: bash output CAN be JSON
// when the operator runs `jq` or similar.
func checkJSONPathHints(_ *Workflow, idx *nodeIndex, rpt *Report) {
	for _, id := range idx.order {
		n := idx.nodes[id]
		for _, body := range interpolatedBodies(n) {
			for _, ref := range NodeRefs(body) {
				if len(ref.Path) == 0 {
					continue
				}
				target, ok := idx.nodes[ref.Name]
				if !ok {
					continue // already reported as dangling
				}
				if target.Kind() == KindBash {
					rpt.Add(Diagnostic{
						Code: CodeJSONPathOnNonJSON, Severity: SeverityWarning,
						NodeID: id, Line: n.SourceLine,
						Message: fmt.Sprintf("%s indexes into %q which is a bash node (stdout is plain text; ensure command emits JSON)", ref.Raw, ref.Name),
					})
				}
			}
		}
	}
}
