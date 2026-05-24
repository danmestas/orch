package spawnspec

import (
	"bytes"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// MarshalSpecV2 serialises a v2 SpawnSpec to canonical YAML.
func MarshalSpecV2(s *SpawnSpecV2) ([]byte, error) {
	if s == nil {
		return nil, fmt.Errorf("spawnspec: cannot marshal nil SpawnSpecV2")
	}
	if s.SpecVersion == "" {
		s.SpecVersion = SpecVersionV2
	}
	return yaml.Marshal(s)
}

// UnmarshalSpecV2 parses a v2 SpawnSpec from YAML. Used by the
// version-aware UnmarshalAnySpec dispatcher; direct callers can use
// it when they already know the document is v2.
func UnmarshalSpecV2(data []byte) (*SpawnSpecV2, error) {
	var s SpawnSpecV2
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("spawnspec: empty document")
		}
		return nil, fmt.Errorf("spawnspec: yaml parse: %w", err)
	}
	if s.SpecVersion == "" {
		s.SpecVersion = SpecVersionV2
	}
	if s.SpecVersion != SpecVersionV2 {
		return nil, fmt.Errorf(
			"spawnspec: UnmarshalSpecV2 requires spec_version=v2 (got %q); use UnmarshalSpec for v1 or UnmarshalAnySpec for version-agnostic parsing",
			s.SpecVersion,
		)
	}
	return &s, nil
}

// MarshalHandleV2 serialises a v2 WorkerHandle to canonical YAML.
func MarshalHandleV2(h *WorkerHandleV2) ([]byte, error) {
	if h == nil {
		return nil, fmt.Errorf("spawnspec: cannot marshal nil WorkerHandleV2")
	}
	if h.SpecVersion == "" {
		h.SpecVersion = SpecVersionV2
	}
	return yaml.Marshal(h)
}

// UnmarshalHandleV2 parses a v2 WorkerHandle from YAML.
func UnmarshalHandleV2(data []byte) (*WorkerHandleV2, error) {
	var h WorkerHandleV2
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&h); err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("spawnspec: empty document")
		}
		return nil, fmt.Errorf("spawnspec: yaml parse: %w", err)
	}
	if h.SpecVersion == "" {
		h.SpecVersion = SpecVersionV2
	}
	if h.SpecVersion != SpecVersionV2 {
		return nil, fmt.Errorf(
			"spawnspec: UnmarshalHandleV2 requires spec_version=v2 (got %q)",
			h.SpecVersion,
		)
	}
	return &h, nil
}

// AnySpec is the discriminated-union view callers use when they want
// to accept any supported wire version. Exactly one of V1 / V2 is
// non-nil; the Version field repeats the chosen version for switch
// readability.
type AnySpec struct {
	Version string
	V1      *SpawnSpec
	V2      *SpawnSpecV2
}

// UnmarshalAnySpec parses a YAML document into the appropriate
// version-specific struct. Both v1 and v2 are accepted indefinitely
// per docs/spawn-spec-versioning.md; the document's `spec_version:`
// field selects the parser.
//
// A missing spec_version defaults to v1 (back-compat with documents
// authored before v2 existed).
func UnmarshalAnySpec(data []byte) (*AnySpec, error) {
	ver, err := peekSpecVersion(data)
	if err != nil {
		return nil, err
	}
	switch ver {
	case "", SpecVersionV1:
		s, err := UnmarshalSpec(data)
		if err != nil {
			return nil, err
		}
		return &AnySpec{Version: SpecVersionV1, V1: s}, nil
	case SpecVersionV2:
		s, err := UnmarshalSpecV2(data)
		if err != nil {
			return nil, err
		}
		return &AnySpec{Version: SpecVersionV2, V2: s}, nil
	default:
		return nil, fmt.Errorf(
			"spawnspec: unsupported spec_version %q (this binary speaks v1 and v2)",
			ver,
		)
	}
}

// peekSpecVersion does a lenient first-pass decode of the YAML
// document looking only at the `spec_version:` field. KnownFields is
// OFF for the peek so unrelated fields don't blow up the version
// check (the chosen-version parser will catch unknown fields on the
// second pass with KnownFields ON).
func peekSpecVersion(data []byte) (string, error) {
	var peek struct {
		SpecVersion string `yaml:"spec_version"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(false)
	if err := dec.Decode(&peek); err != nil {
		if err == io.EOF {
			return "", fmt.Errorf("spawnspec: empty document")
		}
		return "", fmt.Errorf("spawnspec: yaml peek: %w", err)
	}
	return peek.SpecVersion, nil
}
