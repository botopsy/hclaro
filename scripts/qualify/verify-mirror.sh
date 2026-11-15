#!/usr/bin/env bash
# verify-mirror.sh <mirror-dir> <engines-dir> <evidence-dir>
# Re-hashes the mirror against mirror-manifest.json, then, under enforced isolation (macOS
# sandbox-exec, scripts/qualify/sandbox.sb), runs init for every approved engine against
# fixtures/qualify/providers and asserts the lock file and provider directory come from the mirror.
# `providers schema -json` then starts each provider plugin (read-only) to show it runs with that engine.
# F-0 is checked with netprobe before the first init and after the last; the run fails unless both
# brackets are `enforced`. COMBINED=1 runs every engine with the combined Hub config
# (scripts/qualify/hub.tfrc, both mirrors) instead of its family's config (M27.05). Nothing is applied, planned or refreshed; there is no state.
set -euo pipefail
. "$(dirname "$0")/lib.sh"

[ $# -eq 3 ] || die "usage: verify-mirror.sh <mirror-dir> <engines-dir> <evidence-dir>"
[ "$(uname -s)" = Darwin ] || die "only the macOS sandbox-exec isolation is implemented; other platforms are NOT_TESTED (OD-7)"
command -v jq >/dev/null || die "jq is required"
mirror=$(cd "$1" && pwd -P) engines=$(cd "$2" && pwd -P)
mkdir -p "$3"; ev=$(cd "$3" && pwd -P)
repo=$(cd "$QUALIFY_DIR/../.." && pwd -P)
manifest=$mirror/mirror-manifest.json
real_home=$(cd "$HOME" && pwd -P)
host_platform=$(uname -s | tr A-Z a-z)_$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
engine_list="terraform-1.5.5 terraform-1.5.7 terraform-1.17.2 opentofu-1.6.3 opentofu-1.13.9" # OD-1b

# The run dir is outside the real home, which the profile makes unreadable.
run=$(cd "$(mktemp -d /tmp/qualify-verify.XXXXXX)" && pwd -P)
trap 'rm -rf "$run"' EXIT
case $run/ in "$real_home"/*) die "run dir $run is under the real home";; esac
mkdir -p "$run/tmp" "$run/home"
canary=$real_home/hclaro-qualify/home-read-canary
[ -f "$canary" ] || echo "M27 OD-14 canary: reading this inside the sandbox means the home deny failed" >"$canary"

sandboxed() { # <cwd> <cmd...>: same mechanism and environment for netprobe and the engines.
  local cwd=$1; shift
  (cd "$cwd" && sandbox-exec -D REAL_HOME="$real_home" -D RUN_TMP="$run/tmp" -f "$run/sandbox.sb" \
    env -i PATH=/usr/bin:/bin HOME="$run/home" TMPDIR="$run/tmp" CHECKPOINT_DISABLE=1 \
    TF_CLI_CONFIG_FILE="${CLI:-/dev/null}" TF_IN_AUTOMATION=1 LANG=C.UTF-8 LC_ALL=C.UTF-8 "$@")
}

cp "$QUALIFY_DIR/sandbox.sb" "$run/sandbox.sb"
(cd "$repo" && go build -o "$run/netprobe" ./internal/qualify/netprobe)

# F-0 bracket: control must reach the OD-5 endpoints; isolated must deny them (child and grandchild),
# keep loopback (5 s retry, F0-LB), fail system DNS and fail the home read.
f0() {
  local tag=$1
  "$run/netprobe" control >"$ev/f0-$tag-control.json"
  sandboxed "$run" "$run/netprobe" isolated -read "$canary" >"$ev/f0-$tag-isolated.json"
  jq -e '[.checks[] | select(.name=="ipv4_tcp" and .ok)] | length > 0' "$ev/f0-$tag-control.json" >/dev/null &&
    jq -e '[.checks[] | select(.name=="udp_dns" and .ok)] | length > 0' "$ev/f0-$tag-control.json" >/dev/null ||
    die "F-0 $tag: control did not reach the OD-5 endpoints, so denial proves nothing (not enforced)"
  jq -e '([.checks[], .grandchild.checks[] | select(has("expected")) | .ok] | all)
         and ([.checks[] | select(.name=="system_resolver" or .name=="home_read") | .outcome == "failed"] | all)
         and ([.grandchild.checks[]] | length > 0)' "$ev/f0-$tag-isolated.json" >/dev/null ||
    die "F-0 $tag: isolation not enforced (see $ev/f0-$tag-isolated.json)"
  echo "F-0 $tag: enforced (loopback attempts: $(jq -r '[.checks[]|select(.name=="loopback_tcp4")|.attempt][0]' "$ev/f0-$tag-isolated.json"))"
}

# Copy the mirror into the run dir and re-hash the copy against the manifest.
for family in terraform tofu; do
  cp -R "$mirror/$family" "$run/mirror-$family"
  sed "s|@MIRROR@/$family|$run/mirror-$family|" "$mirror/$family.tfrc" >"$run/$family.tfrc"
done
sed "s|@MIRROR@/terraform|$run/mirror-terraform|;s|@MIRROR@/tofu|$run/mirror-tofu|" "$QUALIFY_DIR/hub.tfrc" >"$run/hub.tfrc"
[ "${COMBINED:-}" != 1 ] || echo "combined config: hub.tfrc sha256 $(sha256 "$QUALIFY_DIR/hub.tfrc")"
jq -r '.entries[] | "\(.family) \(.archive) \(.sha256)"' "$manifest" | while read -r family archive want; do
  got=$(sha256 "$run/mirror-$family/$archive")
  [ "$got" = "$want" ] || die "$family/$archive: SHA-256 $got, manifest $want"
done
echo "mirror: every archive matches mirror-manifest.json (sha256 $(sha256 "$manifest"))"
for pl in $(jq -r '.platforms[]' "$manifest"); do
  [ "$pl" = "$host_platform" ] || echo "$pl: mirrored, not init-verified on this host (M27.07)"
done

f0 before
results=$ev/verify-results.jsonl
: >"$results"
for e in $engine_list; do
  name=${e%-*} v=${e##*-}
  case $name in terraform) family=terraform bin=terraform ;; opentofu) family=tofu bin=tofu ;; esac
  cli=$run/$family.tfrc
  [ "${COMBINED:-}" != 1 ] || cli=$run/hub.tfrc
  src=$engines/$e-$host_platform/$bin
  [ -x "$src" ] || die "engine missing: $src"
  cp "$src" "$run/$e"
  proj=$run/proj-$e
  mkdir -p "$proj" && cp "$repo/fixtures/qualify/providers/versions.tf" "$proj/"
  CLI=$cli sandboxed "$proj" "$run/$e" init -input=false -no-color >"$ev/init-$e.log" 2>&1 ||
    die "$e: init failed under isolation (see $ev/init-$e.log)"
  cp "$proj/.terraform.lock.hcl" "$ev/lock-$e.hcl"

  # Lock file: exactly the pinned providers and versions; every hash is a manifest hash for this family.
  locked=$(awk -F'"' '/^provider /{p=$2} /^  version /{print p, $2}' "$proj/.terraform.lock.hcl" | sort)
  want=$(jq -r --arg f "$family" --arg pl "$host_platform" \
    '.entries[] | select(.family==$f and .platform==$pl) | "\(.host)/\(.provider) \(.version)"' "$manifest" | sort)
  [ "$locked" = "$want" ] || die "$e: lock file providers [$locked] are not the pinned [$want]"
  lock_hashes=$(awk -F'"' '/^    "/{print $2}' "$proj/.terraform.lock.hcl" | sort)
  want_hashes=$(jq -r --arg f "$family" --arg pl "$host_platform" \
    '.entries[] | select(.family==$f and .platform==$pl) | .hashes[]' "$manifest" | sort)
  # A mirror install locks only h1: hashes; OpenTofu's mirror metadata also lists zh: hashes.
  [ -n "$lock_hashes" ] && [ -z "$(comm -23 <(echo "$lock_hashes") <(echo "$want_hashes"))" ] &&
    [ -z "$(comm -13 <(echo "$lock_hashes") <(echo "$want_hashes" | grep '^h1:'))" ] ||
    die "$e: lock file hashes [$lock_hashes] do not match the manifest hashes [$want_hashes]"
  # Providers installed under the host directory the mirror uses (direct install is excluded by the CLI config).
  while read -r hp ver; do
    [ -d "$proj/.terraform/providers/$hp/$ver/$host_platform" ] || die "$e: $hp $ver not installed from the mirror"
  done <<<"$want"

  # Start each provider plugin (schema only; no plan, no state).
  CLI=$cli sandboxed "$proj" "$run/$e" providers schema -json >"$run/schema-$e.json" 2>"$ev/schema-$e.err" ||
    die "$e: providers failed to start under isolation (see $ev/schema-$e.err)"
  resources=$(jq -c '[.provider_schemas[] | .resource_schemas | keys[]] | sort' "$run/schema-$e.json")
  jq -nc --arg engine "$e" --arg cli "$(basename "$cli")" --arg platform "$host_platform" --arg binary "$(sha256 "$run/$e")" \
    --arg providers "$want" --argjson resources "$resources" \
    '{engine:$engine, cliConfig:$cli, platform:$platform, binarySha256:$binary, isolation:"enforced", init:"PASS",
      lockedProviders:($providers|split("\n")), providerSchema:"PASS", resources:$resources}' >>"$results"
  echo "$e: init PASS (lock = manifest), providers start PASS"
done
f0 after
echo "verify-mirror: PASS for $(echo $engine_list | wc -w | tr -d ' ') engines on $host_platform; results in $results"
