#!/bin/sh
# One-line Playkeeper install:
#
#   curl -fsSL https://playkeeper.io/install | sudo sh
#
# playkeeper.io/install serves this file from the latest release, as does
# https://github.com/CIYAhq/playkeeper/releases/latest/download/get.sh.
#
# Downloads playkeeper-linux-amd64.tar.gz and its .sha256 from the release
# location, checks the SHA-256 before anything from the download runs, then
# runs the installer, which shows every change and asks before making it.
# Arguments after "sh -s --" go to the installer, for example:
#
#   curl -fsSL .../get.sh | sudo sh -s -- --yes --game-port 25566
#
# Another release location: --base-url URL or PLAYKEEPER_BASE_URL=URL.
# Plain http:// is refused unless PLAYKEEPER_ALLOW_HTTP=1 (local test mirrors).
set -eu

default_base=https://github.com/CIYAhq/playkeeper/releases/latest/download
asset=playkeeper-linux-amd64.tar.gz
# The same limits as the agent's updater (internal/update).
max_tarball=$((200 * 1024 * 1024))
max_small=$((1024 * 1024))

say() { printf '%s\n' "$*"; }
die() {
  printf 'Playkeeper install stopped: %s\n' "$1" >&2
  [ -z "${2:-}" ] || printf '  Fix: %s\n' "$2" >&2
  exit 1
}

fetch() { # URL FILE MAX-BYTES
  rc=0
  curl -fsSL --retry 3 --proto "$proto" --proto-redir "$proto" --max-filesize "$3" -o "$2" "$1" || rc=$?
  # curl before 8.4 applies --max-filesize only to a size the server announces.
  if [ "$rc" = 63 ] || { [ "$rc" = 0 ] && [ "$(wc -c <"$2")" -gt "$3" ]; }; then
    die "$1 is larger than $(($3 / 1024 / 1024)) MB, too large to be a Playkeeper release file. Nothing was installed." "Check the release location; do not install from it if this keeps happening."
  fi
  return "$rc"
}

main() {
  base=${PLAYKEEPER_BASE_URL:-$default_base}
  assume_yes=0
  n=$#
  while [ "$n" -gt 0 ]; do
    arg=$1
    shift
    n=$((n - 1))
    case $arg in
      --base-url=*) base=${arg#--base-url=} ;;
      --base-url)
        [ "$n" -gt 0 ] || die "--base-url needs a URL."
        base=$1
        shift
        n=$((n - 1))
        ;;
      *)
        case $arg in -y | --yes) assume_yes=1 ;; esac
        set -- "$@" "$arg"
        ;;
    esac
  done
  base=${base%/}

  [ "$(uname -s)" = Linux ] || die "Playkeeper installs on Linux servers only."
  case $(uname -m) in
    x86_64 | amd64) ;;
    *) die "this server's CPU is $(uname -m); Playkeeper is built for x86_64 (amd64) only." "Use an x86_64 VPS." ;;
  esac
  [ "$(id -u)" -eq 0 ] || die "the installer needs root." "Pipe into sudo: curl -fsSL <this script's URL> | sudo sh"
  command -v curl >/dev/null 2>&1 || die "curl is not installed." "sudo apt-get install -y curl"
  case $base in
    https://*) proto='=https' ;;
    http://*)
      [ "${PLAYKEEPER_ALLOW_HTTP:-}" = 1 ] || die "$base is not HTTPS." "Use an https:// release location (PLAYKEEPER_ALLOW_HTTP=1 is only for local test mirrors)."
      proto='=http,https'
      say "Warning: downloading over plain HTTP from $base (PLAYKEEPER_ALLOW_HTTP=1)."
      ;;
    *) die "the release location must be an https:// URL, got: $base" ;;
  esac
  # The installer asks for confirmation; with "curl | sh" its input would be
  # this script, so the answer is read from the terminal instead.
  if [ "$assume_yes" = 0 ] && ! (true </dev/tty) 2>/dev/null; then
    die "there is no terminal to answer the installer's question on." "Run the command in an interactive terminal (for example over ssh), or skip the question with --yes: curl -fsSL <this script's URL> | sudo sh -s -- --yes"
  fi

  tmp=$(mktemp -d "${TMPDIR:-/tmp}/playkeeper-get.XXXXXX")
  trap 'rm -rf "$tmp"' EXIT
  trap 'exit 130' INT TERM

  say "Downloading $asset from $base"
  fetch "$base/$asset.sha256" "$tmp/$asset.sha256" "$max_small" ||
    die "could not download $base/$asset.sha256." "Check the address and this server's internet access, or install from the release tarball as the README describes."
  want=$(awk 'NR == 1 {print $1}' "$tmp/$asset.sha256")
  case $want in
    *[!0-9a-f]* | '') die "$asset.sha256 does not contain a SHA-256 checksum." ;;
  esac
  [ "${#want}" -eq 64 ] || die "$asset.sha256 does not contain a SHA-256 checksum."
  named=$(awk 'NR == 1 {print $2}' "$tmp/$asset.sha256")
  case $named in
    "$asset" | "*$asset") ;;
    '') die "$asset.sha256 does not name the file it is the checksum of." ;;
    *[!A-Za-z0-9._-]*) die "$asset.sha256 is not the checksum of $asset." ;;
    *) die "$asset.sha256 is the checksum of $named, not of $asset." ;;
  esac
  fetch "$base/$asset" "$tmp/$asset" "$max_tarball" || die "could not download $base/$asset."
  got=$(sha256sum "$tmp/$asset" | awk '{print $1}')
  [ "$got" = "$want" ] || die "the download does not match its published checksum (expected $want, got $got). Nothing was installed." "Try again later; if it keeps happening, do not install from this location."
  say "SHA-256 verified: $got"

  mkdir "$tmp/x"
  tar -xzf "$tmp/$asset" -C "$tmp/x" --no-same-owner
  count=$(find "$tmp/x" -mindepth 1 -maxdepth 1 | wc -l)
  dir=$(find "$tmp/x" -mindepth 1 -maxdepth 1 -type d -name 'playkeeper-*-linux-amd64')
  if [ "$count" -ne 1 ] || [ -z "$dir" ] || [ ! -f "$dir/install.sh" ] || [ ! -x "$dir/playkeeper" ]; then
    die "$asset does not have the layout of a Playkeeper release."
  fi
  "$dir/playkeeper" version >/dev/null 2>&1 ||
    die "cannot run programs from $tmp (is it mounted noexec?)." "Run again with TMPDIR set to a directory that allows it, for example: ... | sudo TMPDIR=/root sh"

  say "Starting the installer from ${dir##*/}"
  status=0
  if [ "$assume_yes" = 1 ]; then
    "$dir/install.sh" "$@" </dev/null || status=$?
  else
    "$dir/install.sh" "$@" </dev/tty || status=$?
  fi
  exit "$status"
}

main "$@"
