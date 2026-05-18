package workflow

import "gopkg.in/yaml.v3"

// Workflow is the parsed top-level YAML document.
//
// The fields mirror the public YAML schema documented in
// docs/workflow-yaml.md. Defaults applied during Parse:
//
//   - ScopeID may be empty; resolved against the targeted subtree at
//     apply time. It is NOT a parse-time error here — `validate` checks
//     scope-id requirements per command.
//   - Nodes preserves source order so diagnostics can refer back to the
//     position the operator wrote.
type Workflow struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
	ScopeID     string `yaml:"scope-id,omitempty"`
	Nodes       []Node `yaml:"nodes"`

	// SourceLine records the YAML node line for the document root,
	// used to anchor "missing required field" diagnostics that have
	// no narrower position.
	SourceLine int `yaml:"-"`
}

// Node is a single workflow step. Exactly one of the Kind discriminator
// fields (Prompt / Loop / Bash / Script / Command / Approval / Spawn)
// must be set — the parser captures the field that was present and
// stores it in Kind for downstream consumers.
//
// `assign:` is a modifier, not a discriminator: any node may carry it
// to pin the compiled task to a named worker.
type Node struct {
	ID          string   `yaml:"id"`
	DependsOn   []string `yaml:"depends_on,omitempty"`
	When        string   `yaml:"when,omitempty"`
	TriggerRule string   `yaml:"trigger_rule,omitempty"`
	Timeout     string   `yaml:"timeout,omitempty"`
	IdleTimeout string   `yaml:"idle_timeout,omitempty"`
	Assign      string   `yaml:"assign,omitempty"`

	// Discriminators — exactly one of these is non-zero in a valid node.
	Prompt   string    `yaml:"prompt,omitempty"`
	Bash     string    `yaml:"bash,omitempty"`
	Script   *Script   `yaml:"script,omitempty"`
	Command  *Command  `yaml:"command,omitempty"`
	Loop     *Loop     `yaml:"loop,omitempty"`
	Approval *Approval `yaml:"approval,omitempty"`
	Spawn    *Spawn    `yaml:"spawn,omitempty"`

	// SourceLine is the YAML source line for the node's mapping start,
	// populated by Parse so diagnostics can point at the user's code.
	SourceLine int `yaml:"-"`
}

// NodeKind names the discriminator field that was set on a Node.
// Returns KindUnknown if zero or more-than-one discriminators are set;
// the validator surfaces both cases.
type NodeKind string

const (
	KindUnknown  NodeKind = ""
	KindPrompt   NodeKind = "prompt"
	KindBash     NodeKind = "bash"
	KindScript   NodeKind = "script"
	KindCommand  NodeKind = "command"
	KindLoop     NodeKind = "loop"
	KindApproval NodeKind = "approval"
	KindSpawn    NodeKind = "spawn"
)

// Kind returns the set discriminator (or KindUnknown if 0 / >1 are set).
// The MultiKind helper distinguishes 0 from >1.
func (n *Node) Kind() NodeKind {
	kinds := n.kinds()
	if len(kinds) == 1 {
		return kinds[0]
	}
	return KindUnknown
}

// Kinds returns every discriminator that was set. Length 0 = missing
// discriminator; length >1 = ambiguous.
func (n *Node) Kinds() []NodeKind { return n.kinds() }

func (n *Node) kinds() []NodeKind {
	out := make([]NodeKind, 0, 1)
	if n.Prompt != "" {
		out = append(out, KindPrompt)
	}
	if n.Bash != "" {
		out = append(out, KindBash)
	}
	if n.Script != nil {
		out = append(out, KindScript)
	}
	if n.Command != nil {
		out = append(out, KindCommand)
	}
	if n.Loop != nil {
		out = append(out, KindLoop)
	}
	if n.Approval != nil {
		out = append(out, KindApproval)
	}
	if n.Spawn != nil {
		out = append(out, KindSpawn)
	}
	return out
}

// Script is the `script:` node body — a named script invoked through a
// language runtime.
type Script struct {
	Name    string            `yaml:"name"`
	Runtime string            `yaml:"runtime,omitempty"`
	Args    []string          `yaml:"args,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`
}

// Command names a reusable command file under .orch/commands/<name>.md.
type Command struct {
	Name string            `yaml:"name"`
	Args map[string]string `yaml:"args,omitempty"`
}

// Loop is the `loop:` node body — iterative prompt with stop predicate.
type Loop struct {
	Prompt        string `yaml:"prompt"`
	Until         string `yaml:"until,omitempty"`
	MaxIterations int    `yaml:"max_iterations,omitempty"`
	FreshContext  bool   `yaml:"fresh_context,omitempty"`
}

// Approval is the `approval:` node body — interactive operator gate.
type Approval struct {
	Prompt string `yaml:"prompt"`
	Until  string `yaml:"until,omitempty"`
}

// Spawn is the `spawn:` node body — provisions a worker mid-workflow.
//
// Phase A treats Spawn as opaque: it is captured for validation
// (presence of name; assign-target wiring) but not interpreted. Phase B
// will replace this struct with the SpawnSpec type from Proposal 0002
// (orch#141) once that lands.
type Spawn struct {
	Name string `yaml:"name"`
	// Body preserves the raw YAML body for Phase B forwarding into the
	// SpawnSpec parser. Phase A only inspects Name.
	Body yaml.Node `yaml:"-"`
	// Raw is the round-trippable form of the spawn body, captured by
	// Parse for Phase B's SpawnSpec hand-off.
	Raw map[string]any `yaml:",inline"`
}
