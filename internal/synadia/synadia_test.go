package synadia

import (
	"testing"

	"github.com/nats-io/nats.go"
)

func TestExitCodeForServiceError(t *testing.T) {
	// The mapping must match the historical bin/orch-tell behaviour
	// exactly so call-site `exit $rc` semantics survive the bash→Go
	// migration (#189). The five table rows below match the five
	// branches of the old `_exit_for_error_code` helper.
	cases := []struct {
		code int
		want int
	}{
		{400, ExitBadRequest},
		{401, ExitUnauthorized},
		{402, ExitUnauthorized},
		{403, ExitUnauthorized},
		{404, ExitNotFound},
		{409, ExitConflict},
		{429, ExitTooManyRequests},
		{500, ExitGeneric},
		{502, ExitGeneric},
		{503, ExitGeneric},

		// Edges between the named bands.
		{405, ExitGeneric},
		{410, ExitGeneric},
		{428, ExitGeneric},
		{430, ExitGeneric},

		// Out of band — degrade to ExitGeneric per docstring.
		{0, ExitGeneric},
		{-1, ExitGeneric},
		{999, ExitGeneric},
	}
	for _, c := range cases {
		got := ExitCodeForServiceError(c.code)
		if got != c.want {
			t.Errorf("ExitCodeForServiceError(%d) = %d, want %d", c.code, got, c.want)
		}
	}
}

func TestIsTerminator(t *testing.T) {
	// §6.5 terminator: empty body AND no headers.
	got := IsTerminator(&nats.Msg{Data: []byte{}, Header: nats.Header{}})
	if !got {
		t.Errorf("empty body + no headers should be a terminator")
	}

	// Non-empty body — not a terminator (regular response / ack chunk).
	got = IsTerminator(&nats.Msg{Data: []byte("{}")})
	if got {
		t.Errorf("non-empty body should not be a terminator")
	}

	// Headers present — error-path message, not a terminator.
	h := nats.Header{}
	h.Set("Nats-Service-Error-Code", "404")
	got = IsTerminator(&nats.Msg{Data: []byte{}, Header: h})
	if got {
		t.Errorf("empty body but headers present should not be a terminator")
	}

	// Nil msg — defensive guard.
	if IsTerminator(nil) {
		t.Errorf("nil msg should return false")
	}
}

func TestAdapterMissingExitCode(t *testing.T) {
	// Snapshot the spec value so a casual rename can't drift it. The
	// shim and orch-spawn both depend on exactly 2 (see bin/orch-spawn
	// adapter probe).
	if AdapterMissingExitCode != 2 {
		t.Errorf("AdapterMissingExitCode = %d, spec says 2", AdapterMissingExitCode)
	}
}

func TestTerminatorByte(t *testing.T) {
	if TerminatorByte != 0x00 {
		t.Errorf("TerminatorByte = %#x, spec §6.5 says 0x00", TerminatorByte)
	}
}
