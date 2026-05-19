package subtree

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/user"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/danmestas/orch/internal/spawnspec"
)

// envRefRE matches `$VAR` style references inside scalar string values
// (Sesh.Existing, SeshSpawn.Cwd, WorkerEntry.Cwd, WorkerEntry.Env).
// Resolution is shell-style: missing vars expand to the empty string;
// $$ is a literal $.
var envRefRE = regexp.MustCompile(`\$([A-Z_][A-Z0-9_]*|\{[A-Z_][A-Z0-9_]*\})`)

// ParseFile reads a topology YAML from disk. Does NOT run Validate —
// callers compose parse + validate so they can distinguish "your YAML
// is malformed" from "your YAML is well-formed but violates the
// topology contract". This mirrors workflow.ParseFile.
func ParseFile(path string) (*Topology, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	t, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return t, nil
}

// Parse decodes YAML from r into a *Topology. Strict mode: unknown
// fields at any level are a parse error so typos like `worker:`
// surface immediately.
//
// Defaults applied during parse (resolution-time substitutions are
// kept separate via ResolveEnv so tests can run without environment
// state):
//
//   - Topology.SpecVersion ← SpecVersion
//   - Each WorkerEntry.SpecVersion ← spawnspec.SpecVersion
//   - Each WorkerEntry.Owner ← Topology owner default (current $USER)
//     when empty
func Parse(r io.Reader) (*Topology, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return ParseBytes(raw)
}

// ParseBytes is the in-memory variant of Parse.
func ParseBytes(b []byte) (*Topology, error) {
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, fmt.Errorf("topology yaml: empty document")
	}

	var root yaml.Node
	if err := yaml.Unmarshal(b, &root); err != nil {
		return nil, err
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil, fmt.Errorf("topology yaml: empty document")
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return nil, fmt.Errorf(
			"topology yaml: top level must be a mapping, got %s",
			kindName(doc.Kind),
		)
	}

	t := &Topology{SourceLine: doc.Line}

	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(t); err != nil {
		return nil, err
	}

	if t.SpecVersion == "" {
		t.SpecVersion = SpecVersion
	}
	if t.SpecVersion != SpecVersion {
		return nil, fmt.Errorf(
			"topology yaml: unsupported spec_version %q (this binary speaks %q)",
			t.SpecVersion, SpecVersion,
		)
	}

	// Attach source-line info to workers and propagate the topology's
	// default owner so each worker's SpawnSpec carries it when the
	// operator didn't repeat it per worker.
	if workersNode := mappingValue(doc, "workers"); workersNode != nil && workersNode.Kind == yaml.SequenceNode {
		for i, child := range workersNode.Content {
			if i >= len(t.Workers) {
				break
			}
			t.Workers[i].SourceLine = child.Line
		}
	}

	applyWorkerDefaults(t)
	return t, nil
}

// applyWorkerDefaults fills SpawnSpec.SpecVersion on each worker and
// propagates the operator's default $USER as Owner when absent.
// Done at parse time so validation sees the post-default shape — a
// rule check against an empty SpecVersion is a parse bug, not an
// operator-correctable error.
func applyWorkerDefaults(t *Topology) {
	owner := defaultOwner()
	for i := range t.Workers {
		w := &t.Workers[i]
		if w.SpawnSpec.SpecVersion == "" {
			w.SpawnSpec.SpecVersion = spawnspec.SpecVersion
		}
		if w.SpawnSpec.Owner == "" && owner != "" {
			w.SpawnSpec.Owner = owner
		}
	}
}

// ResolveEnv substitutes $ENV-VAR references in fields that accept
// them: Sesh.Existing, Sesh.Spawn.Cwd, every WorkerEntry.Cwd, every
// WorkerEntry.Env value. Unknown vars expand to "" (shell-style).
//
// Resolution is separate from parse so tests can construct a Topology
// directly without an environment ambush. The CLI dispatches
// ParseFile → ResolveEnv → Validate → Apply.
func ResolveEnv(t *Topology, lookup func(string) string) {
	if lookup == nil {
		lookup = os.Getenv
	}
	t.Sesh.Existing = expandEnv(t.Sesh.Existing, lookup)
	if t.Sesh.Spawn != nil {
		t.Sesh.Spawn.Cwd = expandEnv(t.Sesh.Spawn.Cwd, lookup)
	}
	for i := range t.Workers {
		w := &t.Workers[i]
		w.SpawnSpec.Cwd = expandEnv(w.SpawnSpec.Cwd, lookup)
		for k, v := range w.SpawnSpec.Env {
			w.SpawnSpec.Env[k] = expandEnv(v, lookup)
		}
	}
}

func expandEnv(s string, lookup func(string) string) string {
	if s == "" || !strings.ContainsRune(s, '$') {
		return s
	}
	return envRefRE.ReplaceAllStringFunc(s, func(match string) string {
		name := strings.TrimPrefix(match, "$")
		name = strings.TrimPrefix(strings.TrimSuffix(name, "}"), "{")
		return lookup(name)
	})
}

func defaultOwner() string {
	if u := os.Getenv("ORCH_OWNER"); u != "" {
		return u
	}
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if cu, err := user.Current(); err == nil && cu != nil {
		return cu.Username
	}
	return ""
}

func mappingValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func kindName(k yaml.Kind) string {
	switch k {
	case yaml.DocumentNode:
		return "document"
	case yaml.SequenceNode:
		return "sequence"
	case yaml.MappingNode:
		return "mapping"
	case yaml.ScalarNode:
		return "scalar"
	case yaml.AliasNode:
		return "alias"
	default:
		return "unknown"
	}
}
