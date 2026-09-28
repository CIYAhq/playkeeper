#!/bin/sh
# playkeeper.io/install: runs the one-line installer, get.sh, from the latest
# Playkeeper release on GitHub, and tells it the install came through
# playkeeper.io, for the anonymous install count (README.md, "Usage stats").
# The same get.sh, without these lines, is at
# https://github.com/CIYAhq/playkeeper/releases/latest/download/get.sh
#
# DO_NOT_TRACK=1 turns usage stats off:
#   curl -fsSL https://playkeeper.io/install | sudo DO_NOT_TRACK=1 sh
set -eu
url=https://github.com/CIYAhq/playkeeper/releases/latest/download/get.sh
die() {
  printf 'Playkeeper install stopped: %s\n' "$1" >&2
  exit 1
}
script=$(curl -fsSL --retry 3 --proto '=https' --proto-redir '=https' --max-filesize 1048576 "$url") || die "could not download $url."
[ -n "$script" ] || die "$url was empty."
# playkeeper.io/install/<code> is a channel's command; nginx puts its code here.
PLAYKEEPER_INSTALL_SOURCE=playkeeper.io
PLAYKEEPER_INSTALL_CHANNEL=""
export PLAYKEEPER_INSTALL_SOURCE PLAYKEEPER_INSTALL_CHANNEL
# get.sh takes this script's place, so the installer it hands over to is
# still sudo's own child when it asks on the terminal.
exec sh -c "$script" get.sh "$@"
