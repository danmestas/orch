#!/usr/bin/env bash
# Regression tests for orch-tail.
# Run with: bash test/test-orch-tail.sh
#
# Strategy:
#   - Build a fixture Claude Code JSONL with known trouble + non-trouble lines.
#   - PATH-shadow `orch-registry` with a stub that returns a worker whose
#     .cwd points at a tmpdir we control.
#   - Point ORCH_PROJECTS_DIR at that tmpdir so the encoded-path lookup
#     resolves to our fixture without touching the real ~/.claude/projects.
#   - Exercise: --once / --patterns / --tool-results-only / default trouble
#     regex / unknown-target exit code / --help.
set -uo pipefail

PASS=0
FAIL=0
FAILED_TESTS=()

assert() {
    local desc=$1 expected=$2 got=$3
    if [ "$expected" = "$got" ]; then
        echo "  PASS  $desc"
        PASS=$((PASS + 1))
    else
        echo "  FAIL  $desc"
        echo "        expected: $expected"
        echo "        got:      $got"
        FAIL=$((FAIL + 1))
        FAILED_TESTS+=("$desc")
    fi
}

assert_contains() {
    local desc=$1 substr=$2 haystack=$3
    if [[ "$haystack" == *"$substr"* ]]; then
        echo "  PASS  $desc"
        PASS=$((PASS + 1))
    else
        echo "  FAIL  $desc"
        echo "        expected substring: $substr"
        echo "        got:                $haystack"
        FAIL=$((FAIL + 1))
        FAILED_TESTS+=("$desc")
    fi
}

assert_not_contains() {
    local desc=$1 substr=$2 haystack=$3
    if [[ "$haystack" != *"$substr"* ]]; then
        echo "  PASS  $desc"
        PASS=$((PASS + 1))
    else
        echo "  FAIL  $desc"
        echo "        unexpected substring: $substr"
        echo "        got:                  $haystack"
        FAIL=$((FAIL + 1))
        FAILED_TESTS+=("$desc")
    fi
}

ROOT=$(cd "$(dirname "$0")/.." && pwd)
ORCH_TAIL="$ROOT/bin/orch-tail"

[ -x "$ORCH_TAIL" ] || { echo "orch-tail not executable at $ORCH_TAIL"; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "jq not on PATH"; exit 1; }

TMPDIR=$(mktemp -d -t orch-tail-test.XXXXXX)
trap 'rm -rf "$TMPDIR"' EXIT

# Fake worker cwd. encode_cwd resolves symlinks first via `pwd -P`, so on
# macOS /var/folders/... → /private/var/folders/.... Match that.
FAKE_CWD="$TMPDIR/proj"
mkdir -p "$FAKE_CWD"
RESOLVED_CWD=$(cd "$FAKE_CWD" && pwd -P)
ENCODED=$(printf '%s' "$RESOLVED_CWD" | sed 's|/|-|g; s|_|-|g')

# Mock projects dir + session dir.
PROJECTS_DIR="$TMPDIR/projects"
SESSION_DIR="$PROJECTS_DIR/$ENCODED"
mkdir -p "$SESSION_DIR"
JSONL="$SESSION_DIR/session-abc.jsonl"

# Fixture transcript. Each line is one CC event.
#   - assistant text mentioning a trouble token (FAIL)
#   - assistant text with no trouble token
#   - user tool_result with a trouble token (panic:)
#   - user tool_result with a trouble token (test failed)
#   - user tool_result with no trouble token
#   - user tool_result with a "cargo: error" (only matches a custom --patterns)
cat > "$JSONL" <<'EOF'
{"type":"assistant","timestamp":"2026-05-18T12:34:56.000Z","message":{"content":[{"type":"text","text":"running suite\nFAIL TestFoo (0.02s)\nmoving on"}]}}
{"type":"assistant","timestamp":"2026-05-18T12:35:01.000Z","message":{"content":[{"type":"text","text":"hmm, nothing to report"}]}}
{"type":"user","timestamp":"2026-05-18T12:35:10.000Z","message":{"content":[{"type":"tool_result","content":[{"type":"text","text":"goroutine 1 [running]:\npanic: nil deref\nbailing"}]}]}}
{"type":"user","timestamp":"2026-05-18T12:35:20.000Z","message":{"content":[{"type":"tool_result","content":[{"type":"text","text":"test failed: foo_test.go:42"}]}]}}
{"type":"user","timestamp":"2026-05-18T12:35:30.000Z","message":{"content":[{"type":"tool_result","content":[{"type":"text","text":"everything fine, 0 issues"}]}]}}
{"type":"user","timestamp":"2026-05-18T12:35:40.000Z","message":{"content":[{"type":"tool_result","content":[{"type":"text","text":"cargo: error[E0277]: trait bound not satisfied"}]}]}}
EOF

# Stub orch-registry. Returns a worker JSON whose .cwd is our fixture
# directory for known targets ("builder", "%42"), or exits 4 for unknown.
STUB_DIR="$TMPDIR/bin"
mkdir -p "$STUB_DIR"
cat > "$STUB_DIR/orch-registry" <<STUB
#!/usr/bin/env bash
sub=\${1:-}; shift || true
case "\$sub" in
    lookup)
        # Drop --nats=... flags; pick the first positional that isn't a flag.
        target=""
        for a in "\$@"; do
            case "\$a" in
                --nats=*|--alias-file=*|--operator-file=*|--hb-window=*) ;;
                --*) ;;
                *) [ -z "\$target" ] && target=\$a ;;
            esac
        done
        case "\$target" in
            builder|%42)
                printf '{"pane_id":"%%42","name":"builder","role":"worker","cwd":"%s"}\n' "$FAKE_CWD"
                exit 0 ;;
            *) exit 4 ;;
        esac ;;
    *) echo "stub: unknown sub \$sub" >&2; exit 1 ;;
esac
STUB
chmod +x "$STUB_DIR/orch-registry"

# Compose env for every orch-tail run.
RUN() {
    PATH="$STUB_DIR:$PATH" ORCH_PROJECTS_DIR="$PROJECTS_DIR" "$ORCH_TAIL" "$@"
}

#---- 1. --help prints usage cleanly and exits 0.
echo "## --help"
help_out=$(RUN --help 2>&1)
help_ec=$?
assert "--help exits 0" "0" "$help_ec"
assert_contains "--help mentions 'orch-tail'"  "orch-tail" "$help_out"
assert_contains "--help mentions 'Usage:'"     "Usage:"    "$help_out"
assert_contains "--help mentions '--once'"     "--once"    "$help_out"
assert_contains "--help mentions '--patterns'" "--patterns" "$help_out"

#---- 2. --once with built-in regex catches FAIL / panic / test failed.
echo
echo "## --once (default trouble regex)"
out=$(RUN builder --once 2>&1)
ec=$?
assert "--once exits 0" "0" "$ec"
assert_contains "matches assistant 'FAIL' line"             "FAIL TestFoo"          "$out"
assert_contains "matches tool_result 'panic:' line"         "panic: nil deref"      "$out"
assert_contains "matches tool_result 'test failed' line"    "test failed: foo_test" "$out"
assert_not_contains "does NOT match benign assistant text"  "hmm, nothing to report" "$out"
assert_not_contains "does NOT match benign tool_result"     "everything fine"        "$out"
# Built-in regex does NOT include "cargo:" — that's a custom override case.
assert_not_contains "default regex does NOT match 'cargo: error'" "cargo: error" "$out"

#---- 3. --patterns override picks up cargo / go-style errors.
echo
echo "## --patterns override"
out=$(RUN builder --once --patterns='cargo: error|go: error' 2>&1)
ec=$?
assert "--patterns exits 0" "0" "$ec"
assert_contains "custom regex catches 'cargo: error'" "cargo: error" "$out"
assert_not_contains "custom regex skips 'FAIL' (not in override)" "FAIL TestFoo" "$out"
assert_not_contains "custom regex skips 'panic:' (not in override)" "panic: nil deref" "$out"

#---- 4. --tool-results-only filters assistant text out.
echo
echo "## --tool-results-only"
out=$(RUN builder --once --tool-results-only 2>&1)
ec=$?
assert "--tool-results-only exits 0" "0" "$ec"
assert_contains "tool-results-only still catches panic"        "panic: nil deref"      "$out"
assert_contains "tool-results-only still catches test failed"  "test failed: foo_test" "$out"
assert_not_contains "tool-results-only drops assistant FAIL"   "FAIL TestFoo"          "$out"

#---- 5. Pane-id alias form (%42) resolves through the stub identically.
echo
echo "## %pane resolution"
out=$(RUN %42 --once 2>&1)
ec=$?
assert "%pane lookup exits 0" "0" "$ec"
assert_contains "%pane lookup yields same matches" "panic: nil deref" "$out"

#---- 6. Unknown target → exit 4 + diagnostic on stderr.
echo
echo "## unknown target"
err=$(RUN ghost --once 2>&1 1>/dev/null || true)
ec=$(RUN ghost --once >/dev/null 2>&1; echo $?)
assert "unknown target exits 4" "4" "$ec"
assert_contains "unknown target stderr mentions 'not in the orch registry'" "not in the orch registry" "$err"

#---- 7. Output format: '[HH:MM:SS] <kind>: <excerpt>'
echo
echo "## output format"
out=$(RUN builder --once 2>&1)
# At least one line should match [12:34:56] assistant: ... (we know FAIL is at 12:34:56)
fmt_ok=$(printf '%s\n' "$out" | grep -cE '^\[[0-9]{2}:[0-9]{2}:[0-9]{2}\] (assistant|tool_result): ' || true)
if [ "$fmt_ok" -ge 3 ]; then
    echo "  PASS  output rows match '[HH:MM:SS] <kind>: …' format (count=$fmt_ok)"
    PASS=$((PASS + 1))
else
    echo "  FAIL  expected >=3 format-matching rows, got $fmt_ok"
    printf '        full output was:\n%s\n' "$out"
    FAIL=$((FAIL + 1))
    FAILED_TESTS+=("output format")
fi

echo
echo "================================"
echo "PASS: $PASS / $((PASS + FAIL))"
if [ $FAIL -gt 0 ]; then
    echo "FAIL: $FAIL"
    printf '       %s\n' "${FAILED_TESTS[@]}"
    exit 1
fi
echo "all green"
