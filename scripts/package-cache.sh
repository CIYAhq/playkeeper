#!/usr/bin/env bash
# The package cache (.github/actions/package-cache, package-cache.yml): after a
# set's installs, keep DIR leaves in DIR the .deb files of the packages that
# are installed at their versions and deletes the rest (older versions apt
# no longer wants, apt's lock and partial downloads), then prints a name for
# what's left: the first 16 hex digits of the SHA-256 of their file names,
# sorted. The same packages always get the same name, so the workflow saves
# a set again only when a version in it changed. Fails when nothing is left,
# rather than give a set no packages.
#   scripts/package-cache.sh keep DIR
# The dpkg formats are single-quoted on purpose: dpkg fills in their fields.
# shellcheck disable=SC2016
set -euo pipefail

if [ "${1:-}" != keep ] || [ -z "${2:-}" ]; then
  echo "usage: scripts/package-cache.sh keep DIR" >&2
  exit 2
fi
dir=$2
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
