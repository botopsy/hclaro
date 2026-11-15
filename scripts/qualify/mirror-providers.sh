#!/usr/bin/env bash
# mirror-providers.sh <dir> <engines-dir>
# Builds one packed provider mirror per engine family (<dir>/terraform, <dir>/tofu) for the OD-2
# versions and OD-3 platforms, checks every archive against the provider's signed SHA256SUMS (OD-6e),
# and writes <dir>/mirror-manifest.json and one CLI config per family (<dir>/<family>.tfrc).
set -euo pipefail
. "$(dirname "$0")/lib.sh"

[ $# -eq 2 ] || die "usage: mirror-providers.sh <dir> <engines-dir>"
command -v jq >/dev/null || die "jq is required"
mkdir -p "$1"
dir=$(cd "$1" && pwd) engines=$(cd "$2" && pwd)
fixture=$QUALIFY_DIR/../../fixtures/qualify/providers/versions.tf
providers="random:3.9.1 local:2.9.1" # OD-2; must match the fixture
platforms="darwin_amd64"            # OD-3 until OD-7; each new platform needs a download approval
# The newest approved engine of each family builds that family's mirror.
families="terraform:terraform-1.17.2-darwin_amd64/terraform tofu:opentofu-1.13.9-darwin_amd64/tofu"

for p in $providers; do grep -q "\"= ${p#*:}\"" "$fixture" || die "fixture does not pin ${p%%:*} ${p#*:}"; done

work=$(mktemp -d "${TMPDIR:-/tmp}/qualify-mirror.XXXXXX")
trap 'rm -rf "$work"' EXIT
export CLEAN_HOME=$work/home TF_CLI_CONFIG_FILE=$work/home/empty.tfrc
mkdir -p "$CLEAN_HOME/cfg" && : >"$TF_CLI_CONFIG_FILE"
cp "$fixture" "$CLEAN_HOME/cfg/"

entries=$work/entries.jsonl
: >"$entries"
for f in $families; do
  family=${f%%:*} bin=$engines/${f#*:}
  [ -x "$bin" ] || die "engine missing: $bin (run fetch-engines.sh first)"
  rm -rf "${dir:?}/$family"
  args=(); for pl in $platforms; do args+=("-platform=$pl"); done
  (cd "$CLEAN_HOME/cfg" && clean_env "$bin" providers mirror "${args[@]}" "$dir/$family") >"$work/$family.log" 2>&1 ||
    { cat "$work/$family.log" >&2; die "$family providers mirror failed"; }

  for p in $providers; do
    name=${p%%:*} v=${p#*:}
    # The host directory the engine actually wrote (registry.terraform.io or registry.opentofu.org).
    hostdir=$(cd "$dir/$family" && ls -d */hashicorp/"$name") || die "$family: no mirror directory for hashicorp/$name"
    [ "$(echo "$hostdir" | wc -l)" -eq 1 ] || die "$family: more than one host directory for $name"
    host=${hostdir%%/*}
    case $host in
      registry.terraform.io) base=https://releases.hashicorp.com/terraform-provider-$name/$v
                             sig=terraform-provider-${name}_${v}_SHA256SUMS.72D7468F.sig key=hashicorp.asc fpr=$HASHICORP_FPR ;;
      registry.opentofu.org) base=https://github.com/opentofu/terraform-provider-$name/releases/download/v$v
                             sig=terraform-provider-${name}_${v}_SHA256SUMS.sig key=opentofu.asc fpr=$OPENTOFU_FPR ;;
      *) die "$family: unexpected registry host $host" ;;
    esac
    sums=terraform-provider-${name}_${v}_SHA256SUMS
    fetch "$base/$sums" "$work/$family-$sums"
    fetch "$base/$sig" "$work/$family-$sig"
    sigline=$(verify_sig "$work/$family-$sig" "$work/$family-$sums" "$QUALIFY_DIR/keys/$key" "$fpr")
    for pl in $platforms; do
      zip=terraform-provider-${name}_${v}_$pl.zip
      [ -f "$dir/$family/$hostdir/$zip" ] || die "$family: $zip not in mirror"
      got=$(sha256 "$dir/$family/$hostdir/$zip")
      listed=$(awk -v z="$zip" '$2==z{print $1}' "$work/$family-$sums")
      [ -n "$listed" ] && [ "$got" = "$listed" ] || die "$family: $zip SHA-256 $got does not match signed SHA256SUMS '$listed'"
      h1=$(jq -r --arg z "$zip" '.archives | to_entries[] | select(.value.url==$z) | .value.hashes[]' "$dir/$family/$hostdir/$v.json")
      jq -nc --arg family "$family" --arg host "$host" --arg provider "hashicorp/$name" --arg version "$v" \
        --arg platform "$pl" --arg archive "$hostdir/$zip" --arg sha256 "$got" --arg h1 "$h1" \
        --arg signer "$fpr" --arg sums "$base/$sums" \
        '{family:$family, host:$host, provider:$provider, version:$version, platform:$platform,
          archive:$archive, sha256:$sha256, hashes:($h1|split("\n")), sha256sums:$sums, sha256sumsSigner:$signer}' >>"$entries"
    done
    echo "$family $host/hashicorp/$name $v: archives match signed SHA256SUMS ($sigline)"
  done
  # Registry-free CLI config: init cannot fall back to the network even if it is reachable.
  # The path is relative to the run dir verify-mirror.sh creates (mirror copied in there).
  printf 'provider_installation {\n  filesystem_mirror {\n    path = "%s"\n  }\n  direct {\n    exclude = ["*/*"]\n  }\n}\n' \
    "@MIRROR@/$family" >"$dir/$family.tfrc"
done

# Deterministic: no timestamps, no absolute paths, sorted.
jq -sS --arg platforms "$platforms" '{platforms:($platforms|split(" ")), entries:(sort_by(.family, .provider, .platform))}' \
  "$entries" >"$dir/mirror-manifest.json"
echo "mirror-manifest.json sha256=$(sha256 "$dir/mirror-manifest.json")"
