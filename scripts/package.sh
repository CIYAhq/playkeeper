#!/usr/bin/env bash
# Builds the release tarball: a static linux/amd64 binary with the embedded
# UI, the installer wrapper and install notes. Output goes to dist/, with the
# one-line installer assets a release would carry: get.sh and a copy of the
# tarball under the stable name it downloads (playkeeper-linux-amd64.tar.gz).
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
export PATH="$root/.tools/go/bin:$root/.tools/node/bin:$PATH"

commit=$(git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
version=${VERSION:-0.1.0-dev+$commit}
epoch=${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct 2>/dev/null || date +%s)}
date=$(date -u -d "@$epoch" +%Y-%m-%dT%H:%M:%SZ)
name="playkeeper-$version-linux-amd64"
out="$root/dist"
stage="$out/$name"

stable=playkeeper-linux-amd64.tar.gz
rm -rf "$stage" "${out:?}/$name.tar.gz" "$out/$name.tar.gz.sha256" "$out/${stable:?}" "$out/$stable.sha256" "$out/get.sh"
mkdir -p "$stage"

(cd web && npm ci --no-audit --no-fund --silent && npm run build --silent)

pkg=github.com/CIYAhq/playkeeper/internal/version
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false \
  -ldflags "-s -w -X $pkg.Version=$version -X $pkg.Commit=$commit -X $pkg.Date=$date" \
  -o "$stage/playkeeper" ./cmd/playkeeper

install -m 0755 packaging/install.sh "$stage/install.sh"
install -m 0644 packaging/README-INSTALL.txt "$stage/README-INSTALL.txt"
install -m 0644 docs/THIRD_PARTY.md "$stage/THIRD_PARTY.md"

tar --sort=name --owner=0 --group=0 --numeric-owner --mtime="@$epoch" \
  -C "$out" -czf "$out/$name.tar.gz" "$name"
cp "$out/$name.tar.gz" "$out/$stable"
install -m 0755 packaging/get.sh "$out/get.sh"
(cd "$out" && sha256sum "$name.tar.gz" > "$name.tar.gz.sha256" && sha256sum "$stable" > "$stable.sha256")

echo "Built $out/$name.tar.gz (one-line installer assets: $out/get.sh, $out/$stable)"
cat "$out/$name.tar.gz.sha256"
