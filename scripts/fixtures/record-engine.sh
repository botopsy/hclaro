#!/bin/sh
# Records real engine output for the parser goldens (M26.16) from fixtures/engines/project.
# Run: scripts/fixtures/record-engine.sh /path/to/terraform-or-tofu fixtures/engines/<engine>-<version>
# Offline: built-in resources only, default local backend, a hand-written synthetic state, no
# provider downloads. Never applies. Uses the argv Hclaro uses (internal/engine/planner.go, graph.go).
set -eu
bin=$(cd "$(dirname "$1")" && pwd)/$(basename "$1")
out=$2
src=$(cd "$(dirname "$0")/../../fixtures/engines/project" && pwd)
mkdir -p "$out"
out=$(cd "$out" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cp -R "$src/." "$work/"
mv "$work/state.json" "$work/terraform.tfstate"
cd "$work"
run() { env -i TZ=UTC PATH=/usr/bin:/bin HOME="$work" CHECKPOINT_DISABLE=1 "$bin" "$@"; }
run init -input=false -no-color >/dev/null
run version -json >"$out/version.json"
# plan exits 0; a failure here means the recording is wrong, so set -e stops.
run plan -input=false -json -out="$work/plan.out" -var-file=prod.tfvars >"$out/plan.jsonl"
run show -json "$work/plan.out" >"$out/plan.json"
run plan -input=false -json -out="$work/drift.out" -refresh-only -var-file=prod.tfvars >"$out/drift.jsonl"
run show -json "$work/drift.out" >"$out/drift.json"
run show -json >"$out/state.json"
run graph >"$out/graph.dot"
# Recordings must not carry machine paths.
if grep -l "$work" "$out"/*; then echo "machine path in recording" >&2; exit 1; fi
