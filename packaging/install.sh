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
# machine is the ELF e_machine an executable for this CPU has (its low byte,
# at offset 18): 62 for x86_64, 183 for AArch64.
case $(uname -m) in
  x86_64 | amd64) cpu=x86_64 machine=62 build=amd64 ;;
  aarch64 | arm64) cpu=aarch64 machine=183 build=arm64 ;;
  *)
    echo "This server's CPU is $(uname -m); Playkeeper runs on x86_64 (amd64) and 64-bit ARM (aarch64) servers." >&2
    echo "  Fix: use an x86_64 or 64-bit ARM server. On a Raspberry Pi 4 or 5, install a 64-bit system." >&2
    exit 1
    ;;
esac
if [ "$(od -An -tu1 -j18 -N1 "$here/playkeeper" 2>/dev/null | tr -d ' ')" != "$machine" ]; then
  echo "This Playkeeper download is for another CPU; this server's is $cpu." >&2
  echo "  Fix: download playkeeper-linux-$build.tar.gz instead." >&2
  exit 1
fi
exec "$here/playkeeper" install "$@"
