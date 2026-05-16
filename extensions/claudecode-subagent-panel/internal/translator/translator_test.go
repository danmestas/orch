package translator

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func fixedMeta() AgentMeta {
	return AgentMeta{
		AgentID:   "pct37",
		Harness:   "claude-code",
		PaneID:    "%37",
		SessionID: "e7be6d18-65ed-45a4-836c-d4a650373577",
		CWD:       "/Users/op/projects/foo",
		GitBranch: "main",
		CCVersion: "2.1.143",
		Slug:      "snazzy-percolating-breeze",
	}
}

func TestSeedLineShape(t *testing.T) {
	meta := fixedMeta()
	now := time.Date(2026, 5, 16, 16, 44, 47, int(846*time.Millisecond), time.UTC)
	line, err := SeedLine(meta, now)
	if err != nil {
		t.Fatalf("SeedLine: %v", err)
	}
	if line.Bytes == nil || line.Bytes[len(line.Bytes)-1] != '\n' {
		t.Fatalf("expected newline-terminated bytes")
	}
	if line.Next.ParentUUID == "" {
		t.Fatalf("expected non-empty parent uuid cursor")
	}

	var m map[string]any
	if err := json.Unmarshal(line.Bytes, &m); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	want := map[string]any{
		"isSidechain": true,
		"agentId":     "pct37",
		"type":        "user",
		"userType":    "external",
		"entrypoint":  "cli",
		"cwd":         "/Users/op/projects/foo",
		"sessionId":   "e7be6d18-65ed-45a4-836c-d4a650373577",
		"version":     "2.1.143",
		"gitBranch":   "main",
		"slug":        "snazzy-percolating-breeze",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("field %q: got %v, want %v", k, m[k], v)
		}
	}
	if m["parentUuid"] != nil {
		t.Errorf("parentUuid should be null on first line, got %v", m["parentUuid"])
	}
	msg, _ := m["message"].(map[string]any)
	if msg == nil || msg["role"] != "user" {
		t.Errorf("message.role missing or wrong")
	}
	if !strings.Contains(msg["content"].(string), "%37") {
		t.Errorf("seed line should mention pane id, got: %v", msg["content"])
	}
	if !strings.Contains(msg["content"].(string), "claude-code") {
		t.Errorf("seed line should mention harness, got: %v", msg["content"])
	}
}

func TestAssistantLineForResponseChunk(t *testing.T) {
	meta := fixedMeta()
	now := time.Now()
	cur := Cursor{ParentUUID: "seed-uuid"}
	line, err := AssistantLine(meta, cur, Chunk{Type: "response", Text: "hello world"}, now)
	if err != nil {
		t.Fatalf("AssistantLine: %v", err)
	}
	if line.Bytes == nil {
		t.Fatalf("expected non-nil bytes for response chunk")
	}
	var m map[string]any
	if err := json.Unmarshal(line.Bytes, &m); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if m["parentUuid"] != "seed-uuid" {
		t.Errorf("expected parent uuid chained, got %v", m["parentUuid"])
	}
	if m["type"] != "assistant" {
		t.Errorf("expected type=assistant, got %v", m["type"])
	}
	msg := m["message"].(map[string]any)
	if msg["model"] != "orch-claude-code" {
		t.Errorf("expected model=orch-claude-code, got %v", msg["model"])
	}
	if msg["stop_reason"] != nil {
		t.Errorf("non-final chunk should not set stop_reason, got %v", msg["stop_reason"])
	}
	content := msg["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected one content block, got %d", len(content))
	}
	c0 := content[0].(map[string]any)
	if c0["type"] != "text" || c0["text"] != "hello world" {
		t.Errorf("content block wrong: %v", c0)
	}
}

func TestAssistantLineForTerminator(t *testing.T) {
	meta := fixedMeta()
	line, err := AssistantLine(meta, Cursor{ParentUUID: "x"}, Chunk{Terminator: true}, time.Now())
	if err != nil {
		t.Fatalf("AssistantLine terminator: %v", err)
	}
	if line.Bytes == nil {
		t.Fatalf("terminator should still emit a line (with stop_reason)")
	}
	var m map[string]any
	_ = json.Unmarshal(line.Bytes, &m)
	stopReason := m["message"].(map[string]any)["stop_reason"]
	if stopReason != "end_turn" {
		t.Errorf("expected stop_reason=end_turn, got %v", stopReason)
	}
}

func TestAssistantLineForAckIsNoOp(t *testing.T) {
	meta := fixedMeta()
	line, err := AssistantLine(meta, Cursor{ParentUUID: "x"}, Chunk{Type: "status", Text: "ack"}, time.Now())
	if err != nil {
		t.Fatalf("AssistantLine ack: %v", err)
	}
	if line.Bytes != nil {
		t.Fatalf("ack chunk should be a no-op (no line emitted)")
	}
	if line.Next.ParentUUID != "x" {
		t.Errorf("cursor should pass through unchanged on no-op, got %q", line.Next.ParentUUID)
	}
}

func TestAssistantLineForError(t *testing.T) {
	meta := fixedMeta()
	line, err := AssistantLine(meta, Cursor{ParentUUID: "x"}, Chunk{
		Type: "error", ErrCode: "500", ErrMessage: "boom",
	}, time.Now())
	if err != nil {
		t.Fatalf("AssistantLine error: %v", err)
	}
	if line.Bytes == nil {
		t.Fatalf("error chunk should emit a line")
	}
	if !strings.Contains(string(line.Bytes), "[error: 500 boom]") {
		t.Errorf("expected error rendering, got: %s", line.Bytes)
	}
}

func TestRandomSlugShape(t *testing.T) {
	s, err := RandomSlug()
	if err != nil {
		t.Fatalf("RandomSlug: %v", err)
	}
	parts := strings.Split(s, "-")
	if len(parts) != 3 {
		t.Errorf("expected three hyphen-joined parts, got %q", s)
	}
}

func TestRenderTextUnknownType(t *testing.T) {
	text, stop := renderText(Chunk{Type: "newchunk", Text: "hi"})
	if stop {
		t.Errorf("unknown chunk should not stop turn")
	}
	if text != "[newchunk: hi]" {
		t.Errorf("expected breadcrumb for unknown chunk, got %q", text)
	}
}
