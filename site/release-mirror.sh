#!/bin/sh
# playkeeper.io's copy of the latest release's signed manifest and its
# signature, which installed Playkeepers check for a new release
# (/releases/latest/ in nginx.conf, cmd/release-mirror): fetched once before
# nginx starts, so a new container serves them from its first request, then
# looked after every minute in the background.
set -eu
dir=/usr/share/nginx/html/releases/latest
/usr/local/bin/release-mirror -dir "$dir" -once ||
  echo "release-mirror: no copy of the latest release yet; trying again every minute" >&2
/usr/local/bin/release-mirror -dir "$dir" </dev/null &
