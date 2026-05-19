// Package natsurl resolves the NATS client URL via orch's documented
// precedence chain.
//
// Background — sesh writes three URL files (see sesh's cli/hubinfo.go):
//
//	~/.sesh/hub.url       — leaf-node URL, owned by HubGuard O_EXCL lease;
//	                        NOT intended for NATS clients
//	~/.sesh/hub.nats.url  — NATS client URL  ← what NATS clients should read
//	~/.sesh/hub.fossil.url — Fossil HTTP endpoint
//
// Historically orch consumers read hub.url and failed to connect with
// "nats: attempted to connect to leaf node port." The correct file for
// NATS clients is hub.nats.url. This helper reads hub.nats.url primarily
// and falls back to hub.url with a deprecation warning, so operators on
// older sesh versions still work while a stderr warning surfaces the
// upgrade path.
//
// Resolution order (first non-empty wins):
//
//  1. override (e.g. CLI --nats flag)
//  2. $NATS_URL environment variable
//  3. ~/.sesh/hub.nats.url
//  4. ~/.sesh/hub.url  (deprecated — emits a stderr warning)
//  5. nats://127.0.0.1:4222
package natsurl

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// DefaultURL is the loopback fallback used when no other source produces a URL.
const DefaultURL = "nats://127.0.0.1:4222"

// Resolve returns the NATS client URL using the precedence chain documented
// at the package level. If a legacy ~/.sesh/hub.url is read (because the
// newer hub.nats.url is absent), Resolve writes a one-line deprecation
// warning to os.Stderr tagged with binName.
//
// binName is the program name used in the deprecation warning; pass the
// running binary's base name (e.g. "orch-goal-stop-account-daemon"). An
// empty binName falls back to "orch".
func Resolve(binName, override string) string {
	return resolve(binName, override, os.Getenv, os.UserHomeDir, os.ReadFile, os.Stderr)
}

// resolve is the testable seam: callers inject env / home / file / stderr.
func resolve(
	binName, override string,
	getenv func(string) string,
	userHomeDir func() (string, error),
	readFile func(string) ([]byte, error),
	stderr io.Writer,
) string {
	if u := strings.TrimSpace(override); u != "" {
		return u
	}
	if u := strings.TrimSpace(getenv("NATS_URL")); u != "" {
		return u
	}
	home, err := userHomeDir()
	if err == nil {
		// Primary: NATS client URL written by sesh's hubinfo.go.
		if raw, err := readFile(filepath.Join(home, ".sesh", "hub.nats.url")); err == nil {
			if u := strings.TrimSpace(string(raw)); u != "" {
				return u
			}
		}
		// Fallback: legacy file (leaf URL, may still work for some
		// older sesh deployments that did not yet split the files).
		// Emit a deprecation warning so operators upgrade sesh.
		if raw, err := readFile(filepath.Join(home, ".sesh", "hub.url")); err == nil {
			if u := strings.TrimSpace(string(raw)); u != "" {
				name := binName
				if name == "" {
					name = "orch"
				}
				fmt.Fprintf(
					stderr,
					"%s: reading legacy ~/.sesh/hub.url (deprecated — sesh now writes hub.nats.url); upgrade sesh to remove this warning\n",
					name,
				)
				return u
			}
		}
	}
	return DefaultURL
}
