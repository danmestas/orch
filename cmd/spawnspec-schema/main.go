// spawnspec-schema generates and writes the published JSON Schemas for
// SpawnSpec and WorkerHandle, in every supported version. The Go
// structs in internal/spawnspec are canonical; this tool exists so
// non-Go consumers (TS, Python, IDE plugins) can validate without
// hand-maintaining a parallel schema.
//
// Usage:
//
//	go run ./cmd/spawnspec-schema -out dist/schema
//
// Four files are written:
//
//	<out>/spawn-spec.v1.json
//	<out>/worker-handle.v1.json
//	<out>/spawn-spec.v2.json
//	<out>/worker-handle.v2.json
//
// The generator is idempotent; CI re-runs it and fails the diff if the
// checked-in schema drifts from the Go structs. v1 is frozen per the
// SpawnSpec versioning policy (docs/spawn-spec-versioning.md); v2 is
// the active wire format for cmux / zmx / layout=none.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/danmestas/orch/internal/spawnspec"
)

func main() {
	out := flag.String("out", "dist/schema", "directory to write schema files into")
	flag.Parse()

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fail("mkdir %s: %v", *out, err)
	}

	type schemaJob struct {
		path     string
		generate func() ([]byte, error)
	}
	jobs := []schemaJob{
		{filepath.Join(*out, "spawn-spec.v1.json"), spawnspec.SpecSchema},
		{filepath.Join(*out, "worker-handle.v1.json"), spawnspec.HandleSchema},
		{filepath.Join(*out, "spawn-spec.v2.json"), spawnspec.SpecSchemaV2},
		{filepath.Join(*out, "worker-handle.v2.json"), spawnspec.HandleSchemaV2},
	}
	for _, j := range jobs {
		data, err := j.generate()
		if err != nil {
			fail("generate %s: %v", filepath.Base(j.path), err)
		}
		if err := os.WriteFile(j.path, data, 0o644); err != nil {
			fail("write %s: %v", j.path, err)
		}
		fmt.Printf("wrote %s (%d bytes)\n", j.path, len(data))
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "spawnspec-schema: "+format+"\n", args...)
	os.Exit(1)
}
