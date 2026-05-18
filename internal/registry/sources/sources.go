// Package sources collects the per-source readers the registry joins.
//
// Each source returns its own typed data. The join function in the parent
// registry package consumes them; sources do not know about each other.
//
// Sources are deliberately small and stateless where possible — they read
// from one place, return one shape, and degrade gracefully when their
// backing data is absent (file missing, NATS unreachable). Errors are
// surfaced; an empty result is not an error.
//
// Source interfaces (AgentReader, HeartbeatReader, AliasReader,
// OperatorReader) live in the parent registry package; implementations
// here satisfy those via structural typing.
package sources
