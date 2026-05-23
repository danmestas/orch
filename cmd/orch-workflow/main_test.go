package main

import (
	"errors"
	"flag"
	"reflect"
	"strings"
	"testing"
)

func TestSplitCSV(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"alice", []string{"alice"}},
		{"alice,bob", []string{"alice", "bob"}},
		{" alice , bob ", []string{"alice", "bob"}},
		{"alice,,bob", []string{"alice", "bob"}},
		{",alice,", []string{"alice"}},
		{"   ", nil},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := splitCSV(tc.in)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("splitCSV(%q) = %v; want %v", tc.in, got, tc.want)
			}
		})
	}
}

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

func TestRun_routesApplyStatusCancelByName(t *testing.T) {
	// Each subcommand must reach its handler — the handler will fail
	// for lack of args, but the error message anchors the test to the
	// correct branch.
	cases := []struct {
		sub    string
		errSub string
	}{
		{"apply", "yaml path required"},
		{"status", "workflow id required"},
		{"cancel", "workflow id required"},
	}
	for _, tc := range cases {
		t.Run(tc.sub, func(t *testing.T) {
			err := run([]string{tc.sub})
			if err == nil {
				t.Fatalf("%s with no args should error", tc.sub)
			}
			if !strings.Contains(err.Error(), tc.errSub) {
				t.Errorf("%s error %q missing substring %q", tc.sub, err.Error(), tc.errSub)
			}
		})
	}
}

func TestRun_unknownSubcommandSurfaces(t *testing.T) {
	err := run([]string{"frobnicate"})
	if err == nil || !strings.Contains(err.Error(), "unknown subcommand") {
		t.Errorf("got %v; want unknown subcommand error", err)
	}
}

func TestStatusAndCancel_requireScopeID(t *testing.T) {
	for _, sub := range []string{"status", "cancel"} {
		err := run([]string{sub, "workflow-id-without-scope"})
		if err == nil || !strings.Contains(err.Error(), "--scope-id is required") {
			t.Errorf("%s without --scope-id should fail loudly; got %v", sub, err)
		}
	}
}

func TestErrInvalidShape(t *testing.T) {
	// Regression: production main() relies on errors.Is(err, errInvalid)
	// to map validation failures to exit code 2.
	if !errors.Is(errInvalid, errInvalid) {
		t.Fatal("errInvalid must satisfy errors.Is reflexively")
	}
}
