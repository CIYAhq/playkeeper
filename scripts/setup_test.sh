#!/usr/bin/env bash
# Tests scripts/setup.sh without network: a copy runs in a temporary checkout
# whose pins name fake Go and Node toolchains, with curl and uname stand-ins on
# PATH. With python3 and curl 7.71 or later, the real curl also downloads them
# from a loopback server that cuts the first response to each file short.
# Assertions are written "condition || fail ...": fail always exits.
# shellcheck disable=SC2015
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
t=$(mktemp -d)
srv=''
trap 'if [ -n "$srv" ]; then kill "$srv" 2>/dev/null || true; fi; rm -rf "$t"' EXIT
checks=0
fail() {
  echo "FAIL: $*" >&2
  exit 1
}
ok() {
  checks=$((checks + 1))
  echo "ok: $*"
}

# Fake toolchains, packed like the real ones, that answer what setup.sh asks.
go='go0.0.1.linux-amd64.tar.gz'
node='node-v0.0.2-linux-x64'
mkdir -p "$t/pack/go/bin" "$t/pack/$node/bin" "$t/site" "$t/bin"
cat >"$t/pack/go/bin/go" <<'EOF'
#!/bin/sh
case $1 in env) echo go0.0.1 ;; version) echo "go version go0.0.1" ;; esac
EOF
cat >"$t/pack/$node/bin/node" <<'EOF'
#!/bin/sh
echo v0.0.2
EOF
cat >"$t/pack/$node/bin/npm" <<'EOF'
#!/bin/sh
[ "$1" != --version ] || echo 0.0.3
EOF
chmod +x "$t/pack/go/bin/go" "$t/pack/$node/bin/node" "$t/pack/$node/bin/npm"
tar -czf "$t/site/$go" -C "$t/pack" go
tar -cJf "$t/site/$node.tar.xz" -C "$t/pack" "$node"

cat >"$t/bin/uname" <<'EOF'
#!/bin/sh
echo x86_64
EOF
cat >"$t/bin/curl" <<'EOF'
#!/usr/bin/env bash
# CURL_OLD=1 acts like curl before 7.71. Downloads are copied from $T/site, or
# with SERVE set, made by the real curl ($REAL_CURL) from that server.
old=${CURL_OLD:-0}
if [ "$1" = --help ]; then
  echo "     --retry <num> Retry request if transient problems occur"
  [ "$old" = 1 ] || echo "     --retry-all-errors Retry all errors (use with --retry)"
  exit 0
fi
echo "$*" >>"$T/curl.log"
args=("$@") out='' url=''
for ((i = 0; i < ${#args[@]}; i++)); do
  case ${args[i]} in
    --retry-all-errors)
      if [ "$old" = 1 ]; then
        echo "curl: option --retry-all-errors: is unknown" >&2
        exit 2
      fi
      ;;
    -o) out=${args[i + 1]} ;;
    https://*)
      url=${args[i]}
      args[i]=${SERVE:-}/${url##*/}
      ;;
  esac
done
[ -n "${SERVE:-}" ] || exec cp "$T/site/${url##*/}" "$out"
exec "$REAL_CURL" "${args[@]}"
EOF
chmod +x "$t/bin/uname" "$t/bin/curl"

checkout() { # a fresh checkout: setup.sh, pins for the fake toolchains, web/
  rm -rf "$t/co"
  mkdir -p "$t/co/scripts" "$t/co/web"
  cp "$root/scripts/setup.sh" "$t/co/scripts/"
  {
    printf 'go 0.0.1\nnode 0.0.2\n'
    (cd "$t/site" && sha256sum "$go" "$node.tar.xz")
  } >"$t/co/scripts/toolchains.txt"
}
status=0 out=''
setup() { # [VAR=value...] — runs the checkout's setup.sh
  : >"$t/curl.log"
  status=0
  out=$(env PATH="$t/bin:$PATH" T="$t" "$@" bash "$t/co/scripts/setup.sh" 2>&1) || status=$?
}
downloads() { # PATTERN — how many curl calls had PATTERN in their arguments
  grep -c -e "$1" "$t/curl.log" || true
}

checkout
setup
[ "$status" = 0 ] && [[ $out == *"Setup complete."* ]] || fail "setup should succeed ($status): $out"
[ -x "$t/co/.tools/go/bin/go" ] && [ -x "$t/co/.tools/node/bin/node" ] || fail "setup should install both toolchains: $out"
[ "$(downloads ' --retry 3 --retry-all-errors ')" = 2 ] || fail "both downloads should retry on any error: $(cat "$t/curl.log")"
ok "with curl 7.71 or later, both downloads retry on any error"

checkout
setup CURL_OLD=1
[ "$status" = 0 ] && [[ $out == *"Setup complete."* ]] || fail "setup should work with curl before 7.71 ($status): $out"
[ "$(downloads ' --retry 3 ')" = 2 ] && [ "$(downloads 'retry-all-errors')" = 0 ] ||
  fail "with curl before 7.71, both downloads should use --retry 3 alone: $(cat "$t/curl.log")"
ok "with curl before 7.71, setup still works and downloads use --retry 3"

dropped() {
  cat >"$t/serve.py" <<'PY'
import http.server
import os
import sys


class Handler(http.server.BaseHTTPRequestHandler):
    """Serves files from argv[1]; the first response to each path stops a
    third of the way through, after its headers promised the whole file."""

    protocol_version = "HTTP/1.1"
    seen = set()

    def do_GET(self):
        with open(os.path.join(sys.argv[1], os.path.basename(self.path)), "rb") as f:
            body = f.read()
        first = self.path not in Handler.seen
        Handler.seen.add(self.path)
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body[: len(body) // 3] if first else body)
        self.close_connection = first

    def log_message(self, *args):
        pass


server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
with open(sys.argv[2] + ".tmp", "w") as f:
    f.write(str(server.server_address[1]))
os.replace(sys.argv[2] + ".tmp", sys.argv[2])
server.serve_forever()
PY
  python3 "$t/serve.py" "$t/site" "$t/port" &
  srv=$!
  for _ in $(seq 50); do
    [ -s "$t/port" ] && break
    sleep 0.1
  done
  [ -s "$t/port" ] || fail "the loopback server did not start"
  local base real
  base=http://127.0.0.1:$(cat "$t/port")
  real=$(command -v curl)

  checkout
  setup REAL_CURL="$real" SERVE="$base/new"
  [ "$status" = 0 ] && [[ $out == *"Setup complete."* ]] || fail "setup should get past downloads cut short ($status): $out"
  ok "downloads cut short are retried and setup completes"

  checkout
  setup REAL_CURL="$real" SERVE="$base/old" CURL_OLD=1
  [ "$status" != 0 ] && [[ $out == *"curl: (18)"* ]] || fail "with --retry alone, a download cut short should stop setup ($status): $out"
  ok "with --retry alone, the same cut stops setup"
}
if command -v python3 >/dev/null 2>&1 && [[ $(curl --help all 2>/dev/null) == *--retry-all-errors* ]]; then
  dropped
else
  echo "skip: downloads cut short (needs python3 and curl 7.71 or later)"
fi

echo "setup.sh: $checks checks passed"
