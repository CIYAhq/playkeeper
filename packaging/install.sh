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
exec "$here/playkeeper" install "$@"
