#!/usr/bin/env bash
# Tests the one-line installer (packaging/get.sh) and the install.sh guards
# without network or root: curl, getconf, id and uname are stubs on PATH, and
# every run happens in a new session so there is no terminal to confirm on.
# Assertions are written "condition || fail ...": fail always exits.
# shellcheck disable=SC2015
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT
checks=0
fail() {
  echo "FAIL: $*" >&2
  exit 1
}
ok() {
  checks=$((checks + 1))
  echo "ok: $*"
}

# A fake release whose install.sh records the arguments it receives.
rel=playkeeper-0.0.0-test-linux-amd64
mkdir -p "$t/rel/$rel" "$t/tmp" "$t/bin"
cat >"$t/rel/$rel/install.sh" <<EOF
#!/bin/sh
printf '%s\n' "\$@" >"$t/ran.txt"
exit "\${FAKE_INSTALL_STATUS:-0}"
EOF
printf '#!/bin/sh\necho "playkeeper test"\n' >"$t/rel/$rel/playkeeper"
chmod +x "$t/rel/$rel/install.sh" "$t/rel/$rel/playkeeper"
asset=playkeeper-linux-amd64.tar.gz
publish() { # DIR ARCHIVE-SOURCE-DIR ENTRY... — writes the asset and its checksum
  publish_as "$asset" "$@"
}
publish_as() { # ASSET DIR ARCHIVE-SOURCE-DIR ENTRY...
  local name=$1 dir=$t/site/$2 src=$3
  shift 3
  mkdir -p "$dir"
  tar -czf "$dir/$name" -C "$src" "$@"
  (cd "$dir" && sha256sum "$name" >"$name.sha256")
}
publish good "$t/rel" "$rel"
publish bad "$t/rel" "$rel"
printf '%064d  %s\n' 0 "$asset" >"$t/site/bad/$asset.sha256"
publish nosum "$t/rel" "$rel"
rm "$t/site/nosum/$asset.sha256"
mkdir -p "$t/other/a" "$t/other/b"
publish layout "$t/other" a b

cat >"$t/bin/curl" <<'EOF'
#!/bin/sh
out='' url=''
while [ $# -gt 0 ]; do
  case $1 in
    -o) out=$2; shift ;;
    --proto | --proto-redir | --retry) shift ;;
    -*) ;;
    *) url=$1 ;;
  esac
  shift
done
echo "$url" >>"$SITE/../curl.log"
path=${url#*://example.test/}
[ -f "$SITE/$path" ] || exit 22
cp "$SITE/$path" "$out"
EOF
cat >"$t/bin/id" <<'EOF'
#!/bin/sh
echo "${FAKE_UID:-0}"
EOF
cat >"$t/bin/uname" <<'EOF'
#!/bin/sh
case $1 in -m) echo "${FAKE_ARCH:-x86_64}" ;; *) echo Linux ;; esac
EOF
cat >"$t/bin/getconf" <<'EOF'
#!/bin/sh
[ "$1" = LONG_BIT ] && echo "${FAKE_LONG_BIT:-64}"
EOF
chmod +x "$t/bin/curl" "$t/bin/id" "$t/bin/uname" "$t/bin/getconf"

out='' status=0
get() { # [VAR=value...] -- get.sh arguments...
  local vars=()
  while [ "$1" != -- ]; do
    vars+=("$1")
    shift
  done
  shift
  rm -f "$t/ran.txt" "$t/curl.log"
  status=0
  out=$(env PATH="$t/bin:$PATH" SITE="$t/site" TMPDIR="$t/tmp" "${vars[@]}" setsid -w sh "$root/packaging/get.sh" "$@" 2>&1) || status=$?
  [ -z "$(ls -A "$t/tmp")" ] || fail "temporary files left behind: $(ls "$t/tmp")"
}
ran() { [ -f "$t/ran.txt" ] && tr '\n' ' ' <"$t/ran.txt"; }

get PLAYKEEPER_BASE_URL=https://example.test/good -- --yes --game-port 25566
[ "$status" = 0 ] && [[ $out == *"SHA-256 verified"* ]] && [ "$(ran)" = "--yes --game-port 25566 " ] ||
  fail "verified download should run install.sh with the flags passed through ($status): $out"
ok "checksum verified, install.sh ran with the flags passed through"

get -- --base-url https://example.test/good --yes
[ "$status" = 0 ] && [ "$(ran)" = "--yes " ] || fail "--base-url must be consumed, not passed on ($status): $out"
get -- --base-url=https://example.test/good/ --yes
[ "$status" = 0 ] && [ "$(ran)" = "--yes " ] || fail "--base-url=URL/ must work ($status): $out"
ok "--base-url overrides the release location and is not passed to install.sh"

get PLAYKEEPER_BASE_URL=https://example.test/bad -- --yes
[ "$status" != 0 ] && [[ $out == *"does not match its published checksum"* ]] && [ ! -f "$t/ran.txt" ] ||
  fail "a checksum mismatch must stop before running anything ($status): $out"
ok "checksum mismatch refused; nothing from the download ran"

get PLAYKEEPER_BASE_URL=https://example.test/nosum -- --yes
[ "$status" != 0 ] && [[ $out == *"could not download"*".sha256"* ]] && [ ! -f "$t/ran.txt" ] ||
  fail "a missing checksum file must stop the install ($status): $out"
ok "missing checksum file refused"

get PLAYKEEPER_BASE_URL=https://example.test/layout -- --yes
[ "$status" != 0 ] && [[ $out == *"layout of a Playkeeper release"* ]] && [ ! -f "$t/ran.txt" ] ||
  fail "an archive that is not a Playkeeper release must be refused ($status): $out"
ok "archive with an unexpected layout refused"

get PLAYKEEPER_BASE_URL=http://example.test/good -- --yes
[ "$status" != 0 ] && [[ $out == *"is not HTTPS"* ]] && [ ! -f "$t/curl.log" ] || fail "plain HTTP must be refused ($status): $out"
get PLAYKEEPER_BASE_URL=http://example.test/good PLAYKEEPER_ALLOW_HTTP=1 -- --yes
[ "$status" = 0 ] && [ "$(ran)" = "--yes " ] || fail "PLAYKEEPER_ALLOW_HTTP=1 should allow a local HTTP mirror ($status): $out"
ok "plain HTTP refused unless PLAYKEEPER_ALLOW_HTTP=1"

get PLAYKEEPER_BASE_URL=https://example.test/good FAKE_UID=1000 -- --yes
[ "$status" != 0 ] && [[ $out == *"needs root"* ]] && [ ! -f "$t/curl.log" ] || fail "non-root must be refused before downloading ($status): $out"
ok "non-root refused before downloading"

for cpu in armv7l i686 riscv64; do
  get PLAYKEEPER_BASE_URL=https://example.test/good FAKE_ARCH=$cpu -- --yes
  [ "$status" != 0 ] && [[ $out == *"CPU is $cpu; Playkeeper runs on x86_64 (amd64) and 64-bit ARM (aarch64) servers."*"On a Raspberry Pi 4 or 5, install a 64-bit system."* ]] &&
    [ ! -f "$t/curl.log" ] || fail "a $cpu CPU must be refused before downloading ($status): $out"
done
ok "CPUs other than x86_64 and 64-bit ARM refused before downloading"

get PLAYKEEPER_BASE_URL=https://example.test/good FAKE_ARCH=aarch64 FAKE_LONG_BIT=32 -- --yes
[ "$status" != 0 ] && [[ $out == *"32-bit system on a 64-bit CPU"*"Install the 64-bit version"* ]] && [ ! -f "$t/curl.log" ] ||
  fail "a 32-bit system on a 64-bit CPU must be refused before downloading ($status): $out"
ok "a 32-bit system on a 64-bit ARM CPU refused before downloading"

# The arm64 release, next to the amd64 one as a GitHub release has them.
rel64=playkeeper-0.0.0-test-linux-arm64
mkdir -p "$t/rel64/$rel64"
cat >"$t/rel64/$rel64/install.sh" <<EOF
#!/bin/sh
printf '%s\n' arm64 "\$@" >"$t/ran.txt"
EOF
cp "$t/rel/$rel/playkeeper" "$t/rel64/$rel64/playkeeper"
chmod +x "$t/rel64/$rel64/install.sh"
publish both "$t/rel" "$rel"
publish_as playkeeper-linux-arm64.tar.gz both "$t/rel64" "$rel64"
for cpu in aarch64 arm64; do
  get PLAYKEEPER_BASE_URL=https://example.test/both FAKE_ARCH=$cpu -- --yes
  [ "$status" = 0 ] && [[ $out == *"Downloading playkeeper-linux-arm64.tar.gz"* ]] && [ "$(ran)" = "arm64 --yes " ] &&
    [ "$(cat "$t/curl.log")" = "https://example.test/both/playkeeper-linux-arm64.tar.gz.sha256
https://example.test/both/playkeeper-linux-arm64.tar.gz" ] || fail "a $cpu CPU must get the arm64 tarball ($status): $out"
done
get PLAYKEEPER_BASE_URL=https://example.test/both -- --yes
[ "$status" = 0 ] && [[ $out == *"Downloading playkeeper-linux-amd64.tar.gz"* ]] && [ "$(ran)" = "--yes " ] ||
  fail "an x86_64 CPU must get the amd64 tarball ($status): $out"
ok "each CPU downloads and runs its own tarball: arm64 on aarch64, amd64 on x86_64"

get PLAYKEEPER_BASE_URL=https://example.test/good FAKE_ARCH=aarch64 -- --yes
[ "$status" != 0 ] && [[ $out == *"could not download https://example.test/good/playkeeper-linux-arm64.tar.gz.sha256"* ]] && [ ! -f "$t/ran.txt" ] ||
  fail "a release without an arm64 tarball must stop the install on 64-bit ARM ($status): $out"
mkdir -p "$t/site/swapped"
cp "$t/site/good/playkeeper-linux-amd64.tar.gz" "$t/site/swapped/playkeeper-linux-arm64.tar.gz"
(cd "$t/site/swapped" && sha256sum playkeeper-linux-arm64.tar.gz >playkeeper-linux-arm64.tar.gz.sha256)
get PLAYKEEPER_BASE_URL=https://example.test/swapped FAKE_ARCH=aarch64 -- --yes
[ "$status" != 0 ] && [[ $out == *"layout of a Playkeeper release"* ]] && [ ! -f "$t/ran.txt" ] ||
  fail "an amd64 build under the arm64 name must be refused ($status): $out"
ok "no arm64 tarball, or an amd64 build under its name, stops the install on 64-bit ARM"

get PLAYKEEPER_BASE_URL=https://example.test/good --
[ "$status" != 0 ] && [[ $out == *"no terminal to answer"* ]] && [ ! -f "$t/curl.log" ] ||
  fail "without a terminal or --yes the script must stop before downloading ($status): $out"
ok "no terminal and no --yes: stops before downloading and explains --yes"

get PLAYKEEPER_BASE_URL=https://example.test/good FAKE_INSTALL_STATUS=3 -- --yes
[ "$status" = 3 ] || fail "the installer's exit status must be passed on (got $status): $out"
ok "installer exit status passed on"

# install.sh runs the playkeeper next to it only when it is built for this
# CPU: byte 18 of an ELF file names it, 62 for x86_64 and 183 for AArch64.
# These fakes are shell scripts with that byte in a comment, so they still run.
build() { # DIR OCTAL-BYTE
  mkdir -p "$1"
  cp "$root/packaging/install.sh" "$1/install.sh"
  {
    printf '#!/bin/sh\n#abcdefg'
    printf '%b' "\\0$2"
    printf '\nprintf "%%s\\n" "$@" >"%s/ran.txt"\n' "$t"
  } >"$1/playkeeper"
  chmod +x "$1/install.sh" "$1/playkeeper"
}
build "$t/builds/amd64" 076
build "$t/builds/arm64" 267
install_sh() { # DIR [VAR=value...]
  local dir=$1
  shift
  rm -f "$t/ran.txt"
  status=0
  out=$(env PATH="$t/bin:$PATH" "$@" sh "$dir/install.sh" --yes 2>&1) || status=$?
}
install_sh "$t/builds/arm64" FAKE_ARCH=aarch64
[ "$status" = 0 ] && [ "$(ran)" = "install --yes " ] || fail "install.sh must run the arm64 build on a 64-bit ARM CPU ($status): $out"
install_sh "$t/builds/amd64" FAKE_ARCH=x86_64
[ "$status" = 0 ] && [ "$(ran)" = "install --yes " ] || fail "install.sh must run the amd64 build on an x86_64 CPU ($status): $out"
ok "install.sh runs the build for this CPU, passing its flags on"
install_sh "$t/builds/amd64" FAKE_ARCH=aarch64
[ "$status" != 0 ] && [[ $out == *"for another CPU; this server's is aarch64."*"download playkeeper-linux-arm64.tar.gz instead."* ]] && [ ! -f "$t/ran.txt" ] ||
  fail "install.sh must refuse the amd64 build on a 64-bit ARM CPU ($status): $out"
install_sh "$t/builds/arm64" FAKE_ARCH=x86_64
[ "$status" != 0 ] && [[ $out == *"for another CPU; this server's is x86_64."*"download playkeeper-linux-amd64.tar.gz instead."* ]] && [ ! -f "$t/ran.txt" ] ||
  fail "install.sh must refuse the arm64 build on an x86_64 CPU ($status): $out"
ok "install.sh refuses another CPU's build and names the tarball to download"
install_sh "$t/builds/arm64" FAKE_ARCH=armv7l
[ "$status" != 0 ] && [[ $out == *"CPU is armv7l; Playkeeper runs on x86_64 (amd64) and 64-bit ARM (aarch64) servers."*"install a 64-bit system."* ]] &&
  [ ! -f "$t/ran.txt" ] || fail "install.sh must refuse a 32-bit ARM CPU with a fix ($status): $out"
ok "install.sh refuses other CPUs with a fix"

# Follow-ups after 0.3.0.
publish othername "$t/rel" "$rel"
(cd "$t/site/othername" && printf '%s  %s\n' "$(sha256sum "$asset" | cut -d' ' -f1)" playkeeper-linux-arm64.tar.gz >"$asset.sha256")
publish unnamed "$t/rel" "$rel"
(cd "$t/site/unnamed" && sha256sum "$asset" | cut -d' ' -f1 >"$asset.sha256")
publish oddname "$t/rel" "$rel"
(cd "$t/site/oddname" && printf '%s  %s\033[2J\n' "$(sha256sum "$asset" | cut -d' ' -f1)" "$asset" >"$asset.sha256")
publish binary "$t/rel" "$rel"
(cd "$t/site/binary" && sha256sum -b "$asset" >"$asset.sha256")

get PLAYKEEPER_BASE_URL=https://example.test/othername -- --yes
[ "$status" != 0 ] && [[ $out == *"$asset.sha256 is the checksum of playkeeper-linux-arm64.tar.gz, not of $asset."* ]] &&
  ! grep -qxF "https://example.test/othername/$asset" "$t/curl.log" && [ ! -f "$t/ran.txt" ] ||
  fail "a .sha256 that names another file must stop before the download ($status): $out"
get PLAYKEEPER_BASE_URL=https://example.test/unnamed -- --yes
[ "$status" != 0 ] && [[ $out == *"$asset.sha256 does not name the file it is the checksum of."* ]] && [ ! -f "$t/ran.txt" ] ||
  fail "a .sha256 without a file name must stop the install ($status): $out"
get PLAYKEEPER_BASE_URL=https://example.test/oddname -- --yes
[ "$status" != 0 ] && [[ $out == *"$asset.sha256 is not the checksum of $asset."* ]] && [[ $out != *$'\033'* ]] && [ ! -f "$t/ran.txt" ] ||
  fail "a .sha256 naming a file with control characters must stop the install without printing them ($status): $out"
ok ".sha256 naming another file, or no file, refused before the download"

get PLAYKEEPER_BASE_URL=https://example.test/binary -- --yes
[ "$status" = 0 ] && [ "$(ran)" = "--yes " ] || fail "a .sha256 from sha256sum -b (\"*name\") should be accepted ($status): $out"
ok ".sha256 in the form sha256sum -b writes accepted"

# From here curl applies --max-filesize before writing anything, as curl does
# when the server announces the size. With FAKE_OLD_CURL=1 it writes the whole
# file, as curl before 8.4 does when the server announces no size.
cat >"$t/bin/curl" <<'EOF'
#!/bin/sh
out='' url='' max=''
while [ $# -gt 0 ]; do
  case $1 in
    -o) out=$2; shift ;;
    --max-filesize) max=$2; shift ;;
    --proto | --proto-redir | --retry) shift ;;
    -*) ;;
    *) url=$1 ;;
  esac
  shift
done
echo "$url" >>"$SITE/../curl.log"
echo "$url ${max:-none}" >>"$SITE/../limits.log"
path=${url#*://example.test/}
[ -f "$SITE/$path" ] || exit 22
if [ -n "$max" ] && [ "${FAKE_OLD_CURL:-}" != 1 ] && [ "$(wc -c <"$SITE/$path")" -gt "$max" ]; then
  exit 63
fi
cp "$SITE/$path" "$out"
EOF
publish big "$t/rel" "$rel"
(cd "$t/site/big" && truncate -s 201M "$asset" && sha256sum "$asset" >"$asset.sha256")
publish bigsum "$t/rel" "$rel"
truncate -s 2M "$t/site/bigsum/$asset.sha256"

rm -f "$t/limits.log"
get PLAYKEEPER_BASE_URL=https://example.test/good -- --yes
[ "$status" = 0 ] && [ "$(cat "$t/limits.log")" = "https://example.test/good/$asset.sha256 1048576
https://example.test/good/$asset 209715200" ] ||
  fail "every download must be limited, the .sha256 to 1 MB and the tarball to 200 MB ($status): $(cat "$t/limits.log")"
ok "every download is size-limited: 1 MB for the .sha256, 200 MB for the tarball"

for old in '' 1; do
  get PLAYKEEPER_BASE_URL=https://example.test/big FAKE_OLD_CURL=$old -- --yes
  [ "$status" != 0 ] && [[ $out == *"https://example.test/big/$asset is larger than 200 MB"*"Nothing was installed."* ]] &&
    [[ $out != *"SHA-256 verified"* ]] && [ ! -f "$t/ran.txt" ] ||
    fail "a tarball over 200 MB must be refused before it is verified or run (FAKE_OLD_CURL=$old, status $status): $out"
  get PLAYKEEPER_BASE_URL=https://example.test/bigsum FAKE_OLD_CURL=$old -- --yes
  [ "$status" != 0 ] && [[ $out == *"https://example.test/bigsum/$asset.sha256 is larger than 1 MB"*"Nothing was installed."* ]] &&
    ! grep -qxF "https://example.test/bigsum/$asset" "$t/curl.log" && [ ! -f "$t/ran.txt" ] ||
    fail "a .sha256 over 1 MB must be refused before the tarball is downloaded (FAKE_OLD_CURL=$old, status $status): $out"
done
ok "a tarball over 200 MB or a .sha256 over 1 MB refused before anything is installed, also when curl cannot stop it early"

# In a terminal, install.sh takes get.sh's place (the same process), so that
# it is sudo's own child when it asks, and deletes the download itself.
mkdir -p "$t/asks/$rel"
cat >"$t/asks/$rel/install.sh" <<EOF
#!/bin/sh
{ echo "pid \$\$"; echo "dir \$PLAYKEEPER_GET_DIR"; [ -t 0 ] && echo "stdin is a terminal"; } >"$t/asks.txt"
case \$PLAYKEEPER_GET_DIR in "$t"/tmp/playkeeper-get.*) rm -rf "\$PLAYKEEPER_GET_DIR" ;; esac
EOF
cp "$t/rel/$rel/playkeeper" "$t/asks/$rel/"
chmod +x "$t/asks/$rel/install.sh"
publish asks "$t/asks" "$rel"
status=0
out=$(script -qec "sh -c 'echo \$\$ >\"$t/get.pid\"; exec env PATH=\"$t/bin:$PATH\" SITE=\"$t/site\" TMPDIR=\"$t/tmp\" PLAYKEEPER_BASE_URL=https://example.test/asks sh \"$root/packaging/get.sh\"'" /dev/null 2>&1) || status=$?
[ "$status" = 0 ] && grep -qx "pid $(cat "$t/get.pid")" "$t/asks.txt" && grep -qx "stdin is a terminal" "$t/asks.txt" &&
  grep -qx "dir $t/tmp/playkeeper-get\..*" "$t/asks.txt" && [ -z "$(ls -A "$t/tmp")" ] ||
  fail "in a terminal, install.sh must replace get.sh, read the terminal and get the download to delete ($status): $out $(cat "$t/asks.txt" 2>/dev/null)"
ok "in a terminal, install.sh takes get.sh's place, asks on the terminal and deletes the download"

echo "get.sh and install.sh: $checks checks passed"
