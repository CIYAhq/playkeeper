#!/usr/bin/env bash
# Builds the names service image (services/names/Dockerfile, from the
# repository root) and runs it with a dummy Cloudflare token, with
# api.cloudflare.com pointed at the container itself so nothing is sent
# anywhere. Checks what it serves: /healthz, / (the source code link),
# /v1/ip, availability from its database and that unsigned changes are
# refused; then that it runs as a non-root user who can write /data, has CA
# certificates and wget (Coolify's health check), logs no error and no token,
# reports healthy and stops cleanly; last, that it refuses to start with a
# plain http:// alert webhook and names the setting without showing its
# value. Needs Docker.
# Usage: scripts/names-check.sh   (NAMES_CHECK_PORT picks the local port, default 8081)
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
image=playkeeper-names:check
name=playkeeper-names-check
port=${NAMES_CHECK_PORT:-8081}
base=http://127.0.0.1:$port
token=names-check-dummy-token-not-a-secret
out=$(mktemp)
fail() {
  echo "names check failed: $*" >&2
  docker logs "$name" 2>&1 | tail -20 >&2 || true
  exit 1
}

docker build -f "$root/services/names/Dockerfile" -t "$image" "$root"
docker rm -f "$name" >/dev/null 2>&1 || true
docker run -d --name "$name" -p "127.0.0.1:$port:8080" --add-host api.cloudflare.com:127.0.0.1 \
  -e NAMES_CLOUDFLARE_API_TOKEN="$token" -e NAMES_CLOUDFLARE_ZONE_ID=0123456789abcdef0123456789abcdef \
  "$image" >/dev/null
trap 'docker rm -f "$name" >/dev/null 2>&1 || true; rm -f "$out"' EXIT

for _ in $(seq 30); do
  curl -fsS -o /dev/null "$base/healthz" 2>/dev/null && break
  sleep 1
done
health=$(curl -fsS "$base/healthz") || fail "/healthz does not answer"
[ "$health" = ok ] || fail "/healthz answered '$health', not 'ok'"

page=$(curl -fsS "$base/") || fail "/ does not answer"
grep -qF 'Source code (AGPL-3.0): https://github.com/CIYAhq/playkeeper' <<<"$page" || fail "/ does not link the source code"

ip=$(curl -fsS "$base/v1/ip") || fail "/v1/ip does not answer"
grep -qE '^\{"ip":"[0-9a-f.:]+","family":"ipv[46]","public":false\}$' <<<"$ip" || fail "/v1/ip answered '$ip'"

avail=$(curl -fsS "$base/v1/names/www") || fail "/v1/names/www does not answer"
grep -qF '"available":false,"code":"name_reserved"' <<<"$avail" || fail "www is not reported as reserved: $avail"
avail=$(curl -fsS "$base/v1/names/names-check") || fail "/v1/names/names-check does not answer"
grep -qF '"available":true' <<<"$avail" || fail "a free name is not reported as free: $avail"

code=$(curl -sS -o "$out" -w '%{http_code}' -X PUT "$base/v1/names/names-check") || fail "PUT /v1/names/names-check does not answer"
[ "$code" = 401 ] || fail "an unsigned claim answered $code, not 401"
grep -qF '"code":"unsigned"' "$out" || fail "an unsigned claim was not refused as unsigned: $(cat "$out")"

status=unknown
for _ in $(seq 30); do
  status=$(docker inspect -f '{{.State.Health.Status}}' "$name")
  [ "$status" = healthy ] && break
  sleep 2
done
[ "$status" = healthy ] || fail "the container's health check reports '$status'"

uid=$(docker exec "$name" awk '/^Uid:/ {print $2}' /proc/1/status) || fail "cannot read the service's user"
[ "$uid" != 0 ] || fail "the service runs as root"
docker exec "$name" sh -c 'test -s /data/names.db && ls /data/backups/names-*.db' >/dev/null ||
  fail "the service did not write its database and first snapshot to /data"
docker exec "$name" test -s /etc/ssl/certs/ca-certificates.crt || fail "the image has no CA certificates, so it cannot reach Cloudflare"
docker exec "$name" wget -q -O /dev/null http://127.0.0.1:8080/healthz || fail "wget in the image cannot fetch /healthz"

logs=$(docker logs "$name" 2>&1)
grep -qF 'Could not reach Cloudflare to check the zone' <<<"$logs" || fail "the service did not say that Cloudflare is out of reach"
if grep -q 'level=ERROR' <<<"$logs"; then fail "the service logged an error"; fi
if grep -qF "$token" <<<"$logs"; then fail "the log shows the Cloudflare token"; fi

docker stop "$name" >/dev/null
exit_code=$(docker inspect -f '{{.State.ExitCode}}' "$name")
[ "$exit_code" = 0 ] || fail "the service exited with $exit_code when stopped, not 0"

hook_secret=names-check-dummy-hook-not-a-secret
if docker run --rm --network none -e NAMES_CLOUDFLARE_API_TOKEN="$token" -e NAMES_CLOUDFLARE_ZONE_ID=0123456789abcdef0123456789abcdef \
  -e NAMES_ALERT_WEBHOOK_URL="http://discord.com/api/webhooks/1/$hook_secret" "$image" >"$out" 2>&1; then
  fail "the service started with a plain http:// NAMES_ALERT_WEBHOOK_URL"
fi
grep -qF NAMES_ALERT_WEBHOOK_URL "$out" || fail "refusing a plain http:// webhook does not name NAMES_ALERT_WEBHOOK_URL: $(cat "$out")"
if grep -qF "$hook_secret" "$out"; then fail "refusing a plain http:// webhook shows its URL"; fi

echo "Names image checks out: /healthz is ok, / links the source, /v1/ip and availability answer, unsigned claims get 401, runs as uid $uid and writes /data, no error or token in the log, healthy, stops cleanly, refuses a plain http:// webhook without showing it."
