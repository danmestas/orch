package main

import (
	"reflect"
	"testing"
)

// TestReorderSpyArgs exercises the bash-style intermixed-flag handling
// that bin/orch-spy supported via its while-loop case statement. Go's
// flag.Parse stops at the first non-flag; reorderSpyArgs moves all
// flags (and their values) ahead of positional args so Parse sees them.
func TestReorderSpyArgs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "flag after positional gets pulled forward",
			in:   []string{"--dry-run-brief", "operator", "--mission-file", "M"},
			want: []string{"--dry-run-brief", "--mission-file", "M", "operator"},
		},
		{
			name: "bool flag mixed with positional",
			in:   []string{"operator", "--headed", "audit"},
			want: []string{"--headed", "operator", "audit"},
		},
		{
			name: "--flag=value form keeps value attached",
			in:   []string{"operator", "--mission-file=/tmp/m", "go"},
			want: []string{"--mission-file=/tmp/m", "operator", "go"},
		},
		{
			name: "--quiet bool flag does not consume positional",
			in:   []string{"--quiet", "operator", "do thing"},
			want: []string{"--quiet", "operator", "do thing"},
		},
		{
			name: "no flags at all",
			in:   []string{"operator", "audit my session"},
			want: []string{"operator", "audit my session"},
		},
		{
			name: "double-dash terminator keeps following tokens positional",
			in:   []string{"--quiet", "--", "operator", "--mission-file"},
			want: []string{"--quiet", "operator", "--mission-file"},
		},
		{
			name: "stdin mission dash stays positional",
			in:   []string{"operator", "-"},
			want: []string{"operator", "-"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := reorderSpyArgs(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("reorderSpyArgs(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
