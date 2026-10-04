#!/usr/bin/env bash
# Builds the stats service image (services/stats/Dockerfile, from the
# repository root) and runs it with a dummy read token, trusting Docker's
# bridge as its proxy. Checks what it serves: /healthz, / (what installs send,
# and the source code link), the dashboard's page and files with the
# Content-Security-Policy that keeps it to them, reports taken and refused,
# a copy of the install command taken from playkeeper.io and refused from
# anywhere else, the counts refused without the token and right with it, day
# by day and in the funnel too,
# `playkeeper-stats summary` in the
# container; then that it runs as a non-root user who can write /data, that
# neither its files nor its log hold the address a report came from, its user
# agent or the token, that it reports healthy and stops cleanly; last, that
# it refuses to start with a read token that is too short and names the
# setting without showing its value. Needs Docker.
# Usage: scripts/stats-check.sh   (STATS_CHECK_PORT picks the local port, default 8082)
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
image=playkeeper-stats:check
name=playkeeper-stats-check
port=${STATS_CHECK_PORT:-8082}
base=http://127.0.0.1:$port
token=stats-check-dummy-read-token-not-a-secret
client=203.0.113.77
agent="playkeeper/0.4.4 stats-check-agent"
out=$(mktemp)
copy=$(mktemp -d)
fail() {
  echo "stats check failed: $*" >&2
  docker logs "$name" 2>&1 | tail -20 >&2 || true
  exit 1
}

docker build -f "$root/services/stats/Dockerfile" -t "$image" "$root"
docker rm -f "$name" >/dev/null 2>&1 || true
# Requests from this machine reach the container from the bridge's gateway,
# which stands in for Coolify's proxy here.
gateway=$(docker network inspect bridge --format '{{range .IPAM.Config}}{{.Gateway}}{{end}}')
[ -n "$gateway" ] || fail "cannot find the address of Docker's bridge"
docker run -d --name "$name" -p "127.0.0.1:$port:8080" -e STATS_READ_TOKEN="$token" -e STATS_TRUSTED_PROXIES="$gateway" "$image" >/dev/null
trap 'docker rm -f "$name" >/dev/null 2>&1 || true; rm -rf "$out" "$copy"' EXIT

for _ in $(seq 30); do
  curl -fsS -o /dev/null "$base/healthz" 2>/dev/null && break
  sleep 1
done
health=$(curl -fsS "$base/healthz") || fail "/healthz does not answer"
[ "$health" = ok ] || fail "/healthz answered '$health', not 'ok'"

page=$(curl -fsS "$base/") || fail "/ does not answer"
grep -qF 'Source code (AGPL-3.0): https://github.com/CIYAhq/playkeeper' <<<"$page" || fail "/ does not link the source code"
grep -qF 'https://github.com/CIYAhq/playkeeper#usage-stats' <<<"$page" || fail "/ does not say where what installs send is described"

headers=$(curl -fsS -D - -o "$out" "$base/dashboard") || fail "/dashboard does not answer"
grep -qi '^content-type: text/html' <<<"$headers" || fail "/dashboard is not a page: $headers"
grep -qi "^content-security-policy: default-src 'none'; script-src 'self'" <<<"$headers" ||
  fail "/dashboard has no Content-Security-Policy that keeps it to its own files: $headers"
grep -qF 'src="/dashboard/app.js"' "$out" || fail "/dashboard does not load its script"
for file in app.js app.css icon.svg; do
  curl -fsS -o /dev/null "$base/dashboard/$file" || fail "/dashboard/$file does not answer"
done

id=0123456789abcdef0123456789abcdef
system='"version":"0.4.4","os":"ubuntu","osVersion":"24.04","arch":"amd64","source":"playkeeper.io","kind":"dashboard"'
report() { # PATH JSON — the status the service answered
  curl -sS -o "$out" -w '%{http_code}' -X POST -H 'Content-Type: application/json' -H "X-Forwarded-For: $client" -A "$agent" \
    --data "$2" "$base$1"
}
code=$(report /v1/install "{\"id\":\"$id\",\"event\":\"started\",$system}") || fail "POST /v1/install does not answer"
[ "$code" = 204 ] || fail "an install's first report answered $code: $(cat "$out")"
code=$(report /v1/install "{\"id\":\"$id\",\"event\":\"succeeded\",$system}")
[ "$code" = 204 ] || fail "an install's last report answered $code: $(cat "$out")"
code=$(report /v1/heartbeat "{\"id\":\"$id\",$system,\"address\":\"free\",\"servers\":2,\"running\":1,\"reached\":\"played\",\"hostname\":\"alice-vps\"}")
[ "$code" = 204 ] || fail "a heartbeat answered $code: $(cat "$out")"
code=$(report /v1/heartbeat "{\"id\":\"$id\",$system,\"address\":\"alice.example.com\",\"servers\":2,\"running\":1}")
[ "$code" = 400 ] || fail "a heartbeat with an address instead of its kind answered $code, not 400"
grep -qF '"code":"invalid_request"' "$out" || fail "an invalid heartbeat was not refused as invalid: $(cat "$out")"
if grep -qF alice "$out"; then fail "the refusal repeats the value it refused"; fi
copy() { # ORIGIN — the status the service answered a copy of the install command
  curl -sS -o "$out" -w '%{http_code}' -X POST -H 'Content-Type: text/plain;charset=UTF-8' -H "Origin: $1" -H "X-Forwarded-For: $client" -A "$agent" \
    --data '{"event":"install_copied","channel":""}' "$base/v1/site"
}
code=$(copy https://playkeeper.io) || fail "POST /v1/site does not answer"
[ "$code" = 204 ] || fail "a copy of the install command from playkeeper.io answered $code: $(cat "$out")"
code=$(copy https://elsewhere.example)
[ "$code" = 403 ] || fail "a copy from another site answered $code, not 403"

code=$(curl -sS -o "$out" -w '%{http_code}' "$base/v1/summary")
[ "$code" = 401 ] || fail "the counts without the token answered $code, not 401"
code=$(curl -sS -o "$out" -w '%{http_code}' -H "Authorization: Bearer $token" "$base/v1/summary")
[ "$code" = 200 ] || fail "the counts with the token answered $code, not 200"
python3 - "$out" "$id" <<'EOF' || fail "the counts are wrong: $(cat "$out")"
import json, sys
s = json.load(open(sys.argv[1]))
assert sys.argv[2] not in open(sys.argv[1]).read(), "the counts show an install ID"
assert s["installs"]["1d"]["started"] == 1 and s["installs"]["1d"]["succeeded"] == 1, s["installs"]["1d"]
a = s["active"]["1d"]
assert a["installs"] == 1 and a["onOurDomain"] == 1 and a["servers"] == 2 and a["running"] == 1, a
assert a["byAddress"] == {"free": 1}, a["byAddress"]
assert a["byReached"] == {"played": 1}, a["byReached"]
assert s["test"] == {"started30d": 0, "active7d": 0}, s["test"]
f = s["funnel"]["1d"]
assert f == {"visitors": None, "demoOpens": None, "commandCopies": 1, "started": 1, "succeeded": 1, "stillRunning": 1}, f
assert s["site"] == {"configured": False}, s["site"]
d = s["daily"][-1]
assert d["started"] == 1 and d["succeeded"] == 1 and d["active"] == 1, d
assert d["bySource"] == {"playkeeper.io": {"started": 1, "succeeded": 1, "failed": 0, "refused": 0}}, d
EOF
docker exec "$name" playkeeper-stats summary >"$out" || fail "playkeeper-stats summary does not run in the container"
grep -qF '"onOurDomain": 1' "$out" || fail "playkeeper-stats summary does not count the install: $(cat "$out")"

status=unknown
for _ in $(seq 30); do
  status=$(docker inspect -f '{{.State.Health.Status}}' "$name")
  [ "$status" = healthy ] && break
  sleep 2
done
[ "$status" = healthy ] || fail "the container's health check reports '$status'"

uid=$(docker exec "$name" awk '/^Uid:/ {print $2}' /proc/1/status) || fail "cannot read the service's user"
[ "$uid" != 0 ] || fail "the service runs as root"
docker exec "$name" sh -c 'test -s /data/stats.db && ls /data/backups/stats-*.db' >/dev/null ||
  fail "the service did not write its database and first snapshot to /data"
docker exec "$name" wget -q -O /dev/null http://127.0.0.1:8080/healthz || fail "wget in the image cannot fetch /healthz"

docker cp "$name:/data/." "$copy/"
for secret in "$client" "stats-check-agent" "$token" alice; do
  if grep -rqF "$secret" "$copy"; then fail "the service's files hold '$secret'"; fi
done
logs=$(docker logs "$name" 2>&1)
if grep -q 'level=ERROR' <<<"$logs"; then fail "the service logged an error"; fi
for secret in "$client" "stats-check-agent" "$token"; do
  if grep -qF "$secret" <<<"$logs"; then fail "the service's log shows '$secret'"; fi
done

docker stop "$name" >/dev/null
exit_code=$(docker inspect -f '{{.State.ExitCode}}' "$name")
[ "$exit_code" = 0 ] || fail "the service exited with $exit_code when stopped, not 0"

short=too-short-a-token
if docker run --rm --network none -e STATS_READ_TOKEN="$short" "$image" >"$out" 2>&1; then
  fail "the service started with a read token that is too short"
fi
grep -qF STATS_READ_TOKEN "$out" || fail "refusing a short read token does not name STATS_READ_TOKEN: $(cat "$out")"
if grep -qF "$short" "$out"; then fail "refusing a short read token shows it"; fi

echo "Stats image checks out: /healthz is ok, / says what installs send and links the source, the dashboard serves its page and files with its Content-Security-Policy, reports taken and invalid ones refused, copies counted from playkeeper.io alone, the counts need the token and are right, day by day and in the funnel too, summary runs in the container, runs as uid $uid and writes /data, no client address, user agent or token in its files or log, healthy, stops cleanly, refuses a short read token without showing it."
