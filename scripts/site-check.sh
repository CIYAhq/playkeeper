#!/usr/bin/env bash
# Builds the playkeeper.io image the way Coolify does (site/Dockerfile, from
# the repository root), runs it, and checks what it serves: every page in its
# sitemap and every page the launch needs (200, a title, a description, a
# canonical address, a social preview that answers, one <h1>, no inline
# script or style, and every file it uses served with the content type
# nosniff needs), /robots.txt, /sitemap.xml and the blog's feed, the share
# page for server templates at /t (kept out of search engines), a missing
# page's 404, an address ending in / sent to the page without it, the sizing
# guide's table at /sizing, the live demo at /demo/ (its page, its files, the
# players' faces and the plugins' icons, deep links answered by the app, a
# missing file still a 404, and that it is the demo build), /community,
# /healthz, the /install redirect to get.sh of the latest release, cache and
# security headers, and the container's own health check. With Chrome or
# Chromium installed, it also opens /sizing in headless Chrome with an answer
# in its address, and with one it can't read, and checks the answer the page
# shows; and it opens /t with the template links in
# internal/templates/testdata, and checks what the page shows and that the
# template never reaches the server. Needs Docker. The image stays, as
# playkeeper-site:check, for test/e2e/ui/demo.spec.ts.
# Usage: scripts/site-check.sh   (SITE_CHECK_PORT picks the local port, default 8080;
#                                 CHROME picks the browser)
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
image=playkeeper-site:check
name=playkeeper-site-check
port=${SITE_CHECK_PORT:-8080}
base=http://127.0.0.1:$port
site=https://playkeeper.io
want=https://github.com/CIYAhq/playkeeper/releases/latest/download/get.sh
community=https://github.com/CIYAhq/playkeeper/discussions
# The pages the launch needs, whether or not the sitemap lists them.
needed=(/ /features/mods-and-modpacks /alternatives/aternos /alternatives/pterodactyl /guides/modded-minecraft-server
  /sizing /docs /docs/install /pricing /blog /blog/playkeeper-0-4-0)
fail() {
  echo "site check failed: $*" >&2
  docker logs "$name" 2>&1 | tail -20 >&2 || true
  exit 1
}
headers_ok() { # PATH HEADERS
  local h
  for h in "content-security-policy: default-src 'none'" 'x-content-type-options: nosniff' 'x-frame-options: DENY' \
    'referrer-policy: no-referrer' 'strict-transport-security: max-age='; do
    grep -qiF "$h" <<<"$2" || fail "$1 does not send '$h'"
  done
  if grep -qi '^server: nginx/' <<<"$2"; then fail "$1 shows the nginx version"; fi
}
# check_files checks every file a page uses: each answers 200, with the
# content type browsers need under nosniff, kept for a year when it's under
# /assets/.
check_files() {
  local path=$1 file=$2 f code type want_type
  while read -r f; do
    read -r code type < <(curl -sS -o /dev/null -w '%{http_code} %{content_type}\n' "$base$f")
    [ "$code" = 200 ] || fail "$path uses $f, which answered $code, not 200"
    case $f in
      *.js) want_type=javascript ;;
      *.css) want_type=text/css ;;
      *.svg) want_type=image/svg+xml ;;
      *.webp) want_type=image/webp ;;
      *.png) want_type=image/png ;;
      *.xml) want_type=xml ;;
      *) continue ;;
    esac
    [[ $type == *"$want_type"* ]] || fail "$f is served as '$type'; with nosniff, browsers only use it as $want_type"
  done < <(grep -oE '(src|href|srcset|imagesrcset)="/[^"#?]*' "$file" | sed -E 's/^[a-z]+="//' | grep -vE '^/($|demo/|install$|community$|t$)' |
    grep -E '\.[a-z0-9]+$' | sort -u)
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
read -r code location < <(curl -sS -o /dev/null -w '%{http_code} %{redirect_url}\n' "$base/community")
[ "$code" = 302 ] || fail "/community answered $code, not 302"
[ "$location" = "$community" ] || fail "/community redirects to '$location', not $community"

read -r code type < <(curl -sS -o "$work/robots.txt" -w '%{http_code} %{content_type}\n' "$base/robots.txt")
[ "$code" = 200 ] || fail "/robots.txt answered $code"
for text in "Sitemap: $site/sitemap.xml" 'Allow: /demo/$' 'Disallow: /demo/'; do
  grep -qxF "$text" "$work/robots.txt" || fail "/robots.txt does not say '$text'"
done
read -r code type < <(curl -sS -o "$work/sitemap.xml" -w '%{http_code} %{content_type}\n' "$base/sitemap.xml")
[ "$code" = 200 ] || fail "/sitemap.xml answered $code"
[[ $type == *xml* ]] || fail "/sitemap.xml is served as '$type'"
read -r code type < <(curl -sS -o "$work/feed.xml" -w '%{http_code} %{content_type}\n' "$base/blog/feed.xml")
[ "$code" = 200 ] || fail "/blog/feed.xml answered $code"
grep -qF "<link rel=\"alternate\" type=\"text/html\" href=\"$site/blog/playkeeper-0-4-0\"/>" "$work/feed.xml" || fail "the blog's feed does not have the 0.4.0 post"

mapfile -t listed < <(sed -n 's|.*<loc>'"$site"'\(/[^<]*\)</loc>.*|\1|p' "$work/sitemap.xml")
for p in "${needed[@]}"; do
  printf '%s\n' "${listed[@]}" | grep -qxF "$p" || fail "the sitemap does not list $p"
done
if printf '%s\n' "${listed[@]}" | grep -qxE '/t|/404'; then fail "the sitemap lists /t or /404, which stay out of search engines"; fi

page=$work/page.html
checked=0
for p in "${listed[@]}"; do
  [ "$p" = /demo/ ] && continue
  read -r code type < <(curl -sS -o "$page" -w '%{http_code} %{content_type}\n' "$base$p")
  [ "$code" = 200 ] || fail "$p answered $code, not 200"
  [[ $type == text/html* ]] || fail "$p is served as '$type'"
  grep -qE '<title>[^<]{10,70}</title>' "$page" || fail "$p has no title of 10 to 70 characters"
  grep -qE '<meta name="description" content="[^"]{50,170}">' "$page" || fail "$p has no description of 50 to 170 characters"
  grep -qF "<link rel=\"canonical\" href=\"$site$p\">" "$page" || fail "$p does not name $site$p as its canonical address"
  for tag in 'property="og:title"' 'property="og:description"' "property=\"og:url\" content=\"$site$p\"" 'name="twitter:card" content="summary_large_image"'; do
    grep -qF "<meta $tag" "$page" || fail "$p has no <meta $tag"
  done
  og=$(sed -n 's|.*<meta property="og:image" content="'"$site"'\(/[^"]*\)">.*|\1|p' "$page")
  [ -n "$og" ] || fail "$p has no social preview image on $site"
  [ "$(curl -sS -o /dev/null -w '%{http_code}' "$base$og")" = 200 ] || fail "$p's social preview $og does not answer"
  [ "$(grep -o '<h1[ >]' "$page" | wc -l)" = 1 ] || fail "$p does not have exactly one <h1>"
  if sed 's|<script type="application/ld+json">[^<]*</script>||g' "$page" | grep -qE '<script>|<script [^s]|<style|[[:space:]](style|on[a-z]+)='; then
    fail "$p has inline script or style, which the Content-Security-Policy blocks"
  fi
  if grep -qE '<script src="https?:' "$page"; then fail "$p loads a script from another site"; fi
  check_files "$p" "$page"
  checked=$((checked + 1))
done
grep -qF 'curl -fsSL https://playkeeper.io/install | sudo sh' <(curl -sS "$base/") || fail "/ does not show the install command"

headers=$(curl -sS -D - -o /dev/null "$base/")
grep -qi '^cache-control: no-cache' <<<"$headers" || fail "/ can be cached, so a deploy would not reach visitors"
asset=$(grep -oE '/assets/css/site\.[0-9a-f]{8}\.css' <(curl -sS "$base/") | head -1)
[ -n "$asset" ] || fail "/ does not use a hashed stylesheet under /assets/"
headers=$(curl -sS -D - -o /dev/null "$base$asset")
grep -qi '^cache-control: max-age=31536000' <<<"$headers" || fail "$asset is not cached for a year"

read -r code location < <(curl -sS -o /dev/null -w '%{http_code} %{redirect_url}\n' "$base/pricing/")
[ "$code" = 301 ] || fail "/pricing/ answered $code, not 301"
[ "$location" = "$base/pricing" ] || fail "/pricing/ redirects to '$location', not /pricing"
code=$(curl -sS -o "$page" -w '%{http_code}' "$base/no-such-page")
[ "$code" = 404 ] || fail "a missing page answered $code, not 404"
grep -qF "This page isn't here" "$page" || fail "a missing page does not show the 404 page"
grep -qF '<meta name="robots" content="noindex">' "$page" || fail "the 404 page can be indexed"

# The sizing guide: its table works without JavaScript.
code=$(curl -sS -o "$page" -w '%{http_code}' "$base/sizing")
[ "$code" = 200 ] || fail "/sizing answered $code, not 200"
for text in 'How much RAM does a Minecraft server need?' 'Every size at a glance' '<td id="size-5-10-vanilla">' \
  'Playkeeper itself needs at least 2 CPU cores, 3 GB of memory and 5 GB of free disk.' \
  'curl -fsSL https://playkeeper.io/install | sudo sh' 'href="/demo/"'; do
  grep -qF "$text" "$page" || fail "/sizing does not show '$text'"
done
read -r code location < <(curl -sS -o /dev/null -w '%{http_code} %{redirect_url}\n' "$base/sizing/")
[ "$code" = 301 ] || fail "/sizing/ answered $code, not 301"
[ "$location" = "$base/sizing" ] || fail "/sizing/ redirects to '$location', not /sizing"

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
script=$(grep -oE '/demo/assets/index-[^"]+\.js' "$page" | head -1)
curl -fsS -o "$work/demo.js" "$base$script" || fail "$script does not download"
# The pages load in chunks, so the demo's code is in the entry or in a chunk it names.
demo=
for chunk in "$script" $(grep -oE '(\./|assets/)[A-Za-z0-9_-]+\.js' "$work/demo.js" | sed -E 's#^(\./|assets/)#/demo/assets/#' | sort -u); do
  curl -fsS -o "$work/chunk.js" "$base$chunk" || fail "$chunk does not download"
  if grep -qF 'playkeeper-live-demo' "$work/chunk.js"; then demo=$chunk; break; fi
done
[ -n "$demo" ] || fail "$script is not the demo build (vite build --mode demo)"
for deep in /demo/servers/survival/console /demo/settings/audit; do
  code=$(curl -sS -o "$work/deep.html" -w '%{http_code}' "$base$deep")
  [ "$code" = 200 ] || fail "$deep answered $code, not 200"
  cmp -s "$page" "$work/deep.html" || fail "$deep is not answered by the demo's page"
done
for drawing in /demo/faces/0.svg /demo/icons/0.svg; do
  read -r code type < <(curl -sS -o /dev/null -w '%{http_code} %{content_type}\n' "$base$drawing")
  [ "$code" = 200 ] || fail "$drawing answered $code, not 200"
  [[ $type == *image/svg+xml* ]] || fail "$drawing is served as '$type', not image/svg+xml"
done
code=$(curl -sS -o /dev/null -w '%{http_code}' "$base/demo/assets/missing.js")
[ "$code" = 404 ] || fail "/demo/assets/missing.js answered $code; a missing file must stay a 404"
headers=$(curl -sS -D - -o /dev/null "$base/demo/")
grep -qi '^cache-control: no-cache' <<<"$headers" || fail "/demo/ can be cached, so a deploy would not reach visitors"
headers_ok /demo/ "$headers"
headers=$(curl -sS -D - -o /dev/null "$base$script")
grep -qi '^cache-control: max-age=31536000' <<<"$headers" || fail "$script is not cached for a year"
headers_ok "$script" "$headers"

code=$(curl -sS -o "$page" -w '%{http_code}' "$base/t")
[ "$code" = 200 ] || fail "/t answered $code, not 200"
grep -qF '<meta name="robots" content="noindex">' "$page" || fail "/t can be indexed"
grep -qE '<script src="/assets/js/t\.[0-9a-f]{8}\.js" defer></script>' "$page" || fail "/t is not the share page"
[ "$(grep -o '<script src=' "$page" | wc -l)" = 2 ] || fail "/t loads more than site.js and t.js"
if grep -qF 'data-stars' "$page"; then fail "/t asks GitHub for the star count; the share page makes no requests"; fi
check_files /t "$page"

for path in / /pricing /t /install /robots.txt /no-such-page "$asset"; do
  headers_ok "$path" "$(curl -sS -D - -o /dev/null "$base$path")"
done

# The share page reads templates in the browser, so this part opens it in
# headless Chrome, when there is one.
chrome=${CHROME:-$(command -v google-chrome-stable || command -v google-chrome || command -v chromium || command -v chromium-browser || true)}
browser="Chrome was not found, so /sizing and /t were not opened in a browser (CHROME picks one)"
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
    'data-fit-label="">Fits your answer, 11–20 friends on a big modpack: 24 GB of memory, 6 fast cores<'; do
    grep -qF "$text" "$dom" || fail "/sizing#friends=11-20&run=modpack does not show '$text' in Chrome"
  done
  open_sizing 'friends=lots&run=everything'
  for text in 'id="answer-long">A VPS with 6 GB of memory<' '<td id="size-5-10-vanilla" class="current">'; do
    grep -qF "$text" "$dom" || fail "/sizing#friends=lots&run=everything does not fall back to the first answer ('$text' is missing)"
  done
  # open_share_page opens /t with the given data after # and leaves the page,
  # as it settles, in $dom. Chrome only opens this site's page here, so it
  # runs without its sandbox, which containers and some CI runners refuse.
  open_share_page() {
    $limit "$chrome" --headless=new --no-sandbox --disable-gpu --no-first-run --no-default-browser-check \
      --user-data-dir="$work/chrome" --virtual-time-budget=5000 --dump-dom "$base/t#$1" >"$dom" 2>/dev/null ||
      fail "headless Chrome ($chrome) could not open /t"
  }
  page_state() { sed -n 's/.*<body[^>]* data-state="\([a-z]*\)".*/\1/p' "$dom"; }
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
  for text in 'Survival with friends' 'Paper · Minecraft 26.2 · 4 GB of memory' 'Chunky, ViaVersion and ViaBackwards' \
    'Fresh Animations (resource pack) and Terralith (data pack)' 'data-art="friends" loading="lazy">'; do
    grep -qF "$text" "$dom" || fail "/t does not show '$text' for the Paper template"
  done
  if form_hidden; then fail "/t does not offer to open the Paper template"; fi
  grep -q '<p id="t-made"[^>]* hidden' "$dom" || fail "/t shows a day for a template that doesn't say one"
  # A template can name anyone as its author, so the page shows only the day.
  expect_state "$(link_data share-link-author.txt)" ready
  grep -qF 'Made 25 Sep 2026' "$dom" || fail "/t does not show the day the template was made"
  if grep -qF 'siya' "$dom"; then fail "/t shows the author the template names"; fi
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
  grep -qF 'This link has no template in it' "$dom" || fail "/t does not say the address has no template in it"
  expect_state "$(link_data share-link-markup.txt)" ready
  grep -qF '&lt;b&gt;Survival&lt;/b&gt; &amp; &lt;i&gt;friends&lt;/i&gt;' "$dom" || fail "/t does not show the template's name as text"
  if grep -qF '<b>Survival' "$dom"; then fail "/t turns markup in a template's name into HTML"; fi

  logs=$(docker logs "$name" 2>&1)
  grep -q '"GET /t HTTP/.*HeadlessChrome' <<<"$logs" || fail "Chrome's visits to /t are not in the server's log"
  for f in share-link.txt share-link-markup.txt share-link-author.txt share-link-oversized.txt; do
    data=$(link_data "$f")
    if grep -qF "${data:0:32}" <<<"$logs"; then fail "the template in $f reached the server"; fi
  done
  browser="Headless Chrome showed the answer in /sizing's address and the first answer for an address it can't read, read the template links on /t, and the templates never reached the server"
fi

status=unknown
for _ in $(seq 30); do
  status=$(docker inspect -f '{{.State.Health.Status}}' "$name")
  [ "$status" = healthy ] && break
  sleep 2
done
[ "$status" = healthy ] || fail "the container's health check reports '$status'"

echo "Site image checks out: $checked pages from the sitemap answer with their title, description, canonical address, social preview and files; robots.txt, the sitemap and the feed; /t is the share page and kept out of search engines; a missing page is a 404; /pricing/ redirects; /community, /install and /healthz answer; cache and security headers set; container healthy. $browser."
