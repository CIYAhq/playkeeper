#!/usr/bin/env bash
# Builds the signed test releases the CI update job serves from a local
# mirror. Each goes to OUT_DIR/NAME/ with the assets a GitHub release has:
# get.sh, playkeeper-linux-amd64.tar.gz and its .sha256, and
# playkeeper-release.json and its .sig.
#
#   key                        makes a signing key for this run only
#                              (OUT_DIR/signing.key) and adds its public key to
#                              internal/update/release.pub in this checkout, so
#                              the builds that follow trust it. Never commit
#                              that change; the real release key is not used.
#   build NAME VERSION         this checkout, built by make package as VERSION
#   broken NAME VERSION UNITS  a release whose playkeeper never comes up
#                              healthy: it answers `version`, and `units` with
#                              the JSON in the file UNITS (from the installed
#                              Playkeeper), so the updater installs it, and
#                              fails at everything else
#
# Usage: scripts/e2e/update-releases.sh OUT_DIR key
#        scripts/e2e/update-releases.sh OUT_DIR build NAME VERSION
#        scripts/e2e/update-releases.sh OUT_DIR broken NAME VERSION UNITS
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
mkdir -p "$1"
out=$(realpath "$1")
cmd=$2
cd "$root"
export PATH="$root/.tools/go/bin:$root/.tools/node/bin:$PATH"
key=$out/signing.key

sign() { # DIR
  PLAYKEEPER_RELEASE_SIGNING_KEY=$(cat "$key") scripts/sign-release.sh "$1" "$out/keys.pub"
}

case $cmd in
  key)
    (umask 077 && go run ./cmd/release-sign keygen >"$key" 2>/dev/null)
    echo "Test signing key made; internal/update/release.pub now also trusts it (this checkout only)."
    ;;
  build)
    name=$3 version=$4
    VERSION=$version ./scripts/package.sh
    rm -rf "${out:?}/$name" && mkdir -p "$out/$name"
    cp dist/get.sh dist/playkeeper-linux-amd64.tar.gz dist/playkeeper-linux-amd64.tar.gz.sha256 dist/playkeeper-release.json "$out/$name/"
    sign "$out/$name"
    ;;
  broken)
    name=$3 version=$4 units=$5
    python3 -c 'import json,sys; json.load(open(sys.argv[1]))' "$units"
    dir=$out/$name
    top=playkeeper-$version-linux-amd64
    rm -rf "${dir:?}" && mkdir -p "$dir/stage/$top"
    # shellcheck disable=SC2016 # the script's own $1
    {
      echo '#!/bin/sh'
      echo '# A Playkeeper test release that never comes up healthy (CI update job).'
      echo 'case "$1" in'
      echo "  version) echo 'playkeeper $version (broken on purpose, 1970-01-01T00:00:00Z)' ;;"
      echo "  units) cat <<'UNITS'"
      cat "$units"
      echo 'UNITS'
      echo '    ;;'
      echo '  *) echo "This Playkeeper test release is broken on purpose." >&2; exit 1 ;;'
      echo 'esac'
    } >"$dir/stage/$top/playkeeper"
    chmod 0755 "$dir/stage/$top/playkeeper"
    tar --sort=name --owner=0 --group=0 --numeric-owner -C "$dir/stage" -czf "$dir/playkeeper-linux-amd64.tar.gz" "$top"
    rm -rf "$dir/stage"
    (cd "$dir" && sha256sum playkeeper-linux-amd64.tar.gz >playkeeper-linux-amd64.tar.gz.sha256)
    go run ./cmd/release-sign manifest --version "$version" --tarball "$dir/playkeeper-linux-amd64.tar.gz" \
      --notes "A test release that never comes up healthy, to prove the automatic rollback." >"$dir/playkeeper-release.json"
    sign "$dir"
    ;;
  *)
    echo "usage: $0 OUT_DIR key | build NAME VERSION | broken NAME VERSION UNITS" >&2
    exit 2
    ;;
esac
