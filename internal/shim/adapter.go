// Package shim hosts the per-pane Synadia Agent Protocol bridge that
// fronts an arbitrary agent CLI. The Adapter interface is the seam
// between the protocol-aware shim core (service registration, chunk
// encoding, heartbeat) and a specific harness's stdio quirks (claude
// transcript JSONL, codex events.jsonl, pi extension events, ...).
//
// One adapter per harness. The shim wires it up; the adapter does not
// see NATS.
package shim

import "context"

// ChunkType is the discriminator on the §6.2 chunk wrapper.
type ChunkType string

const (
	// ChunkResponse is content emitted by the agent (§6.3). `Data` is
	// either a string or a JSON-marshallable object (e.g. {"text": "..."}).
	ChunkResponse ChunkType = "response"
	// ChunkStatus is a lifecycle signal (§6.4). v0.1 defines one value:
	// "ack", which the shim emits itself before invoking the adapter.
	// Adapters typically don't emit Status chunks.
	ChunkStatus ChunkType = "status"
	// ChunkQuery is a mid-stream question to the caller (§7.1). `Data`
	// is a QueryData struct.
	ChunkQuery ChunkType = "query"
	// ChunkThinking is a non-standard chunk type for surfacing the model's
	// reasoning trace. Unknown types are silently ignored by callers
	// per §6.6 — emitting these is forward-compatible.
	ChunkThinking ChunkType = "thinking"
	// ChunkToolUse is a non-standard chunk type for surfacing tool calls
	// the agent is making. See note on ChunkThinking re: forward-compat.
	ChunkToolUse ChunkType = "tool_use"
)

// Chunk is a single typed message destined for the prompt reply subject.
// The terminator (§6.5) is signalled by setting Terminator=true; the
// caller drains everything in the stream up to and including the
// terminator, then closes the stream.
type Chunk struct {
	Type ChunkType
	// Data is the chunk payload — either a string (for "response"/"status")
	// or any JSON-marshallable value. Set via NewResponseChunk /
	// NewStatusChunk / NewQueryChunk so the wire shape stays consistent.
	Data any
	// Terminator, when true, signals end-of-stream (§6.5). The other
	// fields are ignored. The shim publishes a zero-byte headerless
	// message and stops draining the channel for the current request.
	Terminator bool
	// Err, when non-nil, signals an error-terminated stream (§9.3, B.10).
	// The shim publishes one error-headered message before the terminator.
	// `Err.Code` becomes Nats-Service-Error-Code, `Err.Message` becomes
	// Nats-Service-Error, and `Err.Body` is the optional JSON body.
	Err *Error
}

// QueryData is the §7.1 query chunk payload.
type QueryData struct {
	ID           string `json:"id"`
	ReplySubject string `json:"reply_subject"`
	Prompt       string `json:"prompt"`
}

// Error is the wire-level error shape (§9). The shim handles header
// emission; the adapter only fills the struct.
type Error struct {
	Code    int            // Nats-Service-Error-Code, per §9.2 taxonomy
	Message string         // Nats-Service-Error, human-readable
	Body    map[string]any // optional JSON body, per §9.1
}

// Adapter bridges the shim's NATS plane to a specific agent CLI.
//
// Lifecycle:
//
//	1. The shim creates the adapter once at startup.
//	2. The shim calls Start(shimCtx) once, AFTER NATS registration is
//	   complete (so the adapter's first chunks have a service to attach
//	   to). Start binds long-lived background work (file tailers,
//	   marker watchers, ...) to shimCtx.
//	3. For each accepted prompt, the shim emits the §6.4 "ack" itself,
//	   then calls OnPrompt(promptCtx, text). OnPrompt should kick off
//	   the agent turn but MUST NOT block waiting for completion — chunks
//	   flow back through the Events() channel.
//	4. The shim drains Events() and publishes each chunk on the active
//	   reply subject until it sees Terminator=true.
//	5. On shutdown, Close() shuts down background work and closes the
//	   channel returned by Events() so the shim's event pump exits.
//
// Start's ctx is the shim's lifetime context — adapters MUST NOT bind
// long-lived background work to OnPrompt's ctx, because that one
// represents a single turn and cancellation must not dismantle the
// adapter.
//
// OnPrompt receives a context whose cancellation means "stop the
// current turn". Adapters MAY use it to interrupt the agent, but the
// v1 claude-code adapter does not (claude has no interrupt API);
// instead it relies on the marker file the host's Stop hook writes.
//
// Close() MUST be idempotent. The shim defers it from Run; tests often
// defer it too.
type Adapter interface {
	Start(ctx context.Context) error
	OnPrompt(ctx context.Context, text string) error
	Events() <-chan Chunk
	Close() error
}
