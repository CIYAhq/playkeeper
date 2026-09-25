#!/usr/bin/env bash
# Builds the playkeeper.io image the way Coolify does (site/Dockerfile, from
# the repository root), runs it, and checks what it serves: / (the page and
# the install command), /sizing (the sizing guide, its table and the /sizing/
# redirect), /demo/ (the live demo: its page, its files, the players' faces,
# deep links answered by the app, a missing file still a 404, and that it is
# the demo build), every file those pages use (200, with the content type
# nosniff needs), /healthz, the /install redirect to get.sh of the latest
# release, the security headers on each of them, and the container's own
# health check. With Chrome or Chromium installed, it also opens /sizing in
# headless Chrome with an answer in its address, and with one it can't read,
# and checks the answer the page shows. Needs Docker. The image stays, as
# playkeeper-site:check, for test/e2e/ui/demo.spec.ts.
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
check_files() {
  local path=$1 file=$2 f code type want_type
  while read -r f; do
    read -r code type < <(curl -sS -o /dev/null -w '%{http_code} %{content_type}\n' "$base/$f")
    [ "$code" = 200 ] || fail "$path uses /$f, which answered $code, not 200"
    case $f in
      *.js) want_type=javascript ;;
      *.css) want_type=text/css ;;
      *.svg) want_type=image/svg+xml ;;
      *) continue ;;
    esac
    [[ $type == *"$want_type"* ]] || fail "/$f is served as '$type'; with nosniff, browsers only use it as $want_type"
  done < <(grep -oE '(src|href)="/?[^"#:?/][^"#:?]*"' "$file" | sed -E 's/^(src|href)="\/?//; s/"$//' | sort -u)
}

docker build -f "$root/site/Dockerfile" -t "$image" "$root"
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
grep -qF 'href="/sizing"' "$page" || fail "/ does not link to the sizing guide"
check_files / "$page"

code=$(curl -sS -o "$page" -w '%{http_code}' "$base/sizing")
[ "$code" = 200 ] || fail "/sizing answered $code, not 200"
for text in 'How big a VPS do you need?' 'Every size at a glance' '<td id="size-5-10-vanilla">' \
  'Playkeeper itself needs at least 2 CPU cores, 3 GB of memory and 5 GB of free disk.' \
  'curl -fsSL https://playkeeper.io/install | sudo sh' 'href="/#install"'; do
  grep -qF "$text" "$page" || fail "/sizing does not show '$text'"
done
if grep -qE '<script>|<style|[[:space:]](style|on[a-z]+)=' "$page"; then
  fail "/sizing has inline script or style, which the Content-Security-Policy blocks"
fi
read -r code location < <(curl -sS -o /dev/null -w '%{http_code} %{redirect_url}\n' "$base/sizing/")
[ "$code" = 301 ] || fail "/sizing/ answered $code, not 301"
[ "$location" = "$base/sizing" ] || fail "/sizing/ redirects to '$location', not /sizing"
check_files /sizing "$page"

# The live demo: the dashboard, built with its sample data, under /demo/.
read -r code location < <(curl -sS -o /dev/null -w '%{http_code} %{redirect_url}\n' "$base/demo")
[ "$code" = 301 ] || fail "/demo answered $code, not 301"
[ "$location" = "$base/demo/" ] || fail "/demo redirects to '$location', not /demo/"
code=$(curl -sS -o "$page" -w '%{http_code}' "$base/demo/")
[ "$code" = 200 ] || fail "/demo/ answered $code, not 200"
grep -qF '<div id="root">' "$page" || fail "/demo/ is not the dashboard"
grep -qE 'src="/demo/assets/index-[^"]+\.js"' "$page" || fail "/demo/ does not load its script from /demo/assets/"
if grep -qE '<script>|<style|[[:space:]](style|on[a-z]+)=' "$page"; then
  fail "/demo/ has inline script or style, which the Content-Security-Policy blocks"
fi
check_files /demo/ "$page"
script=$(grep -oE '/demo/assets/index-[^"]+\.js' "$page" | head -1)
curl -fsS -o "$work/demo.js" "$base$script" || fail "$script does not download"
grep -qF 'playkeeper-live-demo' "$work/demo.js" || fail "$script is not the demo build (vite build --mode demo)"
for deep in /demo/servers/survival/console /demo/settings/audit; do
  code=$(curl -sS -o "$work/deep.html" -w '%{http_code}' "$base$deep")
  [ "$code" = 200 ] || fail "$deep answered $code, not 200"
  cmp -s "$page" "$work/deep.html" || fail "$deep is not answered by the demo's page"
done
read -r code type < <(curl -sS -o /dev/null -w '%{http_code} %{content_type}\n' "$base/demo/faces/0.svg")
[ "$code" = 200 ] || fail "/demo/faces/0.svg answered $code, not 200"
[[ $type == *image/svg+xml* ]] || fail "/demo/faces/0.svg is served as '$type', not image/svg+xml"
code=$(curl -sS -o /dev/null -w '%{http_code}' "$base/demo/assets/missing.js")
[ "$code" = 404 ] || fail "/demo/assets/missing.js answered $code; a missing file must stay a 404"
headers=$(curl -sS -D - -o /dev/null "$base/demo/")
grep -qi '^cache-control: no-cache' <<<"$headers" || fail "/demo/ can be cached, so a deploy would not reach visitors"
headers=$(curl -sS -D - -o /dev/null "$base$script")
grep -qi '^cache-control: max-age=31536000' <<<"$headers" || fail "$script is not cached for a year"

for path in / /sizing /install /demo/ /demo/servers/survival "$script" /demo/faces/0.svg; do
  headers=$(curl -sS -D - -o /dev/null "$base$path")
  for h in "content-security-policy: default-src 'none'" 'x-content-type-options: nosniff' 'x-frame-options: DENY' \
    'referrer-policy: no-referrer' 'strict-transport-security: max-age='; do
    grep -qiF "$h" <<<"$headers" || fail "$path does not send '$h'"
  done
  if grep -qi '^server: nginx/' <<<"$headers"; then fail "$path shows the nginx version"; fi
done

# The sizing guide answers in the browser, so this part opens it in headless
# Chrome, when there is one.
chrome=${CHROME:-$(command -v google-chrome-stable || command -v google-chrome || command -v chromium || command -v chromium-browser || true)}
browser="Chrome was not found, so /sizing was not opened in a browser (CHROME picks one)"
if [ -n "$chrome" ]; then
  limit=
  if command -v timeout >/dev/null; then limit="timeout 60"; fi
  dom=$work/dom.html
  # open_sizing opens /sizing with the given address after # and leaves the
  # page, once its scripts have run, in $dom. Chrome only opens this site's
  # page here, so it runs without its sandbox, which containers and some CI
  # runners refuse.
  open_sizing() {
    $limit "$chrome" --headless=new --no-sandbox --disable-gpu --no-first-run --no-default-browser-check \
      --user-data-dir="$work/chrome" --virtual-time-budget=5000 --dump-dom "$base/sizing#$1" >"$dom" 2>/dev/null ||
      fail "headless Chrome ($chrome) could not open /sizing"
  }
  open_sizing 'friends=11-20&run=modpack'
  for text in 'id="answer-long">A VPS with 24 GB of memory<' '<dt>Memory</dt><dd><strong>24 GB</strong>' \
    'id="run-current">A big modpack<' '<td id="size-11-20-modpack" class="current">' '<td id="size-5-10-vanilla">' \
    'id="answer-status"></p>'; do
    grep -qF "$text" "$dom" || fail "/sizing#friends=11-20&run=modpack does not show '$text' in Chrome"
  done
  if grep -qF 'id="copy" hidden' "$dom"; then fail "/sizing does not offer Copy in Chrome"; fi
  open_sizing 'friends=lots&run=everything'
  for text in 'id="answer-long">A VPS with 6 GB of memory<' '<td id="size-5-10-vanilla" class="current">'; do
    grep -qF "$text" "$dom" || fail "/sizing#friends=lots&run=everything does not fall back to the first answer ('$text' is missing)"
  done
  browser="Headless Chrome showed the answer in /sizing's address, and the first answer for an address it can't read"
fi

status=unknown
for _ in $(seq 30); do
  status=$(docker inspect -f '{{.State.Health.Status}}' "$name")
  [ "$status" = healthy ] && break
  sleep 2
done
[ "$status" = healthy ] || fail "the container's health check reports '$status'"

echo "Site image checks out: / is 200 with the install command, /sizing is 200 with the guide and its files, /demo/ is the live demo with its files and deep links, /healthz is ok, /install is a 302 to $want, headers set, container healthy. $browser."
