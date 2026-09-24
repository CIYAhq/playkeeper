#!/usr/bin/env bash
# Checks a directory of release assets before anything is published: the
# three files the one-line installer downloads, a .sha256 that matches and
# names the stable tarball, the layout get.sh expects, the licence, the
# version the binary reports, and get.sh pointing at the repository's latest
# release.
# Usage: scripts/release-check.sh DIR VERSION OWNER/REPO
set -euo pipefail

dir=$1
version=$2
repo=$3
asset=playkeeper-linux-amd64.tar.gz
top=playkeeper-$version-linux-amd64
fail() {
  echo "release check failed: $*" >&2
  exit 1
}

cd "$dir"
for f in get.sh "$asset" "$asset.sha256"; do
  [ -s "$f" ] || fail "$f is missing or empty"
done
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
for f in playkeeper install.sh README-INSTALL.txt THIRD_PARTY.md LICENSE; do
  [ -f "$tmp/$top/$f" ] || fail "$top/$f is missing"
done
if [ ! -x "$tmp/$top/playkeeper" ] || [ ! -x "$tmp/$top/install.sh" ]; then
  fail "playkeeper and install.sh must be executable"
fi
grep -q 'GNU AFFERO GENERAL PUBLIC LICENSE' "$tmp/$top/LICENSE" || fail "LICENSE is not the GNU AGPL"
file "$tmp/$top/playkeeper" | grep -q 'ELF 64-bit LSB executable, x86-64.*statically linked' ||
  fail "playkeeper is not a static x86-64 Linux binary"
reported=$("$tmp/$top/playkeeper" version)
[[ $reported == "playkeeper $version ("* ]] || fail "the binary reports '$reported', expected version $version"

echo "Release assets in $dir check out: $reported"
