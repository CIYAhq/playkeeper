#!/bin/sh
# The install log (nginx.conf): nginx's workers write a file a day to
# /var/log/playkeeper, which is a volume in Coolify (README.md), so they get to
# own it. Files older than 30 days are deleted at start and once a day after.
set -eu
logs=/var/log/playkeeper
mkdir -p "$logs"
chown nginx:nginx "$logs"
while :; do
  find "$logs" -name 'installs-*.log' -mtime +30 -delete || :
  sleep 86400
done </dev/null >/dev/null 2>&1 &
