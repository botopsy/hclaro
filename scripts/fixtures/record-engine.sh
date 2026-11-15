#!/bin/sh
# Records real engine output for the parser goldens (M26.16, M27.04).
# Run: scripts/fixtures/record-engine.sh /path/to/terraform-or-tofu fixtures/engines/<engine>-<version> [mirror.tfrc]
# fixtures/engines/project: built-in resources only, default local backend, a hand-written
# synthetic state, no provider downloads. Never applies. Uses the argv Hclaro uses
# (internal/engine/planner.go, graph.go).
# With mirror.tfrc (the M27.01 offline mirror config for the engine's family, mirror path
# filled in), fixtures/engines/local-project is recorded too: real local_file drift and the
# plan_failed / init_required diagnostics. M27.04 ran this under scripts/qualify/sandbox.sb.
set -eu
unset TF_CLI_CONFIG_FILE
bin=$(cd "$(dirname "$1")" && pwd)/$(basename "$1")
out=$2
fixtures=$(cd "$(dirname "$0")/../../fixtures/engines" && pwd)
mkdir -p "$out"
out=$(cd "$out" && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/record.XXXXXX")
trap 'rm -rf "$work"' EXIT
cp -R "$fixtures/project/." "$work/"
mv "$work/state.json" "$work/terraform.tfstate"
cd "$work"
fails() { if "$@"; then echo "expected a failure: $*" >&2; exit 1; fi; }
run() { env -i TZ=UTC PATH=/usr/bin:/bin HOME="$work" TMPDIR="$work" CHECKPOINT_DISABLE=1 ${TF_CLI_CONFIG_FILE:+TF_CLI_CONFIG_FILE="$TF_CLI_CONFIG_FILE"} "$bin" "$@"; }
run init -input=false -no-color >/dev/null
run version -json >"$out/version.json"
# plan exits 0; a failure here means the recording is wrong, so set -e stops.
run plan -input=false -json -out="$work/plan.out" -var-file=prod.tfvars >"$out/plan.jsonl"
run show -json "$work/plan.out" >"$out/plan.json"
run plan -input=false -json -out="$work/drift.out" -refresh-only -var-file=prod.tfvars >"$out/drift.jsonl"
run show -json "$work/drift.out" >"$out/drift.json"
run show -json >"$out/state.json"
run graph >"$out/graph.dot"

if [ $# -ge 3 ]; then
  TF_CLI_CONFIG_FILE=$(cd "$(dirname "$3")" && pwd)/$(basename "$3")
  lp=$work/local
  mkdir -p "$lp"
  cp -R "$fixtures/local-project/." "$lp/"
  mv "$lp/state.json" "$lp/terraform.tfstate"
  cd "$lp"
  # Before init: providers are not installed (init_required). Exits 1.
  fails run plan -input=false -json -lock=false >"$out/local-noinit.jsonl"
  run init -input=false -no-color >/dev/null
  # Outside the engine: out/gone.txt is never created, out/edited.txt differs from state.
  mkdir -p out
  printf 'same\n' >out/same.txt
  printf 'edited outside\n' >out/edited.txt
  run plan -input=false -json -out="$lp/drift.out" -refresh-only >"$out/local-drift.jsonl"
  run show -json "$lp/drift.out" >"$out/local-drift.json"
  # file() on a missing path from a var file (plan_failed). Exits 1.
  fails run plan -input=false -json -lock=false -var-file=missing.tfvars >"$out/local-missing-file.jsonl"
  # A lock file that no longer matches the configuration (init_required). Exits 1.
  sed '/^ *version *=/s/"2.9.1"/"2.9.0"/' .terraform.lock.hcl >lock.tmp && mv lock.tmp .terraform.lock.hcl
  grep -q '"2.9.0"' .terraform.lock.hcl
  fails run plan -input=false -json -lock=false >"$out/local-lockfile.jsonl"
fi
# Recordings must not carry machine paths.
if grep -l "$work" "$out"/*; then echo "machine path in recording" >&2; exit 1; fi
