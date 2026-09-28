#!/usr/bin/env bash
# Builds the release tarballs, one for each CPU Playkeeper runs on (linux/amd64
# and linux/arm64): a static binary with the embedded UI, the installer
# wrapper, install notes, the licence and the third-party notices. Output goes
# to dist/, with the one-line installer assets a release carries: get.sh and a
# copy of each tarball under the stable name it downloads
# (playkeeper-linux-amd64.tar.gz, playkeeper-linux-arm64.tar.gz), and the
# release manifest (playkeeper-release.json) that the release workflow signs
# and installed versions check before updating.
# CURSEFORGE_API_KEY, when set, goes into the binary as the CurseForge key for
# owners without their own; it is never printed. --binary OUT builds only the
# linux/amd64 binary, for scripts/package_test.sh.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
export PATH="$root/.tools/go/bin:$root/.tools/node/bin:$PATH"

commit=$(git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
# A build that isn't a release is named after a version no release has reached,
# the one after the next release, so it sorts above the published releases
# before and after the next one: named after a published one, it sorts below
# it, and an installed test build offers that release as an update. After
# each release this names the next version.
version=${VERSION:-0.6.0-dev+$commit}
epoch=${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct 2>/dev/null || date +%s)}
date=$(date -u -d "@$epoch" +%Y-%m-%dT%H:%M:%SZ)
arches=(amd64 arm64)
out="$root/dist"

pkg=github.com/CIYAhq/playkeeper/internal/version
curseforge=github.com/CIYAhq/playkeeper/internal/modpacks/curseforge

# go_build OUT [GOARCH] builds the linux binary, amd64 unless another GOARCH
# is given, with the version stamped in. -trimpath keeps -ldflags, and so the
# key, out of the build info that `go version -m` shows.
go_build() {
  local ldflags="-s -w -X $pkg.Version=$version -X $pkg.Commit=$commit -X $pkg.Date=$date"
  if [ -n "${CURSEFORGE_API_KEY:-}" ]; then
    case $CURSEFORGE_API_KEY in
    *[[:space:]\'\"\\]*)
      echo "CURSEFORGE_API_KEY has spaces, quotes or backslashes, which a CurseForge API key never has" >&2
      return 1
      ;;
    esac
    ldflags+=" -X $curseforge.BuildKey=$CURSEFORGE_API_KEY"
  fi
  CGO_ENABLED=0 GOOS=linux GOARCH=${2:-amd64} go build -trimpath -buildvcs=false -ldflags "$ldflags" -o "$1" ./cmd/playkeeper
}

if [ "${1:-}" = --binary ]; then
  go_build "${2:?usage: scripts/package.sh --binary OUT}"
  exit 0
fi

manifest=playkeeper-release.json
rm -rf "$out/get.sh" "${out:?}/${manifest:?}" "$out/$manifest.sig"
for arch in "${arches[@]}"; do
  name="playkeeper-$version-linux-$arch"
  stable="playkeeper-linux-$arch.tar.gz"
  rm -rf "${out:?}/$name" "$out/$name.tar.gz" "$out/$name.tar.gz.sha256" "$out/${stable:?}" "$out/$stable.sha256"
done

(cd web && npm ci --no-audit --no-fund --silent && npm run build --silent)
./scripts/third-party-notices.sh --check

tarballs=()
for arch in "${arches[@]}"; do
  name="playkeeper-$version-linux-$arch"
  stable="playkeeper-linux-$arch.tar.gz"
  stage="$out/$name"
  mkdir -p "$stage"
  go_build "$stage/playkeeper" "$arch"
  go version -m "$stage/playkeeper" | awk '$1 == "dep" {print $2, $3}' | while read -r mod ver; do
    grep -qxF "$mod $ver (Go module)" THIRD_PARTY_NOTICES ||
      { echo "THIRD_PARTY_NOTICES has no section for $mod $ver, which the linux/$arch binary links" >&2; exit 1; }
  done

  install -m 0755 packaging/install.sh "$stage/install.sh"
  install -m 0644 packaging/README-INSTALL.txt "$stage/README-INSTALL.txt"
  install -m 0644 docs/THIRD_PARTY.md "$stage/THIRD_PARTY.md"
  install -m 0644 LICENSE "$stage/LICENSE"
  install -m 0644 THIRD_PARTY_NOTICES "$stage/THIRD_PARTY_NOTICES"

  tar --sort=name --owner=0 --group=0 --numeric-owner --mtime="@$epoch" \
    -C "$out" -czf "$out/$name.tar.gz" "$name"
  cp "$out/$name.tar.gz" "$out/$stable"
  (cd "$out" && sha256sum "$name.tar.gz" > "$name.tar.gz.sha256" && sha256sum "$stable" > "$stable.sha256")
  tarballs+=(--tarball "$out/$stable")
done
install -m 0755 packaging/get.sh "$out/get.sh"

# A release's notes are its CHANGELOG.md section; other builds say what they are.
if grep -q "^## ${version} *\$" CHANGELOG.md; then
  notes=(--changelog CHANGELOG.md)
else
  notes=(--notes "Playkeeper $version, a build that is not a published release.")
fi
go run ./cmd/release-sign manifest --version "$version" --date "$date" "${tarballs[@]}" "${notes[@]}" >"$out/$manifest"

echo "Built in $out, for ${arches[*]}: playkeeper-$version-linux-{$(IFS=,; echo "${arches[*]}")}.tar.gz (one-line installer assets: get.sh, playkeeper-linux-*.tar.gz; release manifest: $manifest, unsigned)"
for arch in "${arches[@]}"; do cat "$out/playkeeper-$version-linux-$arch.tar.gz.sha256"; done
