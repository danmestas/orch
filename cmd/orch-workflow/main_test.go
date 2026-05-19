package main

import (
	"flag"
	"strings"
	"testing"
)

func TestParseOnePositional(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantPath   string
		wantPrint  bool
		wantFleet  string
		wantErrSub string
	}{
		{
			name:     "flags before positional (legacy)",
			args:     []string{"--print", "--fleet=a,b", "foo.yaml"},
			wantPath: "foo.yaml", wantPrint: true, wantFleet: "a,b",
		},
		{
			name:     "positional before flags (Unix style)",
			args:     []string{"foo.yaml", "--print", "--fleet=a,b"},
			wantPath: "foo.yaml", wantPrint: true, wantFleet: "a,b",
		},
		{
			name:     "flags both sides",
			args:     []string{"--print", "foo.yaml", "--fleet=a"},
			wantPath: "foo.yaml", wantPrint: true, wantFleet: "a",
		},
		{
			name:     "no flags",
			args:     []string{"foo.yaml"},
			wantPath: "foo.yaml",
		},
		{
			name: "empty args",
			args: nil,
		},
		{
			name:       "two positionals errors",
			args:       []string{"foo.yaml", "bar.yaml"},
			wantErrSub: "unexpected positional",
		},
		{
			name:       "two positionals interleaved with flags",
			args:       []string{"foo.yaml", "--print", "bar.yaml"},
			wantErrSub: "unexpected positional",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("compile", flag.ContinueOnError)
			print := fs.Bool("print", false, "")
			fleet := fs.String("fleet", "", "")
			got, err := parseOnePositional(fs, tc.args)
			if tc.wantErrSub != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrSub) {
					t.Fatalf("err = %v; want substring %q", err, tc.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseOnePositional(%v) returned err: %v", tc.args, err)
			}
			if got != tc.wantPath {
				t.Errorf("path = %q; want %q", got, tc.wantPath)
			}
			if *print != tc.wantPrint {
				t.Errorf("--print = %v; want %v", *print, tc.wantPrint)
			}
			if *fleet != tc.wantFleet {
				t.Errorf("--fleet = %q; want %q", *fleet, tc.wantFleet)
			}
		})
	}
}
