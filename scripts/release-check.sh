#!/usr/bin/env bash
# Checks a directory of release assets before anything is published: the
# three files the one-line installer downloads, a .sha256 that matches and
# names the stable tarball, the layout get.sh expects, the licence and
# third-party notices, the version the binary reports, get.sh pointing at
# the repository's latest release, and the signed release manifest that
# installed versions check before updating (it must verify with KEY_FILE and
# describe exactly these assets).
# Usage: scripts/release-check.sh DIR VERSION OWNER/REPO KEY_FILE
set -euo pipefail

dir=$(realpath "$1")
version=$2
repo=$3
keys=$(realpath "$4")
root=$(cd "$(dirname "$0")/.." && pwd)
asset=playkeeper-linux-amd64.tar.gz
manifest=playkeeper-release.json
top=playkeeper-$version-linux-amd64
fail() {
  echo "release check failed: $*" >&2
  exit 1
}

cd "$dir"
for f in get.sh "$asset" "$asset.sha256" "$manifest" "$manifest.sig"; do
  [ -s "$f" ] || fail "$f is missing or empty"
done
(cd "$root" && PATH="$root/.tools/go/bin:$PATH" go run ./cmd/release-sign verify --public-key-file "$keys" "$dir/$manifest") ||
  fail "$manifest is not signed by a key in $4"
field() { python3 -c "import json,sys; m=json.load(open('$manifest')); print($1)"; }
[ "$(field "m['version']")" = "$version" ] || fail "$manifest is for version $(field "m['version']"), not $version"
[ "$(field "m['assets'][0]['sha256']")" = "$(sha256sum "$asset" | cut -d' ' -f1)" ] || fail "$manifest does not describe $asset"
[ -n "$(field "m['notes'].strip()")" ] || fail "$manifest has no notes; add a \"## $version\" section to CHANGELOG.md"
sha256sum --quiet -c "$asset.sha256" || fail "$asset does not match $asset.sha256"
read -r _ named <"$asset.sha256"
[ "$named" = "$asset" ] || fail "$asset.sha256 names '$named' instead of $asset"
sh -n get.sh || fail "get.sh is not a valid sh script"
grep -qx "default_base=https://github.com/$repo/releases/latest/download" get.sh ||
  fail "get.sh does not download from https://github.com/$repo/releases/latest/download"

while IFS= read -r entry; do
  [[ $entry == "$top/"* ]] || fail "$asset has an entry outside $top/: $entry"
  if grep -Eiq '\.(jar|pem|key|env|db|sqlite)$|/\.git' <<<"$entry"; then
    fail "$asset contains an unexpected file: $entry"
  fi
done < <(tar -tzf "$asset")

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
tar -xzf "$asset" -C "$tmp"
for f in playkeeper install.sh README-INSTALL.txt THIRD_PARTY.md LICENSE THIRD_PARTY_NOTICES; do
  [ -f "$tmp/$top/$f" ] || fail "$top/$f is missing"
done
grep -q '^Go standard library and runtime (go' "$tmp/$top/THIRD_PARTY_NOTICES" ||
  fail "THIRD_PARTY_NOTICES does not have the Go standard library's licence"
if [ ! -x "$tmp/$top/playkeeper" ] || [ ! -x "$tmp/$top/install.sh" ]; then
  fail "playkeeper and install.sh must be executable"
fi
grep -q 'GNU AFFERO GENERAL PUBLIC LICENSE' "$tmp/$top/LICENSE" || fail "LICENSE is not the GNU AGPL"
file "$tmp/$top/playkeeper" | grep -q 'ELF 64-bit LSB executable, x86-64.*statically linked' ||
  fail "playkeeper is not a static x86-64 Linux binary"
reported=$("$tmp/$top/playkeeper" version)
[[ $reported == "playkeeper $version ("* ]] || fail "the binary reports '$reported', expected version $version"
[ "$(field "m['assets'][0]['binarySha256']")" = "$(sha256sum "$tmp/$top/playkeeper" | cut -d' ' -f1)" ] ||
  fail "$manifest does not describe the playkeeper binary in $asset"

echo "Release assets in $dir check out: $reported"
