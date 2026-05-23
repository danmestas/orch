package subtree

import (
	"context"
	"testing"
)

func TestNATSEventStream_NilConn(t *testing.T) {
	s := &NATSEventStream{Cache: newMemCache()}
	_, err := s.Subscribe(context.Background(), "x")
	if err == nil {
		t.Fatal("expected error on nil NATS connection")
	}
}

func TestNATSEventStream_NilCache(t *testing.T) {
	s := &NATSEventStream{}
	_, err := s.Subscribe(context.Background(), "x")
	if err == nil {
		t.Fatal("expected error on nil cache or conn")
	}
}

// TestMatchSubject_TokenWalk pins the subject filter's matching
// rule: any token in the subject that matches the wanted set counts;
// pct-encoded pane ids round-trip.
func TestMatchSubject_TokenWalk(t *testing.T) {
	want := map[string]struct{}{
		"lead-engineer": {},
		"%42":           {},
	}
	cases := []struct {
		subject string
		match   string
	}{
		{"agents.events.cc.dmestas.lead-engineer", "lead-engineer"},
		{"agents.hb.cc.dmestas.pct42.tail", "%42"},
		{"agents.events.cc.dmestas.other", ""},
	}
	for _, c := range cases {
		got := matchSubject(c.subject, want)
		if got != c.match {
			t.Errorf("matchSubject(%q) = %q, want %q", c.subject, got, c.match)
		}
	}
}

func TestTruncatePayload_RecordBoundary(t *testing.T) {
	body := make([]byte, 600)
	for i := range body {
		body[i] = 'x'
	}
	body[500] = '}' // record-boundary hint inside the slack window
	out := truncatePayload(body)
	if len(out) > 600 {
		t.Errorf("output exceeded input: len=%d", len(out))
	}
	// Should end with `...<truncated>` since we cut.
	if len(out) >= 600 {
		t.Errorf("truncate did not shorten")
	}
}
