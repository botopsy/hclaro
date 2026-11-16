# Shared helpers for the M27 qualification scripts. Sourced, not executed.
# Signature checks follow OD-6d: hard fail, approved primary fingerprint, expiry and revocation checked.

QUALIFY_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
HASHICORP_FPR=C874011F0AB405110D02105534365D9472D7468F # OD-6a
OPENTOFU_FPR=E3E6E43D84CB852EADB0051D0C0AF313E5FD9F80  # OD-6b

die() { echo "error: $*" >&2; exit 1; }

sha256() { { sha256sum "$1" 2>/dev/null || shasum -a 256 "$1"; } | cut -d' ' -f1; }

# norm_hashes: reads provider hashes, one per line, from stdin and prints them with only a
# trailing CR removed (native jq on Windows ends lines with CRLF: run 43767550235 stored
# "h1:…=\r" in the manifest). Every value must then be exactly h1:<44 base64 chars> or
# zh:<64 lowercase hex>; anything else (an interior CR, a truncated or extended value, an
# unknown scheme, no hashes at all) fails, so the normalisation cannot admit a corrupted hash.
norm_hashes() {
  local line n=0
  while IFS= read -r line || [ -n "$line" ]; do
    line=${line%$'\r'}
    [[ $line =~ ^(h1:[A-Za-z0-9+/]{43}=|zh:[0-9a-f]{64})$ ]] || { echo "error: malformed provider hash: $(printf %q "$line")" >&2; return 1; }
    printf '%s\n' "$line"
    n=$((n + 1))
  done
  [ "$n" -gt 0 ] || { echo "error: no provider hashes" >&2; return 1; }
}

# fetch <url> <out>: HTTPS only, no redirects off https, size-capped.
fetch() {
  curl --proto =https --proto-redir =https --tlsv1.2 -fsSL --max-filesize 209715200 -o "$2" "$1" || die "download failed: $1"
}

# verify_sig <sig> <data> <keyfile> <approved primary fpr>: prints the result line; exits on any failure.
# Uses a throwaway GNUPGHOME (never the user's keyring) and stops its agent.
verify_sig() {
  local sig=$1 data=$2 keyfile=$3 want=$4 home status primary
  command -v gpg >/dev/null || die "gpg not installed: signature not verified for $data"
  home=$(mktemp -d "${TMPDIR:-/tmp}/qualify-gnupg.XXXXXX")
  gpg_() { gpg --homedir "$home" --batch --no-tty --no-auto-key-locate --keyserver none "$@"; }
  # Primary key: fingerprint, validity (r = revoked) and expiry, read before import.
  local pub
  pub=$(gpg_ --show-keys --with-colons --fixed-list-mode "$keyfile" 2>/dev/null | awk -F: -v w="$want" '
    $1=="pub"{v=$2; e=$7; first=1; next} $1=="fpr" && first {first=0; if ($10==w) print v ":" e}')
  gpg_ --import "$keyfile" 2>/dev/null
  status=$(gpg_ --status-fd 1 --verify "$sig" "$data" 2>/dev/null || true)
  gpgconf --homedir "$home" --kill all; rm -rf "$home"
  [ -n "$pub" ] || die "approved primary $want not in $keyfile"
  case $pub in r:*) die "key $want is revoked";; esac
  local exp=${pub#*:}
  [ -z "$exp" ] || [ "$exp" -gt "$(date +%s)" ] || die "key $want expired"
  case $status in *BADSIG*|*EXPKEYSIG*|*REVKEYSIG*|*EXPSIG*) die "bad signature on $data: $status";; esac
  primary=$(printf '%s\n' "$status" | awk '$2=="VALIDSIG"{print $NF}')
  [ "$primary" = "$want" ] || die "signature on $data not made by approved primary $want (got '${primary:-none}')"
  echo "good: primary $primary, $(gpg --version | head -1)"
}

# clean_env <cmd...>: run an engine outside the sandbox with no inherited environment or credentials.
# Needs CLEAN_HOME and TF_CLI_CONFIG_FILE set by the caller.
clean_env() {
  # Windows (Git Bash) engines need SystemRoot for networking; empty elsewhere.
  env -i PATH=/usr/bin:/bin ${SYSTEMROOT:+SYSTEMROOT="$SYSTEMROOT"} HOME="$CLEAN_HOME" TMPDIR="$CLEAN_HOME" TMP="$CLEAN_HOME" TEMP="$CLEAN_HOME" CHECKPOINT_DISABLE=1 \
    TF_CLI_CONFIG_FILE="$TF_CLI_CONFIG_FILE" TF_IN_AUTOMATION=1 LANG=C.UTF-8 LC_ALL=C.UTF-8 "$@"
}
