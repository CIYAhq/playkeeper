#!/usr/bin/env bash
# One-time contributor setup: installs the pinned Go and Node toolchains into
# .tools/ (checksum-verified, no system changes besides missing apt basics)
# and installs the web UI's npm dependencies.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
tools="$root/.tools"
pins="$root/scripts/toolchains.txt"

need=()
for bin in curl tar xz git make; do
  command -v "$bin" >/dev/null 2>&1 || need+=("$bin")
done
if [ ! -d /etc/ssl/certs ] || [ -z "$(ls -A /etc/ssl/certs 2>/dev/null)" ]; then
  need+=(ca-certificates)
fi
if [ "${#need[@]}" -gt 0 ]; then
  pkgs=()
  for b in "${need[@]}"; do
    case "$b" in
      xz) pkgs+=(xz-utils) ;;
      *) pkgs+=("$b") ;;
    esac
  done
  if [ "$(id -u)" -eq 0 ] && command -v apt-get >/dev/null 2>&1; then
    echo "Installing missing basics: ${pkgs[*]}"
    DEBIAN_FRONTEND=noninteractive apt-get update -qq
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "${pkgs[@]}" >/dev/null
  else
    echo "Missing tools: ${pkgs[*]}" >&2
    echo "Install them with: sudo apt-get install -y ${pkgs[*]}" >&2
    exit 1
  fi
fi

case "$(uname -m)" in
  x86_64) goarch=amd64; nodearch=x64 ;;
  aarch64 | arm64) goarch=arm64; nodearch=arm64 ;;
  *) echo "Unsupported CPU: $(uname -m)" >&2; exit 1 ;;
esac
go_version=$(awk '$1 == "go" {print $2}' "$pins")
node_version=$(awk '$1 == "node" {print $2}' "$pins")
# --retry alone gives up when a connection drops mid-download (curl errors 18
# and 92); curl before 7.71 has no --retry-all-errors.
retry=(--retry 3)
case "$(curl --help all 2>/dev/null || true)" in
  *--retry-all-errors*) retry+=(--retry-all-errors) ;;
esac

fetch() { # url file
  local url=$1 file=$2 want got
  want=$(awk -v f="$file" '$2 == f {print $1}' "$pins")
  if [ -z "$want" ]; then
    echo "No pinned checksum for $file" >&2
    exit 1
  fi
  mkdir -p "$tools/downloads"
  if [ ! -f "$tools/downloads/$file" ]; then
    echo "Downloading $file"
    curl -fsSL "${retry[@]}" -o "$tools/downloads/$file.part" "$url"
    mv "$tools/downloads/$file.part" "$tools/downloads/$file"
  fi
  got=$(sha256sum "$tools/downloads/$file" | awk '{print $1}')
  if [ "$got" != "$want" ]; then
    rm -f "$tools/downloads/$file"
    echo "Checksum mismatch for $file (got $got, want $want)" >&2
    exit 1
  fi
}

if [ "$("$tools/go/bin/go" env GOVERSION 2>/dev/null || true)" != "go$go_version" ]; then
  file="go$go_version.linux-$goarch.tar.gz"
  fetch "https://go.dev/dl/$file" "$file"
  rm -rf "$tools/go"
  tar -C "$tools" -xzf "$tools/downloads/$file"
fi

if [ "$("$tools/node/bin/node" --version 2>/dev/null || true)" != "v$node_version" ]; then
  file="node-v$node_version-linux-$nodearch.tar.xz"
  fetch "https://nodejs.org/dist/v$node_version/$file" "$file"
  rm -rf "$tools/node"
  mkdir -p "$tools/node"
  tar -C "$tools/node" --strip-components=1 -xJf "$tools/downloads/$file"
fi

export PATH="$tools/go/bin:$tools/node/bin:$PATH"
echo "Go:   $(go version)"
echo "Node: $(node --version), npm $(npm --version)"
(cd "$root/web" && npm ci --no-audit --no-fund)
(cd "$root" && go mod download)

cat <<EOF

Setup complete. Toolchains are in .tools/ (the Makefile uses them automatically).
Next:
  make check    # lint, typecheck and unit tests (what CI runs)
  make dev      # run the panel and agent locally: https://localhost:8443
  make package  # build the release tarball in dist/
EOF
