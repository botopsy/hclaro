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
# darwin_amd64: every OD-1b version. Other platforms: the PLATFORM set only (OD-3-M27.07, 2026-11-15).
pin() {
  case "$1/$platform" in
    terraform-1.5.5/darwin_amd64) echo 6d61639e2141b7c23a9219c63994f729aa41f91110a1aa08b8a37969fb45e229 ;;
    terraform-1.5.7/darwin_amd64) echo b310ec0e626e9799000cfc8e30247cd827cf7f8030c8e0400257c7f111e93537 ;;
    terraform-1.17.2/darwin_amd64) echo 66b472dcc36cf770d83757ed4d002c476174842c05c68637b40a7e99519cf3e1 ;;
    opentofu-1.6.3/darwin_amd64) echo 25b559b6b0cfafaa96ad5d315efaff42186f1fcad09afc6ba4958bc73b4d7ba9 ;;
    opentofu-1.13.9/darwin_amd64) echo ac5505dd19d896f327d0ab727d4b853413c694931a7602b31f93c3a3e9d46d00 ;;
    terraform-1.17.2/darwin_arm64) echo da0c7ef278a911347862758e7573a5907365e66485c2637cf5c217c5e227992e ;;
    terraform-1.17.2/linux_amd64) echo e6f16b2f0b137ee5729caa8c4e79a3bae4ee261acd198a1fcfa8584a6296605b ;;
    terraform-1.17.2/linux_arm64) echo 858e08c181a76b6b223e8c767a8e983922eebe2d062db949a2de66ce040c368c ;;
    terraform-1.17.2/windows_amd64) echo 2244b7a379ea8c4b224a2eab411b7288f41ef36316fca27ae4b51b1cf2a53024 ;;
    opentofu-1.13.9/darwin_arm64) echo 29ba2f4c78556a8d25476e5070fe4745495ef649c4a3b827170eb7436d23feb3 ;;
    opentofu-1.13.9/linux_amd64) echo bc44eb6b06446ba83bd4072114891987f590f66cca712737a7f1147c50c539ce ;;
    opentofu-1.13.9/linux_arm64) echo 04f7c93828b859d420269ed30f923ffebf09157b5d7fb95bf8d88a4d208b7fa3 ;;
    opentofu-1.13.9/windows_amd64) echo b23a811741911baa829aa7d3d3718626c549e8934546822b0e9b99b9fba75e4e ;;
    *) return 1 ;;
  esac
}

engines=("$@")
if [ ${#engines[@]} -eq 0 ]; then
  engines=(terraform-1.17.2 opentofu-1.13.9) # PLATFORM set
  [ "$platform" != darwin_amd64 ] || engines=(terraform-1.5.5 terraform-1.5.7 terraform-1.17.2 opentofu-1.6.3 opentofu-1.13.9)
fi
exe=
case $platform in windows_*) exe=.exe ;; esac
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
               sums=terraform_${v}_SHA256SUMS; sig=$sums.72D7468F.sig; key=hashicorp.asc; fpr=$HASHICORP_FPR; bin=terraform$exe ;;
    opentofu)  base=https://github.com/opentofu/opentofu/releases/download/v$v; zip=tofu_${v}_$platform.zip
               sums=tofu_${v}_SHA256SUMS; sig=$sums.gpgsig; key=opentofu.asc; fpr=$OPENTOFU_FPR; bin=tofu$exe ;;
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
