#!/usr/bin/env bash
# test-tier.sh drives the command orchestration test tiers introduced by the
# injected workspace seam. The seam lets the highest-volume orchestration tests
# run without spawning Git, so the fast tier is deterministic and Git-free while
# the real-Git contract coverage still runs in the git-contract and full tiers.
#
# Usage: scripts/test-tier.sh <tier>
#
#   fast          Git-free fast tier: go test -short over the command +
#                 process-workspace packages, then assert the reported wall time
#                 against testdata/test-baseline.json.
#   git-contract  Bounded real-Git contract tier: full (non-short) run of the
#                 command + process-workspace packages, exercising the real
#                 worktree/recovery/integration coverage and the designated
#                 command-to-real-workspace smoke path.
#   full          Whole-module run (go test ./...).
#   baseline      Record a fresh same-host cold-cache baseline_ms into
#                 testdata/test-baseline.json for the command package.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

GO="${GO:-go}"
BASELINE_FILE="testdata/test-baseline.json"
TIER_PACKAGES=("./internal/commands/..." "./internal/processworkspace/...")
COMMAND_PACKAGE="./internal/commands/"

# summary_duration_ms runs the given `go test` invocation for a single package
# (with -v so the reporter's TEST-TIER-SUMMARY line reaches stdout), streams the
# output, and echoes the duration_ms parsed from the summary line.
summary_duration_ms() {
	local pkg="$1"
	shift
	local out
	out="$("$GO" test -v "$@" "$pkg" 2>&1)"
	printf '%s\n' "$out" >&2
	local line
	line="$(printf '%s\n' "$out" | grep -E '^TEST-TIER-SUMMARY ' | tail -n1)"
	if [[ -z "$line" ]]; then
		echo "test-tier: no TEST-TIER-SUMMARY line emitted for $pkg" >&2
		return 1
	fi
	printf '%s\n' "$line" | sed -n 's/.*duration_ms=\([0-9][0-9]*\).*/\1/p'
}

json_number() {
	# json_number <file> <key> -- extract an integer/float value for a top-level key.
	sed -n "s/.*\"$2\"[[:space:]]*:[[:space:]]*\([0-9.][0-9.]*\).*/\1/p" "$1" | head -n1
}

tier="${1:-}"
case "$tier" in
fast)
	echo "== fast tier (go test -short; Git-free) =="
	# Verify the whole fast tier is green first.
	"$GO" test -short "${TIER_PACKAGES[@]}"
	# Then measure the command package's reported wall time for the budget check.
	fast_ms="$(summary_duration_ms "$COMMAND_PACKAGE" -short)"
	baseline_ms="$(json_number "$BASELINE_FILE" baseline_ms)"
	max_ratio="$(json_number "$BASELINE_FILE" max_ratio)"
	max_ms="$(json_number "$BASELINE_FILE" max_ms)"
	: "${max_ratio:=0.25}"
	: "${max_ms:=30000}"
	# threshold = max(max_ratio * baseline_ms, max_ms)
	threshold="$(awk -v b="$baseline_ms" -v r="$max_ratio" -v m="$max_ms" 'BEGIN{q=b*r; print (q>m)?q:m}')"
	ratio_pct="$(awk -v f="$fast_ms" -v b="$baseline_ms" 'BEGIN{ if(b>0) printf "%.1f", (f*100.0)/b; else print "n/a"}')"
	echo "-- fast=${fast_ms}ms baseline=${baseline_ms}ms ratio=${ratio_pct}% threshold=${threshold%.*}ms --"
	if awk -v f="$fast_ms" -v t="$threshold" 'BEGIN{exit !(f<=t)}'; then
		echo "test-tier: fast tier within budget (${fast_ms}ms <= ${threshold%.*}ms)"
	else
		echo "test-tier: fast tier exceeded budget (${fast_ms}ms > ${threshold%.*}ms)" >&2
		exit 1
	fi
	;;
git-contract)
	echo "== git-contract tier (real-Git contract + smoke path) =="
	"$GO" test "${TIER_PACKAGES[@]}"
	;;
full)
	echo "== full tier (whole module) =="
	"$GO" test ./...
	;;
baseline)
	echo "== recording cold-cache baseline for $COMMAND_PACKAGE =="
	"$GO" clean -testcache
	baseline_ms="$(summary_duration_ms "$COMMAND_PACKAGE")"
	echo "test-tier: measured baseline_ms=${baseline_ms} (update $BASELINE_FILE manually if adopting)"
	;;
*)
	echo "usage: scripts/test-tier.sh <fast|git-contract|full|baseline>" >&2
	exit 2
	;;
esac
