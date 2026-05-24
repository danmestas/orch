// Package synadia centralises the protocol constants and helpers that orch
// uses to talk to the Synadia Agent Protocol bus.
//
// The full protocol spec lives in the synadia-agents repo at
// docs/sap.md (Synadia Agent Protocol). Sections referenced here:
//
//	§2 — endpoint & subject layout (agents.prompt.* / agents.status.*)
//	§6.2 — response chunk shape ({"type":"response","data":"..."})
//	§6.4 — ack chunk (sent before §6.2 chunks; type:"ack")
//	§6.5 — terminator chunk (zero-byte body, no headers)
//	§6.6 — unknown chunk types must be silently dropped
//	§9 — error reporting via Nats-Service-Error[-Code] headers
//	§9.1 — optional JSON body {error, message, retry_after_s}
//
// Centralising the numbers here means consumers (orch-spawn's adapter probe,
// the orch-goal-stop-account daemon, the cmd/orch tell/ask paths) don't
// embed magic numbers — the spec mapping changes in one place. The bash
// CLIs that historically hard-coded the same numbers were replaced by Go
// code under cmd/orch in issue #189; the constants now flow from this
// package outward.
package synadia

import "github.com/nats-io/nats.go"

// AdapterMissingExitCode is the exit status orch-agent-shim returns when
// invoked with an --agent value that has no compiled adapter (codex / pi /
// gemini before their respective adapter ships). orch-spawn probes the
// shim with this exit code to decide whether to launch it; if the shim
// would fail with code 2, orch-spawn skips it and emits a one-line warning.
// See bin/orch-spawn around line 943.
const AdapterMissingExitCode = 2

// TerminatorByte is the body of the §6.5 terminator chunk. A reply with
// empty body AND no headers signals end-of-stream for a prompt; consumers
// must not treat it as a §6.2 response chunk.
//
// In practice the terminator is a zero-length body (not a single byte), so
// IsTerminator below is the only correct way to test for it. The named
// constant exists so call sites can document intent without referencing
// the literal zero in expressions like `len(msg.Data) == 0`.
const TerminatorByte byte = 0x00

// Stable exit codes that orch CLIs return when an agent surfaces a §9
// Nats-Service-Error header. The bash bin/orch-tell hard-coded these
// numbers in its `_exit_for_error_code` helper; the Go reimplementation
// (cmd/orch/tell.go) imports the table from here.
//
// Caller contract: a non-zero exit means the agent rejected the prompt;
// stdout still carries whatever response chunks streamed before the error.
const (
	ExitOK                  = 0
	ExitGeneric             = 1 // 5xx and unknown error codes
	ExitBadRequest          = 2 // 400
	ExitUnauthorized        = 3 // 401, 402, 403
	ExitNotFound            = 4 // 404
	ExitConflict            = 5 // 409
	ExitTooManyRequests     = 6 // 429
)

// ExitCodeForServiceError maps a §9 Nats-Service-Error-Code header value
// to the orch CLI's exit code. Unknown / out-of-range codes degrade to
// ExitGeneric so a caller's `exit $rc` always lands on a documented value.
//
// The mapping mirrors the pre-#189 bash bin/orch-tell behaviour exactly so
// scripts that branched on the old exit codes don't break.
func ExitCodeForServiceError(code int) int {
	switch {
	case code == 400:
		return ExitBadRequest
	case code >= 401 && code <= 403:
		return ExitUnauthorized
	case code == 404:
		return ExitNotFound
	case code == 409:
		return ExitConflict
	case code == 429:
		return ExitTooManyRequests
	case code >= 500:
		return ExitGeneric
	default:
		return ExitGeneric
	}
}

// IsTerminator returns true for §6.5 terminators: zero-length body AND no
// headers. Regular response chunks are non-empty JSON; error-path messages
// carry Nats-Service-Error headers. The §6.4 ack chunk is also zero-header
// but is non-empty JSON, so the empty-body guard is sufficient on its own.
//
// Consumers that already classify by chunk type (§6.2 / §6.4 / §6.6) can
// call this as a cheap pre-filter before JSON unmarshalling.
func IsTerminator(msg *nats.Msg) bool {
	if msg == nil {
		return false
	}
	return len(msg.Data) == 0 && len(msg.Header) == 0
}
