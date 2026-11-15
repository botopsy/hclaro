#!/usr/bin/env bash
# qualify.sh: M27.02 real-engine harness runner (`make qualify`). Environment inputs:
#   TF, TOFU   absolute paths to OD-1 approved engine binaries (at least one; else NOT_TESTED)
#   TF_ALT, TOFU_ALT  optional second OD-1 binary of the same engine (M27.05 H6 binary swap)
#   MIRROR     the M27.01 mirror root (mirror-manifest.json, terraform/, tofu/, *.tfrc)
#   OUT        result directory (run.json, artifacts/, probes)
#   HCLARO_BIN default bin/hclaro; ISOLATION default sandbox-exec on macOS, else proxy;
#   HCLARO_QUAL_KEEP=1 keeps the raw run directory on failure; RUN_FILTER is passed to -test.run.
# Everything is compiled here, outside the isolation, while the network is available. The
# test binary, hclaro, netprobe, the engines and the mirror are then copied into a run
# directory outside the home directory (sandbox.sb denies reads under it) and the test binary
# runs there with `env -i` inside the isolation. No Go toolchain runs inside it.
set -euo pipefail
. "$(dirname "$0")/lib.sh"

repo=$(cd "$QUALIFY_DIR/../.." && pwd -P)
[ -n "${OUT:-}" ] || die "OUT is required"
[ -n "${MIRROR:-}" ] || die "MIRROR is required"
hclaro=${HCLARO_BIN:-$repo/bin/hclaro}
[ -x "$hclaro" ] || die "hclaro binary missing: $hclaro (run make binary)"
real_home=$(cd "$HOME" && pwd -P)
case $(uname -s) in Darwin) isolation=${ISOLATION:-sandbox-exec} ;; *) isolation=${ISOLATION:-proxy} ;; esac
case $isolation in sandbox-exec|proxy|none) ;; *) die "ISOLATION must be sandbox-exec, proxy or none" ;; esac
[ "$isolation" != sandbox-exec ] || [ "$(uname -s)" = Darwin ] || die "sandbox-exec is macOS only"

run=$(cd "$(mktemp -d /tmp/hclaro-qualify.XXXXXX)" && pwd -P)
case $run/ in "$real_home"/*) die "run dir $run is under the real home" ;; esac
status=1
cleanup() {
  if [ "$status" -ne 0 ] && [ "${HCLARO_QUAL_KEEP:-}" = 1 ]; then
    echo "raw run directory kept (unsanitised, never copy it into the repository): $run" >&2
  else
    rm -rf "$run"
  fi
}
trap cleanup EXIT

commit=$(git -C "$repo" rev-parse HEAD)
dirty=false
[ -z "$(git -C "$repo" status --porcelain)" ] || dirty=true
pkg=hclaro/internal/qualify
ldflags="-X $pkg.buildCommit=$commit -X $pkg.buildDirty=$dirty -X $pkg.buildHome=$real_home -X $pkg.buildRepo=$repo -X $pkg.buildUser=$(id -un)"
# cgo on macOS only: F3 reads Hclaro's own peak memory with proc_pid_rusage (rss_darwin.go).
cgo=0
[ "$(uname -s)" != Darwin ] || cgo=1
(cd "$repo" && CGO_ENABLED=$cgo go test -c -tags realengine -ldflags "$ldflags" -o "$run/qualify.test" ./internal/qualify)
(cd "$repo" && CGO_ENABLED=0 go build -o "$run/netprobe" ./internal/qualify/netprobe)
cp "$hclaro" "$run/hclaro"
cp "$QUALIFY_DIR/sandbox.sb" "$run/sandbox.sb"
mkdir -p "$run/engines" "$run/mirror" "$run/out"
tf= tofu=
if [ -n "${TF:-}" ]; then cp "$TF" "$run/engines/terraform"; tf=$run/engines/terraform; fi
if [ -n "${TOFU:-}" ]; then cp "$TOFU" "$run/engines/tofu"; tofu=$run/engines/tofu; fi
tf_alt= tofu_alt=
if [ -n "${TF_ALT:-}" ]; then cp "$TF_ALT" "$run/engines/terraform-alt"; tf_alt=$run/engines/terraform-alt; fi
if [ -n "${TOFU_ALT:-}" ]; then cp "$TOFU_ALT" "$run/engines/tofu-alt"; tofu_alt=$run/engines/tofu-alt; fi
for f in mirror-manifest.json terraform.tfrc tofu.tfrc; do cp "$MIRROR/$f" "$run/mirror/"; done
# Hub processes with both engines use the combined config (checked by verify-mirror.sh COMBINED=1).
cp "$QUALIFY_DIR/hub.tfrc" "$run/mirror/hub.tfrc"
for family in terraform tofu; do cp -R "$MIRROR/$family" "$run/mirror/$family"; done

# Positive control, outside the isolation; the harness runs the isolated probes inside.
"$run/netprobe" control >"$run/f0-control.json" || true

vars=(PATH=/usr/bin:/bin HCLARO_QUAL_TERRAFORM="$tf" HCLARO_QUAL_TOFU="$tofu" HCLARO_BIN="$run/hclaro"
  HCLARO_QUAL_TERRAFORM_ALT="$tf_alt" HCLARO_QUAL_TOFU_ALT="$tofu_alt"
  HCLARO_QUAL_MIRROR="$run/mirror" HCLARO_QUAL_OUT="$run/out" HCLARO_QUAL_ISOLATION="$isolation")
[ "${HCLARO_QUAL_KEEP:-}" != 1 ] || vars+=(HCLARO_QUAL_KEEP=1)
# Only real-engine tests (TestReal*) run inside the isolation; the untagged unit tests need the
# repository and the Go toolchain.
args=(-test.v -test.timeout 90m -test.run "${RUN_FILTER:-^TestReal}")
set +e
case $isolation in
  sandbox-exec)
    (cd "$run" && env -i "${vars[@]}" sandbox-exec -D REAL_HOME="$real_home" -D RUN_TMP="$run" -f "$run/sandbox.sb" \
      "$run/qualify.test" "${args[@]}") 2>&1 | tee "$run/go-test.log"
    code=${PIPESTATUS[0]} ;;
  *)
    (cd "$run" && env -i "${vars[@]}" "$run/qualify.test" "${args[@]}") 2>&1 | tee "$run/go-test.log"
    code=${PIPESTATUS[0]} ;;
esac
set -e
mkdir -p "$OUT"
cp -R "$run/out/." "$OUT/"
status=$code
[ -f "$OUT/run.json" ] || { echo "qualify: no run.json written" >&2; status=1; }
echo "qualify: test binary exit $code; results in $OUT"
exit "$status"
