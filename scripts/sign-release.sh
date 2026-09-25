#!/usr/bin/env bash
# Signs a built release's manifest (playkeeper-release.json) and writes the
# key file that verifies it to KEY_FILE.
#
# A real release signs with the PLAYKEEPER_RELEASE_SIGNING_KEY secret and
# stops unless its public key is listed in internal/update/release.pub: that
# is the key the release binary was built with, and the key installed
# versions check updates against. A dry run (DRY_RUN=true) signs with a
# throwaway key instead, so the steps are still exercised.
# Usage: scripts/sign-release.sh DIST_DIR KEY_FILE
set -euo pipefail

dir=$1
keys=$2
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
export PATH="$root/.tools/go/bin:$PATH"
fail() {
  echo "release signing failed: $*" >&2
  exit 1
}
sign() { go run ./cmd/release-sign "$@"; }

if [ "${DRY_RUN:-}" = true ]; then
  echo "# throwaway key for a dry run; nothing signed with it is published" >"$keys"
  PLAYKEEPER_RELEASE_SIGNING_KEY=$(sign keygen --public-key-file "$keys" 2>/dev/null)
  export PLAYKEEPER_RELEASE_SIGNING_KEY
else
  [ -n "${PLAYKEEPER_RELEASE_SIGNING_KEY:-}" ] ||
    fail "the PLAYKEEPER_RELEASE_SIGNING_KEY repository secret is not set; see CONTRIBUTING.md (Releases)"
  pub=$(sign pubkey)
  grep -qxF "$pub" internal/update/release.pub ||
    fail "the signing key's public key is not in internal/update/release.pub, so installed Playkeeper versions could not verify this release; see CONTRIBUTING.md (Releases)"
  cp internal/update/release.pub "$keys"
fi
sign sign "$dir/playkeeper-release.json"
sign verify --public-key-file "$keys" "$dir/playkeeper-release.json"
