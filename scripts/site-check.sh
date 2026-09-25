#!/usr/bin/env bash
# Builds the playkeeper.io image from site/, runs it, and checks what it
# serves: / (the page, its files and the install command), /healthz, the
# /install redirect to get.sh of the latest release, the security headers,
# and the container's own health check. Needs Docker.
# Usage: scripts/site-check.sh   (SITE_CHECK_PORT picks the local port, default 8080)
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
image=playkeeper-site:check
name=playkeeper-site-check
port=${SITE_CHECK_PORT:-8080}
base=http://127.0.0.1:$port
want=https://github.com/CIYAhq/playkeeper/releases/latest/download/get.sh
fail() {
  echo "site check failed: $*" >&2
  docker logs "$name" 2>&1 | tail -20 >&2 || true
  exit 1
}

docker build -t "$image" "$root/site"
docker rm -f "$name" >/dev/null 2>&1 || true
docker run -d --name "$name" -p "127.0.0.1:$port:80" "$image" >/dev/null
trap 'docker rm -f "$name" >/dev/null 2>&1 || true' EXIT

for _ in $(seq 30); do
  curl -fsS -o /dev/null "$base/healthz" 2>/dev/null && break
  sleep 1
done
health=$(curl -fsS "$base/healthz") || fail "/healthz does not answer"
[ "$health" = ok ] || fail "/healthz answered '$health', not 'ok'"

read -r code location < <(curl -sS -o /dev/null -w '%{http_code} %{redirect_url}\n' "$base/install")
[ "$code" = 302 ] || fail "/install answered $code, not 302"
[ "$location" = "$want" ] || fail "/install redirects to '$location', not $want"

page=$(mktemp)
code=$(curl -sS -o "$page" -w '%{http_code}' "$base/")
[ "$code" = 200 ] || fail "/ answered $code, not 200"
grep -qF 'curl -fsSL https://playkeeper.io/install | sudo sh' "$page" || fail "/ does not show the install command"
for f in style.css copy.js favicon.svg; do
  code=$(curl -sS -o /dev/null -w '%{http_code}' "$base/$f")
  [ "$code" = 200 ] || fail "/$f answered $code, not 200"
done

for path in / /install; do
  headers=$(curl -sS -D - -o /dev/null "$base$path")
  for h in "content-security-policy: default-src 'none'" 'x-content-type-options: nosniff' 'x-frame-options: DENY' \
    'referrer-policy: no-referrer' 'strict-transport-security: max-age='; do
    grep -qiF "$h" <<<"$headers" || fail "$path does not send '$h'"
  done
  if grep -qi '^server: nginx/' <<<"$headers"; then fail "$path shows the nginx version"; fi
done

status=unknown
for _ in $(seq 30); do
  status=$(docker inspect -f '{{.State.Health.Status}}' "$name")
  [ "$status" = healthy ] && break
  sleep 2
done
[ "$status" = healthy ] || fail "the container's health check reports '$status'"

echo "Site image checks out: / is 200 with the install command, /healthz is ok, /install is a 302 to $want, headers set, container healthy."
