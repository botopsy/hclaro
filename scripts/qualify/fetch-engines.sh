#!/usr/bin/env bash
# fetch-engines.sh <dir> [--platform darwin_amd64] [engine-version...]
# Downloads only the OD-1b engine versions, checks each archive against its pinned SHA-256 and the
# signed SHA256SUMS (OD-6), and unpacks to <dir>/<engine>-<version>-<platform>/. Prints archive and
# binary SHA-256 for the evidence. Default: all approved versions.
set -euo pipefail
. "$(dirname "$0")/lib.sh"

[ $# -ge 1 ] || die "usage: fetch-engines.sh <dir> [--platform <os_arch>] [terraform-1.5.5 ...]"
dir=$1; shift
platform=darwin_amd64
if [ "${1:-}" = --platform ]; then platform=${2:-}; shift 2; fi

# Pinned from the signed SHA256SUMS in F-6 (f6-report.json, 2026-11-14). A changed upstream file fails.
# Only darwin_amd64 is approved (OD-1b, OD-3); other platforms need their own approval and pins.
pin() {
  case "$1/$platform" in
    terraform-1.5.5/darwin_amd64) echo 6d61639e2141b7c23a9219c63994f729aa41f91110a1aa08b8a37969fb45e229 ;;
    terraform-1.5.7/darwin_amd64) echo b310ec0e626e9799000cfc8e30247cd827cf7f8030c8e0400257c7f111e93537 ;;
    terraform-1.17.2/darwin_amd64) echo 66b472dcc36cf770d83757ed4d002c476174842c05c68637b40a7e99519cf3e1 ;;
    opentofu-1.6.3/darwin_amd64) echo 25b559b6b0cfafaa96ad5d315efaff42186f1fcad09afc6ba4958bc73b4d7ba9 ;;
    opentofu-1.13.9/darwin_amd64) echo ac5505dd19d896f327d0ab727d4b853413c694931a7602b31f93c3a3e9d46d00 ;;
    *) return 1 ;;
  esac
}

engines=("$@")
[ ${#engines[@]} -gt 0 ] || engines=(terraform-1.5.5 terraform-1.5.7 terraform-1.17.2 opentofu-1.6.3 opentofu-1.13.9)
# Refuse everything unknown before any request.
for e in "${engines[@]}"; do pin "$e" >/dev/null || die "not approved: $e for $platform"; done

mkdir -p "$dir"
dir=$(cd "$dir" && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/qualify-engines.XXXXXX")
trap 'rm -rf "$work"' EXIT

for e in "${engines[@]}"; do
  name=${e%-*} v=${e##*-}
  case $name in
    terraform) base=https://releases.hashicorp.com/terraform/$v; zip=terraform_${v}_$platform.zip
               sums=terraform_${v}_SHA256SUMS; sig=$sums.72D7468F.sig; key=hashicorp.asc; fpr=$HASHICORP_FPR; bin=terraform ;;
    opentofu)  base=https://github.com/opentofu/opentofu/releases/download/v$v; zip=tofu_${v}_$platform.zip
               sums=tofu_${v}_SHA256SUMS; sig=$sums.gpgsig; key=opentofu.asc; fpr=$OPENTOFU_FPR; bin=tofu ;;
  esac
  out=$dir/$name-$v-$platform
  fetch "$base/$sums" "$work/$sums"
  fetch "$base/$sig" "$work/$sig"
  sigline=$(verify_sig "$work/$sig" "$work/$sums" "$QUALIFY_DIR/keys/$key" "$fpr")
  want=$(pin "$e")
  listed=$(awk -v z="$zip" '$2==z{print $1}' "$work/$sums")
  [ "$listed" = "$want" ] || die "$zip: signed SHA256SUMS says '$listed', pinned $want"
  fetch "$base/$zip" "$work/$zip"
  got=$(sha256 "$work/$zip")
  [ "$got" = "$want" ] || die "$zip: archive SHA-256 $got does not match pinned $want"
  rm -rf "$out"; mkdir -p "$out"
  unzip -q -o "$work/$zip" "$bin" -d "$out"
  chmod 0755 "$out/$bin"
  printf '%s archive=%s binary=%s signature=%s\n' "$e-$platform" "$got" "$(sha256 "$out/$bin")" "$sigline"
done
