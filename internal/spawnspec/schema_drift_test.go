package spawnspec_test

// CI drift-gate for the published JSON Schema artifacts.
//
// Decision #3 of proposal 0002 (Issue #141): the Go structs in
// internal/spawnspec are the source of truth, and dist/schema/*.json
// are generated artifacts published for non-Go consumers (TS UI,
// Python validators, IDE plugins). They must not drift.
//
// This test regenerates the schema in-process via the same public
// functions cmd/spawnspec-schema uses (spawnspec.SpecSchema,
// spawnspec.HandleSchema), reads the committed dist/schema file, and
// compares both after normalising to map[string]any so whitespace
// and key-order differences don't false-fail.
//
// If you see this test fail, you changed the Go structs (types.go,
// the Agent enum, struct tags, etc.) without regenerating the
// published schema. Run:
//
//	go run ./cmd/spawnspec-schema -out dist/schema
//
// then commit the updated dist/schema/*.json alongside your code
// change.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/danmestas/orch/internal/spawnspec"
)

const regenCmd = "go run ./cmd/spawnspec-schema -out dist/schema"

func TestSchemaDrift(t *testing.T) {
	cases := []struct {
		name     string
		generate func() ([]byte, error)
		// distRel is the path relative to the repo root.
		distRel string
	}{
		{
			name:     "spawn-spec.v1.json",
			generate: spawnspec.SpecSchema,
			distRel:  "dist/schema/spawn-spec.v1.json",
		},
		{
			name:     "worker-handle.v1.json",
			generate: spawnspec.HandleSchema,
			distRel:  "dist/schema/worker-handle.v1.json",
		},
		{
			name:     "spawn-spec.v2.json",
			generate: spawnspec.SpecSchemaV2,
			distRel:  "dist/schema/spawn-spec.v2.json",
		},
		{
			name:     "worker-handle.v2.json",
			generate: spawnspec.HandleSchemaV2,
			distRel:  "dist/schema/worker-handle.v2.json",
		},
	}

	root := repoRoot(t)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			generated, err := tc.generate()
			if err != nil {
				t.Fatalf("generate schema: %v", err)
			}

			distPath := filepath.Join(root, tc.distRel)
			committed, err := os.ReadFile(distPath)
			if err != nil {
				t.Fatalf("read committed schema %s: %v", distPath, err)
			}

			gotMap, err := normalize(generated)
			if err != nil {
				t.Fatalf("normalize generated schema: %v", err)
			}
			wantMap, err := normalize(committed)
			if err != nil {
				t.Fatalf("normalize committed schema %s: %v", distPath, err)
			}

			if !reflect.DeepEqual(gotMap, wantMap) {
				t.Fatalf(
					"schema drift detected: %s does not match the schema generated "+
						"from internal/spawnspec Go structs.\n"+
						"Run `%s` and commit the updated file(s).",
					tc.distRel, regenCmd,
				)
			}
		})
	}
}

// normalize deserialises JSON bytes into a generic map so that
// whitespace, indentation, and (because JSON object keys are unordered)
// key order do not influence equality. Arrays remain ordered, which
// matches the schema's contract: $defs is a map, "required" arrays are
// ordered, "enum" arrays are ordered.
func normalize(b []byte) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// repoRoot resolves the orch repo root by walking up from this test
// file. We avoid hard-coding paths so the test works in worktrees and
// CI runners alike.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// thisFile is .../internal/spawnspec/schema_drift_test.go.
	// Repo root is two levels up.
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
}
