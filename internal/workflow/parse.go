package workflow

import (
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// ParseFile is the disk-loading convenience. It does not run Validate —
// call Validate(wf) on the returned value separately so callers can
// distinguish "parse failed, can't even read your YAML" from "your
// YAML is well-formed but the workflow has logical errors".
func ParseFile(path string) (*Workflow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	wf, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return wf, nil
}

// Parse decodes YAML from r into a *Workflow. Decoding is strict — an
// unknown field at any level is a parse error, so typos like `depend_on:`
// surface at parse time instead of being silently dropped.
//
// Source lines are populated from the YAML AST so downstream
// diagnostics can quote the operator's file:line position.
func Parse(r io.Reader) (*Workflow, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return parseBytes(raw)
}

// ParseBytes mirrors Parse for callers who already hold the bytes.
func ParseBytes(b []byte) (*Workflow, error) { return parseBytes(b) }

func parseBytes(b []byte) (*Workflow, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(b, &root); err != nil {
		return nil, err
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil, fmt.Errorf("workflow yaml: empty document")
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("workflow yaml: top level must be a mapping, got %s",
			kindName(doc.Kind))
	}

	wf := &Workflow{SourceLine: doc.Line}

	// Strict decode into the typed shape — unknown top-level fields fail.
	dec := yaml.NewDecoder(byteReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(wf); err != nil {
		return nil, err
	}

	// Walk the YAML AST to attach source-line info to nodes. Strict
	// decode above already enforced the schema; this pass only annotates.
	if nodesNode := mappingValue(doc, "nodes"); nodesNode != nil && nodesNode.Kind == yaml.SequenceNode {
		for i, child := range nodesNode.Content {
			if i >= len(wf.Nodes) {
				break
			}
			wf.Nodes[i].SourceLine = child.Line
		}
	}
	return wf, nil
}

func byteReader(b []byte) io.Reader { return &byteReaderImpl{b: b} }

type byteReaderImpl struct {
	b []byte
	i int
}

func (r *byteReaderImpl) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
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
