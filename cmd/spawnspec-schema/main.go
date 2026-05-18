// spawnspec-schema generates and writes the published JSON Schema for
// SpawnSpec and WorkerHandle. The Go structs in internal/spawnspec are
// canonical; this tool exists so non-Go consumers (TS, Python, IDE
// plugins) can validate without hand-maintaining a parallel schema.
//
// Usage:
//
//	go run ./cmd/spawnspec-schema -out dist/schema
//
// Two files are written:
//
//	<out>/spawn-spec.v1.json
//	<out>/worker-handle.v1.json
//
// The generator is idempotent; CI re-runs it and fails the diff if the
// checked-in schema drifts from the Go structs.
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

	specPath := filepath.Join(*out, "spawn-spec.v1.json")
	specJSON, err := spawnspec.SpecSchema()
	if err != nil {
		fail("generate spawn-spec schema: %v", err)
	}
	if err := os.WriteFile(specPath, specJSON, 0o644); err != nil {
		fail("write %s: %v", specPath, err)
	}
	fmt.Printf("wrote %s (%d bytes)\n", specPath, len(specJSON))

	handlePath := filepath.Join(*out, "worker-handle.v1.json")
	handleJSON, err := spawnspec.HandleSchema()
	if err != nil {
		fail("generate worker-handle schema: %v", err)
	}
	if err := os.WriteFile(handlePath, handleJSON, 0o644); err != nil {
		fail("write %s: %v", handlePath, err)
	}
	fmt.Printf("wrote %s (%d bytes)\n", handlePath, len(handleJSON))
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "spawnspec-schema: "+format+"\n", args...)
	os.Exit(1)
}
