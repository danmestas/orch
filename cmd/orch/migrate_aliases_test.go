package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanFileForAliases_GoldenInputs(t *testing.T) {
	tmp := t.TempDir()
	cases := []struct {
		name        string
		body        string
		wantMatches int
		wantNames   []string
	}{
		{
			name: "zshrc with orch-tell + orch-peek aliases",
			body: `# my dotfiles
alias t='orch-tell'
alias p='orch-peek --json'
function ask() {
  orch-ask "$1" "$2"
}
`,
			wantMatches: 3,
			wantNames:   []string{"orch-tell", "orch-peek", "orch-ask"},
		},
		{
			name: "no orch references at all",
			body: `
alias ll='ls -la'
export EDITOR=vim
`,
			wantMatches: 0,
			wantNames:   nil,
		},
		{
			name: "retired stubs surface for deletion",
			body: `
orch-claim-operator
orch-register %123 claude /tmp
`,
			wantMatches: 2,
			wantNames:   []string{"orch-claim-operator", "orch-register"},
		},
		{
			name: "substring matches must not trigger",
			body: `
# this comment mentions my-orch-teller binary but should not match
notorch-tell-other
`,
			wantMatches: 0,
		},
		{
			name: "orch-spy with mixed flags counts once",
			body: `orch-spy operator "watch the deploy"`,
			wantMatches: 1,
			wantNames:   []string{"orch-spy"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(tmp, strings.ReplaceAll(c.name, " ", "_"))
			if err := os.WriteFile(path, []byte(c.body), 0o644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			hits, err := scanFileForAliases(path)
			if err != nil {
				t.Fatalf("scan: %v", err)
			}
			if len(hits) != c.wantMatches {
				t.Fatalf("len(hits) = %d, want %d, hits=%+v", len(hits), c.wantMatches, hits)
			}
			for _, want := range c.wantNames {
				found := false
				for _, h := range hits {
					if h.mapping.from == want {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected hit for %q, got %+v", want, hits)
				}
			}
		})
	}
}

func TestDefaultMigrateAliasesFiles_Coverage(t *testing.T) {
	// Required by spec: bash, zsh, fish, and the orch-aliases file all
	// covered out of the box. The function reads $HOME — set a controlled
	// one so the test does not depend on the host filesystem layout.
	t.Setenv("HOME", "/home/test")
	got := defaultMigrateAliasesFiles()
	want := []string{
		"/home/test/.bashrc",
		"/home/test/.bash_profile",
		"/home/test/.bash_aliases",
		"/home/test/.zshrc",
		"/home/test/.zprofile",
		"/home/test/.profile",
		"/home/test/.config/fish/config.fish",
		"/home/test/.config/orch-aliases",
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (got=%v)", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("[%d] %s want %s", i, got[i], w)
		}
	}
}

func TestAliasMappings_OneToOneTable(t *testing.T) {
	// All 6 retired CLIs must be in the mapping table; we depend on this
	// for the README + skill-doc migration coverage list.
	mustExist := []string{
		"orch-tell", "orch-ask", "orch-peek", "orch-spy",
		"orch-claim-operator", "orch-register",
	}
	for _, m := range mustExist {
		if _, ok := mappingByName(m); !ok {
			t.Errorf("missing alias mapping for %q", m)
		}
	}
	// Replacement strings: the 4 active CLIs go to subcommand form;
	// the 2 retired-without-replacement CLIs have an empty replace +
	// non-empty note.
	for _, m := range aliasMappings {
		switch m.from {
		case "orch-tell", "orch-ask", "orch-peek", "orch-spy":
			if m.replace == "" {
				t.Errorf("%s should have a replace value, got empty", m.from)
			}
		case "orch-claim-operator", "orch-register":
			if m.replace != "" {
				t.Errorf("%s should have empty replace (delete-the-line); got %q", m.from, m.replace)
			}
			if m.note == "" {
				t.Errorf("%s should have a note explaining the removal", m.from)
			}
		}
	}
}
