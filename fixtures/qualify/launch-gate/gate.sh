#!/bin/sh
# M27.03 OD-8 gate (A6, A7, A7b only). Deterministic; no network; touches only ./.qual/ in the
# project directory it is started in. Exits 0 on release, 130 on an interrupt in normal mode,
# 3 on its hard timeout, 2 on a bad mode.
dir=./.qual
mkdir -p "$dir" || exit 2
case $1 in
normal) trap 'echo int >>"$dir/interrupted"; exit 130' INT; limit=1200 ;;
trap) trap 'echo int >>"$dir/interrupted"' INT; limit=9000 ;;
*) exit 2 ;;
esac
: >"$dir/reached"
i=0
while [ ! -f "$dir/release" ]; do
  i=$((i + 1))
  [ "$i" -gt "$limit" ] && exit 3
  # Sleep in the background and wait, so the INT trap runs as soon as the signal arrives
  # (sh defers traps until a foreground child exits).
  /bin/sleep 0.1 &
  wait $!
done
exit 0
