#!/usr/bin/env bash
# dispatch-manifest.sh <dispatch-dir> <workflow-run-id> <commit>
# Builds <dispatch-dir>/manifest.json for one real-engine workflow dispatch (M27.07), from the
# downloaded artifacts (<dispatch-dir>/real-engine-<platform>/{qualify,f5}/run.json ...) and the
# saved job log (<dispatch-dir>/run-<id>.log). Each platform keeps its own run.json and files; the
# manifest only links them, with SHA-256s, and lists every scenario status per platform and engine.
# It fails if a run.json is missing, partial, dirty or from another commit.
set -euo pipefail
. "$(dirname "$0")/lib.sh"

[ $# -eq 3 ] || die "usage: dispatch-manifest.sh <dispatch-dir> <workflow-run-id> <commit>"
command -v jq >/dev/null || die "jq is required"
dir=$(cd "$1" && pwd) id=$2 commit=$3
rm -f "$dir/manifest.json" # a failed build must not leave an older manifest behind
entries=$(mktemp)
trap 'rm -f "$entries"' EXIT

for art in "$dir"/real-engine-*/; do
  art=${art%/}
  platform=${art##*/real-engine-}
  for kind in qualify f5; do
    rj=$art/$kind/run.json
    [ -f "$rj" ] || { [ "$kind" = f5 ] && continue; die "$platform: $kind/run.json missing"; }
    jq -e --arg c "$commit" '.partial == false and .hclaroDirty == false and .hclaroCommit == $c' "$rj" >/dev/null ||
      die "$platform/$kind: run.json is partial, dirty or not from $commit"
    files=$(cd "$art/$kind" && find . -type f | LC_ALL=C sort | while read -r f; do
      jq -nc --arg path "real-engine-$platform/$kind/${f#./}" --arg sha256 "$(sha256 "$f")" '{path:$path, sha256:$sha256}'; done | jq -s .)
    jq -c --arg platform "$platform" --arg kind "$kind" --argjson files "$files" '{
      platform: $platform, kind: $kind, runId, os, arch, osVersion, runnerImage, runnerImageVersion,
      filesystemCaseSensitive, hclaroVersion, hclaroBinarySha256, mirrorManifestSha256,
      engines: [.engines[]? | {name, version, binarySha256, archiveSha256, provenance}],
      isolation: {mechanism: .isolation.mechanism, level: .isolation.level, reason: .isolation.reason},
      credentialFilesReachable, goTestExit,
      counts: (.scenarios | group_by(.status) | map({key: .[0].status, value: length}) | from_entries),
      byEngine: (.scenarios | group_by(.engine) | map({key: (.[0].engine // "none"), value: map({id, status, reason})}) | from_entries),
      files: $files}' "$rj" >>"$entries"
  done
done
[ -s "$entries" ] || die "no real-engine-* artifacts under $dir"
log=$dir/run-$id.log
logsum=null
[ -f "$log" ] && logsum=$(jq -nc --arg path "${log##*/}" --arg sha256 "$(sha256 "$log")" '{path:$path, sha256:$sha256}')
jq -sS --arg id "$id" --arg commit "$commit" --argjson log "$logsum" \
  '{schemaVersion: 1, workflowRunId: $id, commit: $commit, jobLog: $log, runs: sort_by(.platform, .kind)}' \
  "$entries" >"$dir/manifest.json"
echo "manifest.json sha256=$(sha256 "$dir/manifest.json")"
