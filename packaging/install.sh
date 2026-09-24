#!/bin/sh
# Playkeeper installer. It checks this server first and shows every change
# it will make before asking you to confirm. Usage: sudo ./install.sh
set -eu
here=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
if [ "$(id -u)" -ne 0 ]; then
  echo "Please run the installer as root: sudo $0 $*" >&2
  exit 1
fi
if [ "$(uname -s)" != "Linux" ]; then
  echo "Playkeeper installs on Linux servers only." >&2
  exit 1
fi
case $(uname -m) in
  x86_64 | amd64) ;;
  *)
    echo "This server's CPU is $(uname -m); this Playkeeper build is for x86_64 (amd64) only." >&2
    echo "  Fix: use an x86_64 VPS." >&2
    exit 1
    ;;
esac
exec "$here/playkeeper" install "$@"
