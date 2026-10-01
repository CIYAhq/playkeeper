#!/usr/bin/env bash
# The package cache (.github/actions/package-cache, package-cache.yml).
#
# prefix SET prints the start of SET's cache keys, for this system's Ubuntu
# release and CPU: apt-packages-ubuntu24.04-amd64-chromium. for the chromium
# set. A set's name has only lowercase letters, digits and hyphens, and its
# keys go on after a dot, so one set's keys never start with another's
# prefix (chromium's with chromium-webkit's): the jobs restore by prefix.
#
# keep DIR, after a set's installs, leaves in DIR the .deb files of the
# packages that are installed at their versions and deletes the rest (older
# versions apt no longer wants, apt's lock and partial downloads), then
# prints a name for what's left: the first 16 hex digits of the SHA-256 of
# their file names, sorted. The same packages always get the same name, so
# the workflow saves a set again only when a version in it changed. Fails
# when nothing is left, rather than give a set no packages.
#   scripts/package-cache.sh prefix SET
#   scripts/package-cache.sh keep DIR
# The dpkg formats are single-quoted on purpose: dpkg fills in their fields.
# shellcheck disable=SC2016
set -euo pipefail

usage() {
  echo "usage: scripts/package-cache.sh prefix SET | keep DIR" >&2
  exit 2
}

case "${1:-}" in
  prefix)
    set=${2:-}
    [[ $set =~ ^[a-z0-9]+(-[a-z0-9]+)*$ ]] || usage
    # shellcheck source=/dev/null
    . "${PACKAGE_CACHE_OS_RELEASE:-/etc/os-release}"
    echo "apt-packages-$ID$VERSION_ID-$(dpkg --print-architecture)-$set."
    ;;
  keep)
    dir=${2:-}
    [ -n "$dir" ] || usage
    installed=$(dpkg-query -W -f '${db:Status-Abbrev}${Package} ${Version} ${Architecture}\n' | sed -n 's/^ii *//p')
    rm -rf "$dir/partial" "$dir/lock"
    for deb in "$dir"/*.deb; do
      [ -e "$deb" ] || continue
      what=$(dpkg-deb -W --showformat='${Package} ${Version} ${Architecture}' "$deb")
      grep -qxF "$what" <<<"$installed" || rm -f "$deb"
    done
    names=$(cd "$dir" && find . -maxdepth 1 -name '*.deb' -printf '%f\n' | LC_ALL=C sort)
    if [ -z "$names" ]; then
      echo "package-cache: no installed package's .deb is in $dir; does apt keep its packages there?" >&2
      exit 1
    fi
    echo "package-cache: kept $(wc -l <<<"$names") packages, $(du -sh "$dir" | cut -f1)" >&2
    sha256sum <<<"$names" | cut -c1-16
    ;;
  *) usage ;;
esac
