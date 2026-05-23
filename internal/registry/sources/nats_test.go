package sources

import (
	"testing"

	"github.com/nats-io/nats.go"
)

// parseAgentInfo is the deterministic / pure part of the NATS source.
// Live discovery + heartbeat subscription tests live in the bench
// (test/docker-sesh/) — they need a real NATS server.

func TestParseAgentInfo_FullPayload(t *testing.T) {
	body := []byte(`{
		"type":"io.nats.micro.v1.info_response",
		"id":"abc123",
		"name":"agents",
		"metadata": {"pane_id":"%64","role":"worker","agent":"claude-code"},
		"endpoints":[
			{"name":"prompt","subject":"agents.prompt.cc.dmestas.lead-engineer"},
			{"name":"status","subject":"agents.status.cc.dmestas.lead-engineer"}
		]
	}`)
	info, err := parseAgentInfo(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if info.InstanceID != "abc123" {
		t.Errorf("instance id: got %q want abc123", info.InstanceID)
	}
	if info.Metadata["pane_id"] != "%64" {
		t.Errorf("metadata projection: %+v", info.Metadata)
	}
	if len(info.Endpoints) != 2 {
		t.Fatalf("endpoints: got %d want 2", len(info.Endpoints))
	}
}

func TestParseAgentInfo_PreservesInstanceIDInMetadata(t *testing.T) {
	// Proposal 0009 / issue #181: shim publishes metadata.instance_id as
	// the stable slug. parseAgentInfo's metadata projection is generic so
	// the slug rides through automatically; this test pins the contract
	// in place so a future "promote selected metadata fields to typed
	// AgentInfo struct" refactor doesn't silently drop the slug.
	body := []byte(`{
		"id":"abc123",
		"name":"agents",
		"metadata": {
			"pane_id":"%64",
			"instance_id":"lead-engineer",
			"agent":"claude-code",
			"owner":"dmestas"
		}
	}`)
	info, err := parseAgentInfo(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if info.Metadata["instance_id"] != "lead-engineer" {
		t.Errorf("metadata.instance_id not preserved: got %q want lead-engineer",
			info.Metadata["instance_id"])
	}
	// AgentInfo.InstanceID still surfaces the micro-service id (a per-process
	// UUID); the join layer promotes metadata.instance_id over it.
	if info.InstanceID != "abc123" {
		t.Errorf("AgentInfo.InstanceID should hold micro id: got %q", info.InstanceID)
	}
}

func TestParseAgentInfo_AbsentMetadataDefaultsEmpty(t *testing.T) {
	body := []byte(`{"id":"abc123","name":"agents"}`)
	info, err := parseAgentInfo(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if info.Metadata == nil {
		t.Errorf("metadata should be non-nil even when absent in payload")
	}
}

func TestParseAgentInfo_RejectsMalformedJSON(t *testing.T) {
	_, err := parseAgentInfo([]byte("not json"))
	if err == nil {
		t.Errorf("malformed JSON should surface error")
	}
}

func TestPaneFromHeartbeat_PrefersJSONBody(t *testing.T) {
	msg := &nats.Msg{
		Subject: "agents.hb.cc.dmestas.pct64",
		Data:    []byte(`{"pane_id":"%99"}`),
	}
	if got := paneFromHeartbeat(msg); got != "%99" {
		t.Errorf("json body should win: got %q want %%99", got)
	}
}

func TestPaneFromHeartbeat_FallsBackToSubjectToken(t *testing.T) {
	msg := &nats.Msg{
		Subject: "agents.hb.cc.dmestas.pct64",
		Data:    []byte{},
	}
	if got := paneFromHeartbeat(msg); got != "%64" {
		t.Errorf("subject fallback: got %q want %%64", got)
	}
}

func TestPaneFromHeartbeat_NoIdentityReturnsEmpty(t *testing.T) {
	msg := &nats.Msg{Subject: "agents.hb", Data: []byte{}}
	if got := paneFromHeartbeat(msg); got != "" {
		t.Errorf("short subject should yield empty: got %q", got)
	}
}
