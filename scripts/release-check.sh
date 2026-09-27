#!/usr/bin/env bash
# Checks a directory of release assets before anything is published: get.sh
# pointing at the repository's latest release, the signed release manifest
# that installed versions check before updating (it must verify with KEY_FILE
# and describe exactly these assets), and for each CPU (amd64, arm64) the
# files the one-line installer downloads: the tarball with a .sha256 that
# matches and names it, the layout get.sh expects, the licence and
# third-party notices, and a static binary for that CPU. The binary for this
# machine's CPU must report VERSION.
# Usage: scripts/release-check.sh DIR VERSION OWNER/REPO KEY_FILE
set -euo pipefail

dir=$(realpath "$1")
version=$2
repo=$3
keys=$(realpath "$4")
root=$(cd "$(dirname "$0")/.." && pwd)
manifest=playkeeper-release.json
fail() {
  echo "release check failed: $*" >&2
  exit 1
}

cd "$dir"
for f in get.sh "$manifest" "$manifest.sig"; do
  [ -s "$f" ] || fail "$f is missing or empty"
done
(cd "$root" && PATH="$root/.tools/go/bin:$PATH" go run ./cmd/release-sign verify --public-key-file "$keys" "$dir/$manifest") ||
  fail "$manifest is not signed by a key in $4, or does not describe a build for every CPU"
field() { python3 -c "import json,sys; m=json.load(open('$manifest')); print($1)"; }
[ "$(field "m['version']")" = "$version" ] || fail "$manifest is for version $(field "m['version']"), not $version"
[ -n "$(field "m['notes'].strip()")" ] || fail "$manifest has no notes; add a \"## $version\" section to CHANGELOG.md"
sh -n get.sh || fail "get.sh is not a valid sh script"
grep -qx "default_base=https://github.com/$repo/releases/latest/download" get.sh ||
  fail "get.sh does not download from https://github.com/$repo/releases/latest/download"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
reported=
for arch in amd64 arm64; do
  case $arch in
    amd64) cpu=x86_64 elf=x86-64 ;;
    arm64) cpu=aarch64 elf='ARM aarch64' ;;
  esac
  asset=playkeeper-linux-$arch.tar.gz
  top=playkeeper-$version-linux-$arch
  described() { field "next(a for a in m['assets'] if a['platform'] == 'linux-$arch')['$1']"; }
  for f in "$asset" "$asset.sha256"; do
    [ -s "$f" ] || fail "$f is missing or empty"
  done
  [ "$(described sha256)" = "$(sha256sum "$asset" | cut -d' ' -f1)" ] || fail "$manifest does not describe $asset"
  sha256sum --quiet -c "$asset.sha256" || fail "$asset does not match $asset.sha256"
  read -r _ named <"$asset.sha256"
  [ "$named" = "$asset" ] || fail "$asset.sha256 names '$named' instead of $asset"

  while IFS= read -r entry; do
    [[ $entry == "$top/"* ]] || fail "$asset has an entry outside $top/: $entry"
    if grep -Eiq '\.(jar|pem|key|env|db|sqlite)$|/\.git' <<<"$entry"; then
      fail "$asset contains an unexpected file: $entry"
    fi
  done < <(tar -tzf "$asset")

  mkdir "$tmp/$arch"
  tar -xzf "$asset" -C "$tmp/$arch"
  x=$tmp/$arch/$top
  for f in playkeeper install.sh README-INSTALL.txt THIRD_PARTY.md LICENSE THIRD_PARTY_NOTICES; do
    [ -f "$x/$f" ] || fail "$top/$f is missing"
  done
  grep -q '^Go standard library and runtime (go' "$x/THIRD_PARTY_NOTICES" ||
    fail "THIRD_PARTY_NOTICES in $asset does not have the Go standard library's licence"
  if [ ! -x "$x/playkeeper" ] || [ ! -x "$x/install.sh" ]; then
    fail "playkeeper and install.sh in $asset must be executable"
  fi
  grep -q 'GNU AFFERO GENERAL PUBLIC LICENSE' "$x/LICENSE" || fail "LICENSE in $asset is not the GNU AGPL"
  file "$x/playkeeper" | grep -q "ELF 64-bit LSB executable, $elf.*statically linked" ||
    fail "playkeeper in $asset is not a static $cpu Linux binary"
  [ "$(described binarySha256)" = "$(sha256sum "$x/playkeeper" | cut -d' ' -f1)" ] ||
    fail "$manifest does not describe the playkeeper binary in $asset"
  if [ "$(uname -m)" = "$cpu" ]; then
    reported=$("$x/playkeeper" version)
    [[ $reported == "playkeeper $version ("* ]] || fail "the $arch binary reports '$reported', expected version $version"
  fi
done
[ -n "$reported" ] || fail "neither binary is for this machine's CPU ($(uname -m)), so none could report its version"

echo "Release assets in $dir check out for amd64 and arm64: $reported"
