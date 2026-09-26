#!/usr/bin/env bash
# Builds the playkeeper.io image from site/, runs it, and checks what it
# serves: / (the page, its files and the install command), /healthz, the
# /install redirect to get.sh of the latest release, the share page for
# server templates at /t, the security headers, and the container's own
# health check. With Chrome or Chromium installed, it also opens /t in
# headless Chrome with the template links in internal/templates/testdata,
# and checks what the page shows and that the template never reaches the
# server. Needs Docker.
# Usage: scripts/site-check.sh   (SITE_CHECK_PORT picks the local port, default 8080;
#                                 CHROME picks the browser)
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
work=$(mktemp -d)
trap 'docker rm -f "$name" >/dev/null 2>&1 || true; rm -rf "$work"' EXIT

for _ in $(seq 30); do
  curl -fsS -o /dev/null "$base/healthz" 2>/dev/null && break
  sleep 1
done
health=$(curl -fsS "$base/healthz") || fail "/healthz does not answer"
[ "$health" = ok ] || fail "/healthz answered '$health', not 'ok'"

read -r code location < <(curl -sS -o /dev/null -w '%{http_code} %{redirect_url}\n' "$base/install")
[ "$code" = 302 ] || fail "/install answered $code, not 302"
[ "$location" = "$want" ] || fail "/install redirects to '$location', not $want"

page=$work/page.html
code=$(curl -sS -o "$page" -w '%{http_code}' "$base/")
[ "$code" = 200 ] || fail "/ answered $code, not 200"
grep -qF 'curl -fsSL https://playkeeper.io/install | sudo sh' "$page" || fail "/ does not show the install command"
for f in style.css copy.js favicon.svg t.js; do
  code=$(curl -sS -o /dev/null -w '%{http_code}' "$base/$f")
  [ "$code" = 200 ] || fail "/$f answered $code, not 200"
done

code=$(curl -sS -o "$page" -w '%{http_code}' "$base/t")
[ "$code" = 200 ] || fail "/t answered $code, not 200"
grep -qF '<script src="t.js" defer></script>' "$page" || fail "/t is not the share page"
type=$(curl -sS -o /dev/null -w '%{content_type}' "$base/t.js")
[[ $type == application/javascript* || $type == text/javascript* ]] || fail "/t.js is served as '$type', which browsers do not run"

for path in / /install /t; do
  headers=$(curl -sS -D - -o /dev/null "$base$path")
  for h in "content-security-policy: default-src 'none'" 'x-content-type-options: nosniff' 'x-frame-options: DENY' \
    'referrer-policy: no-referrer' 'strict-transport-security: max-age='; do
    grep -qiF "$h" <<<"$headers" || fail "$path does not send '$h'"
  done
  if grep -qi '^server: nginx/' <<<"$headers"; then fail "$path shows the nginx version"; fi
done

# The share page reads templates in the browser, so this part opens it in
# headless Chrome, when there is one.
chrome=${CHROME:-$(command -v google-chrome-stable || command -v google-chrome || command -v chromium || command -v chromium-browser || true)}
browser="Chrome was not found, so /t was not opened in a browser (CHROME picks one)"
if [ -n "$chrome" ]; then
  limit=
  if command -v timeout >/dev/null; then limit="timeout 60"; fi
  dom=$work/dom.html
  # open_share_page opens /t with the given data after # and leaves the page,
  # as it settles, in $dom. Chrome only opens this site's page here, so it
  # runs without its sandbox, which containers and some CI runners refuse.
  open_share_page() {
    $limit "$chrome" --headless=new --no-sandbox --disable-gpu --no-first-run --no-default-browser-check \
      --user-data-dir="$work/chrome" --virtual-time-budget=5000 --dump-dom "$base/t#$1" >"$dom" 2>/dev/null ||
      fail "headless Chrome ($chrome) could not open /t"
  }
  page_state() { sed -n 's/.*<body data-state="\([a-z]*\)".*/\1/p' "$dom"; }
  expect_state() {
    open_share_page "$1"
    [ "$(page_state)" = "$2" ] || fail "/t#${1:0:24}… shows the state '$(page_state)', not '$2'"
  }
  form_hidden() { grep -q '<form id="open"[^>]* hidden' "$dom"; }
  # link_data is what follows # in one of the links the Go tests keep.
  link_data() {
    local link
    link=$(cat "$root/internal/templates/testdata/$1")
    printf '%s' "${link#*#}"
  }

  payload=$(link_data share-link.txt)
  expect_state "$payload" ready
  for text in 'Survival with friends' 'Paper, Minecraft 26.2' 'Chunky, ViaVersion and ViaBackwards' \
    'Fresh Animations (resource pack) and Terralith (data pack)'; do
    grep -qF "$text" "$dom" || fail "/t does not show '$text' for the Paper template"
  done
  if form_hidden; then fail "/t does not offer to open the Paper template"; fi
  expect_state "template=$payload" ready
  expect_state "$payload)." ready
  expect_state "${payload:0:500}%20${payload:500}" ready
  expect_state "${payload:0:200}" incomplete
  swapped=B
  if [ "${payload:600:1}" = B ]; then swapped=C; fi
  expect_state "${payload:0:600}$swapped${payload:601}" damaged
  expect_state "Ag${payload:2}" newer
  expect_state "$(link_data share-link-oversized.txt)" damaged
  expect_state "" empty
  form_hidden || fail "/t offers to open an address with no template in it"
  expect_state "$(link_data share-link-markup.txt)" ready
  grep -qF '&lt;b&gt;Survival&lt;/b&gt; &amp; &lt;i&gt;friends&lt;/i&gt;' "$dom" || fail "/t does not show the template's name as text"
  if grep -qF '<b>Survival' "$dom"; then fail "/t turns markup in a template's name into HTML"; fi

  logs=$(docker logs "$name" 2>&1)
  grep -q '"GET /t HTTP/.*HeadlessChrome' <<<"$logs" || fail "Chrome's visits to /t are not in the server's log"
  for f in share-link.txt share-link-markup.txt share-link-oversized.txt; do
    data=$(link_data "$f")
    if grep -qF "${data:0:32}" <<<"$logs"; then fail "the template in $f reached the server"; fi
  done
  browser="Headless Chrome read the template links on /t, and the templates never reached the server"
fi

status=unknown
for _ in $(seq 30); do
  status=$(docker inspect -f '{{.State.Health.Status}}' "$name")
  [ "$status" = healthy ] && break
  sleep 2
done
[ "$status" = healthy ] || fail "the container's health check reports '$status'"

echo "Site image checks out: / is 200 with the install command, /healthz is ok, /install is a 302 to $want, /t is the share page, headers set, container healthy. $browser."
