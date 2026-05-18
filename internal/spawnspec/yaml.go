package spawnspec

import (
	"bytes"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// MarshalSpec serialises a SpawnSpec to canonical YAML. Empty optional
// fields are elided (omitempty tags); the executor discriminator block
// is emitted last because Go struct order is preserved by yaml.v3 and
// putting it last is the convention in the Archon-shaped example.
func MarshalSpec(s *SpawnSpec) ([]byte, error) {
	if s == nil {
		return nil, fmt.Errorf("spawnspec: cannot marshal nil SpawnSpec")
	}
	if s.SpecVersion == "" {
		s.SpecVersion = SpecVersion
	}
	return yaml.Marshal(s)
}

// UnmarshalSpec parses a SpawnSpec from YAML, defaults SpecVersion to
// v1 if absent, and rejects anything else. The returned spec is not
// yet validated — call ValidateSpec separately so callers can compose
// parse + validate stages (e.g. `orch-spawn --validate-spec` reports
// version mismatch as a parse error, schema breaches as validation
// errors).
func UnmarshalSpec(data []byte) (*SpawnSpec, error) {
	var s SpawnSpec
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("spawnspec: empty document")
		}
		return nil, fmt.Errorf("spawnspec: yaml parse: %w", err)
	}
	if s.SpecVersion == "" {
		s.SpecVersion = SpecVersion
	}
	if s.SpecVersion != SpecVersion {
		return nil, fmt.Errorf(
			"spawnspec: unsupported spec_version %q (this binary speaks %q)",
			s.SpecVersion, SpecVersion,
		)
	}
	return &s, nil
}

// MarshalHandle serialises a WorkerHandle to canonical YAML.
func MarshalHandle(h *WorkerHandle) ([]byte, error) {
	if h == nil {
		return nil, fmt.Errorf("spawnspec: cannot marshal nil WorkerHandle")
	}
	if h.SpecVersion == "" {
		h.SpecVersion = SpecVersion
	}
	return yaml.Marshal(h)
}

// UnmarshalHandle parses a WorkerHandle. Same version-gate behaviour
// as UnmarshalSpec.
func UnmarshalHandle(data []byte) (*WorkerHandle, error) {
	var h WorkerHandle
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&h); err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("spawnspec: empty document")
		}
		return nil, fmt.Errorf("spawnspec: yaml parse: %w", err)
	}
	if h.SpecVersion == "" {
		h.SpecVersion = SpecVersion
	}
	if h.SpecVersion != SpecVersion {
		return nil, fmt.Errorf(
			"spawnspec: unsupported spec_version %q (this binary speaks %q)",
			h.SpecVersion, SpecVersion,
		)
	}
	return &h, nil
}
