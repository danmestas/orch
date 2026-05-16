#!/usr/bin/env bash
# compare.sh — diff two benchmark runs, flag regressions ≥ 20% in any percentile.
#
# Usage:
#   test/bench/compare.sh <baseline.md> <current.md>
#
# Exit code:
#   0 — no regressions (or all regressions < 20%)
#   1 — one or more regressions ≥ 20%
#   2 — usage error / file not found
#
# Output: a diff table to stdout. Regressions are flagged with "⚠ REGRESSED".
set -euo pipefail

THRESHOLD="${REGRESSION_THRESHOLD:-20}"  # percent

usage() {
    sed -n '1,/^set -e/p' "$0" | sed '$d' | sed 's|^# *||'
    exit 2
}

[ "${1:-}" ] && [ "${2:-}" ] || usage
BASELINE=$1
CURRENT=$2

[ -f "$BASELINE" ] || { echo "compare.sh: baseline not found: $BASELINE" >&2; exit 2; }
[ -f "$CURRENT"  ] || { echo "compare.sh: current not found: $CURRENT" >&2; exit 2; }

# ---------------------------------------------------------------------------
# Extract p50/p95/p99 values from a results Markdown file.
# Table format (from measure.sh):
#   | Path | p50 | p95 | p99 |
#   | orch-tell (legacy) | *X ms* | *Y ms* | *Z ms* |
#   | agents.prompt (shim) | *X ms* | *Y ms* | *Z ms* |
# ---------------------------------------------------------------------------
extract_latency() {
    local file=$1 path=$2 pct=$3
    # pct: 1=p50, 2=p95, 3=p99 (1-indexed field after Path column)
    grep "$path" "$file" \
        | awk -F'|' -v col=$((pct + 2)) '{gsub(/[^0-9.]/,"",$col); print $col}' \
        | head -1
}

compare_pct() {
    local label=$1 base=$2 curr=$3
    if [ -z "$base" ] || [ -z "$curr" ]; then
        printf "| %-40s | %8s | %8s | %-18s |\n" "$label" "n/a" "n/a" "MISSING DATA"
        return
    fi
    # delta% = (curr - base) / base * 100
    result=$(awk -v b="$base" -v c="$curr" -v thr="$THRESHOLD" 'BEGIN {
        if (b == 0) { print "n/a SKIP"; exit }
        delta = (c - b) / b * 100
        sign = (delta >= 0) ? "+" : ""
        flag = (delta >= thr) ? "REGRESSED" : "ok"
        printf "%.1f%s%% %s\n", delta, sign, flag
    }')
    delta=$(echo "$result" | awk '{print $1}')
    flag=$(echo "$result" | awk '{print $2}')
    printf "| %-40s | %8s | %8s | %-18s |\n" "$label" "${base} ms" "${curr} ms" "$delta  $flag"
    # Return non-zero if regressed so caller can count
    [ "$flag" != "REGRESSED" ]
}

REGRESSIONS=0

header() {
    printf "\n## %s\n\n" "$1"
    printf "| %-40s | %8s | %8s | %-18s |\n" "Metric" "baseline" "current" "delta / verdict"
    printf "|%s|%s|%s|%s|\n" "$(printf '%0.s-' {1..42})" "$(printf '%0.s-' {1..10})" "$(printf '%0.s-' {1..10})" "$(printf '%0.s-' {1..20})"
}

printf "# Benchmark comparison\n\n"
printf "**Baseline:** %s\n" "$(basename "$BASELINE")"
printf "**Current:**  %s\n" "$(basename "$CURRENT")"
printf "**Regression threshold:** %s%%\n" "$THRESHOLD"

header "Round-trip latency — orch-tell (legacy)"
for col in 1 2 3; do
    pct_label="p50"; [ $col -eq 2 ] && pct_label="p95"; [ $col -eq 3 ] && pct_label="p99"
    b=$(extract_latency "$BASELINE" "orch-tell" $col)
    c=$(extract_latency "$CURRENT"  "orch-tell" $col)
    compare_pct "  $pct_label" "$b" "$c" || REGRESSIONS=$((REGRESSIONS+1))
done

header "Round-trip latency — agents.prompt (shim)"
for col in 1 2 3; do
    pct_label="p50"; [ $col -eq 2 ] && pct_label="p95"; [ $col -eq 3 ] && pct_label="p99"
    b=$(extract_latency "$BASELINE" "agents.prompt" $col)
    c=$(extract_latency "$CURRENT"  "agents.prompt" $col)
    compare_pct "  $pct_label" "$b" "$c" || REGRESSIONS=$((REGRESSIONS+1))
done

echo

if [ "$REGRESSIONS" -gt 0 ]; then
    printf "\n⚠  %d regression(s) ≥ %s%% detected. Review before merging.\n" "$REGRESSIONS" "$THRESHOLD"
    exit 1
else
    printf "\n✓  No regressions ≥ %s%% detected.\n" "$THRESHOLD"
    exit 0
fi
