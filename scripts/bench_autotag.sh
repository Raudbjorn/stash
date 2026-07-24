#!/usr/bin/env bash
#
# Runs cmd/autotag-bench against two git refs: HEAD (new/perf) and HEAD~1
# (baseline). Expects both refs to include cmd/autotag-bench/ and the
# manager.RunAutoTagFilesForBench wrapper — i.e. the bench tool must be
# committed ahead of the perf changes so it exists on both refs.
#
# Output:
#   bench-results/<preset>/baseline.md
#   bench-results/<preset>/new.md
#   bench-results/summary.md (side-by-side comparison)
#
# Usage:
#   ./scripts/bench_autotag.sh [preset1 preset2 ...]
#   default: tiny small medium large
#
# Each preset is run with --seed=42 so baseline and new see identical data.

set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

PRESETS=("${@:-tiny small medium large}")
# shellcheck disable=SC2206  # intentional word-splitting of $1 when passed
PRESETS=(${PRESETS[@]})

OUT_DIR="$REPO_ROOT/bench-results"
BASELINE_WT="$(mktemp -d -t stash-autotag-baseline-XXXX)"
trap 'rm -rf "$BASELINE_WT"; git worktree prune >/dev/null 2>&1 || true' EXIT

HEAD_SHA=$(git rev-parse --short HEAD)
BASE_SHA=$(git rev-parse --short HEAD~1)

echo "==> HEAD (new):      $HEAD_SHA"
echo "==> HEAD~1 (baseline): $BASE_SHA"
echo "==> Presets:          ${PRESETS[*]}"

mkdir -p "$OUT_DIR"

# Build new binary (current working dir)
echo
echo "==> Building new binary at HEAD..."
go build -o "$OUT_DIR/autotag-bench-new" ./cmd/autotag-bench

# Create worktree at HEAD~1 and build baseline binary there. The manager
# package transitively embeds ui/v2.5/build via //go:embed, so copy the
# current tree's UI build into the worktree to satisfy the embed pattern
# without having to rebuild the UI.
echo
echo "==> Creating worktree at HEAD~1..."
git worktree add --detach "$BASELINE_WT" HEAD~1 >/dev/null
if [[ -d "$REPO_ROOT/ui/v2.5/build" ]]; then
	mkdir -p "$BASELINE_WT/ui/v2.5"
	cp -R "$REPO_ROOT/ui/v2.5/build" "$BASELINE_WT/ui/v2.5/"
else
	echo "warning: $REPO_ROOT/ui/v2.5/build is missing; baseline build will fail."
	echo "         run 'make generate-ui' first, then retry."
	exit 1
fi
echo "==> Building baseline binary..."
(cd "$BASELINE_WT" && go build -o "$OUT_DIR/autotag-bench-baseline" ./cmd/autotag-bench)

run_preset() {
	local preset="$1"
	local preset_dir="$OUT_DIR/$preset"
	mkdir -p "$preset_dir"

	echo
	echo "==> $preset: baseline run"
	"$OUT_DIR/autotag-bench-baseline" \
		--preset="$preset" --seed=42 --label=baseline \
		--out="$preset_dir/baseline.md"

	echo "==> $preset: new run"
	"$OUT_DIR/autotag-bench-new" \
		--preset="$preset" --seed=42 --label=new \
		--out="$preset_dir/new.md"
}

for preset in "${PRESETS[@]}"; do
	run_preset "$preset"
done

# Aggregate into summary.md
SUMMARY="$OUT_DIR/summary.md"
{
	echo "# Auto-tag benchmark: baseline ($BASE_SHA) vs new ($HEAD_SHA)"
	echo
	echo "Seed: 42 (identical synthetic data for both runs per preset)."
	echo "Distribution: 60% scenes, 30% images, 10% galleries. Match mix: ~30% multi-match, ~50% single-match, ~20% no-match."
	echo
	echo "Columns: run time, heap in use post-run, peak RSS (process lifetime), total alloc, GC cycles."
	echo
	for preset in "${PRESETS[@]}"; do
		# shellcheck disable=SC1090
		. /dev/null  # no-op to satisfy linters; preset-specific lookup below
		echo "## $preset"
		echo
		# Reconstruct counts by re-reading preset definitions; hardcoded here to keep the script self-contained.
		case "$preset" in
			tiny)   echo "100 performers · 20 studios · 50 tags · 1k files" ;;
			small)  echo "1k performers · 200 studios · 300 tags · 10k files" ;;
			medium) echo "10k performers · 1.3k studios · 1k tags · 50k files" ;;
			large)  echo "100k performers · 13k studios · 3k tags · 100k files" ;;
		esac
		echo
		echo "| run | time | heap in use | peak rss | total alloc | gc |"
		echo "|-----|------|-------------|----------|-------------|----|"
		cat "$OUT_DIR/$preset/baseline.md"
		cat "$OUT_DIR/$preset/new.md"
		echo
	done
} > "$SUMMARY"

echo
echo "==> Summary written to $SUMMARY"
