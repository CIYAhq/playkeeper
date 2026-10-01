#!/usr/bin/env bash
# Tests scripts/package-cache.sh with stand-ins for dpkg-query and dpkg-deb on
# PATH: a stand-in .deb holds the "package version architecture" it is for.
# keep leaves only the .deb files of packages installed at their versions,
# deletes apt's lock and partial downloads, names what's left by the files'
# names whatever their order, and fails rather than leave nothing.
# Assertions are written "condition || fail ...": fail always exits.
# shellcheck disable=SC2015
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT
checks=0
fail() {
  echo "FAIL: $*"
  exit 1
}
ok() {
  checks=$((checks + 1))
  echo "ok: $*"
}

mkdir -p "$t/bin"
cat >"$t/bin/dpkg-query" <<'EOF'
#!/bin/sh
cat "$T/status"
EOF
cat >"$t/bin/dpkg-deb" <<'EOF'
#!/bin/sh
for last; do :; done
cat "$last"
EOF
chmod +x "$t/bin/dpkg-query" "$t/bin/dpkg-deb"

# status LINE...: what dpkg-query says is on the system, as "ii pkg version arch"
status() { printf '%s\n' "$@" >"$t/status"; }
# deb FILE "PKG VERSION ARCH": a stand-in .deb in the cache folder
deb() { printf '%s' "$2" >"$t/cache/$1"; }
fresh() {
  rm -rf "$t/cache"
  mkdir -p "$t/cache/partial"
  : >"$t/cache/lock"
  : >"$t/cache/partial/mesa-libgallium_25.2.8_amd64.deb"
}
keep() { # sets st, out (the name printed) and left (the files left, sorted)
  st=0
  out=$(PATH="$t/bin:$PATH" T="$t" "$root/scripts/package-cache.sh" keep "$t/cache" 2>"$t/err") || st=$?
  left=$(cd "$t/cache" && find . -mindepth 1 -printf '%P\n' | LC_ALL=C sort | tr '\n' ' ')
}

status 'ii fonts-freefont-ttf 20211204+svn4273-2 all' 'ii libgbm1 25.2.8-0ubuntu0.24.04.3 amd64' 'ii tzdata 1:2024a-3 all' 'rc qemu-utils 1:8.2.2 amd64'
fresh
deb fonts-freefont-ttf_20211204+svn4273-2_all.deb 'fonts-freefont-ttf 20211204+svn4273-2 all'
deb libgbm1_25.2.8-0ubuntu0.24.04.3_amd64.deb 'libgbm1 25.2.8-0ubuntu0.24.04.3 amd64'
deb libgbm1_25.2.8-0ubuntu0.24.04.1_amd64.deb 'libgbm1 25.2.8-0ubuntu0.24.04.1 amd64'
deb tzdata_1%3a2024a-3_all.deb 'tzdata 1:2024a-3 all'
deb qemu-utils_1%3a8.2.2_amd64.deb 'qemu-utils 1:8.2.2 amd64'
deb nmap_7.94_amd64.deb 'nmap 7.94 amd64'
keep
[ "$st" = 0 ] || fail "keep failed (status $st): $(cat "$t/err")"
[ "$left" = "fonts-freefont-ttf_20211204+svn4273-2_all.deb libgbm1_25.2.8-0ubuntu0.24.04.3_amd64.deb tzdata_1%3a2024a-3_all.deb " ] ||
  fail "keep must leave the installed versions' files and nothing else, left: $left"
ok "keep leaves the installed versions and deletes older ones, removed packages, others, the lock and partial downloads"
want=$(printf '%s\n' fonts-freefont-ttf_20211204+svn4273-2_all.deb libgbm1_25.2.8-0ubuntu0.24.04.3_amd64.deb tzdata_1%3a2024a-3_all.deb | sha256sum | cut -c1-16)
[ "$out" = "$want" ] || fail "the name must be the start of the SHA-256 of the sorted file names: got '$out', want '$want'"
case $(cat "$t/err") in *"kept 3 packages"*) ;; *) fail "keep must say what it kept: $(cat "$t/err")" ;; esac
ok "the set is named by its files' names"

first=$out
fresh
deb tzdata_1%3a2024a-3_all.deb 'tzdata 1:2024a-3 all'
deb libgbm1_25.2.8-0ubuntu0.24.04.3_amd64.deb 'libgbm1 25.2.8-0ubuntu0.24.04.3 amd64'
deb fonts-freefont-ttf_20211204+svn4273-2_all.deb 'fonts-freefont-ttf 20211204+svn4273-2 all'
keep
[ "$st" = 0 ] && [ "$out" = "$first" ] || fail "the same packages, made in another order, must get the same name (status $st): '$out', not '$first'"
ok "the same packages get the same name"

fresh
deb libgbm1_25.2.8-0ubuntu0.24.04.4_amd64.deb 'libgbm1 25.2.8-0ubuntu0.24.04.4 amd64'
deb fonts-freefont-ttf_20211204+svn4273-2_all.deb 'fonts-freefont-ttf 20211204+svn4273-2 all'
status 'ii fonts-freefont-ttf 20211204+svn4273-2 all' 'ii libgbm1 25.2.8-0ubuntu0.24.04.4 amd64'
keep
[ "$st" = 0 ] && [ "$out" != "$first" ] || fail "a new version must give the set a new name (status $st): '$out'"
ok "a new version gives the set a new name"

fresh
deb nmap_7.94_amd64.deb 'nmap 7.94 amd64'
keep
[ "$st" = 1 ] && [ -z "$out" ] || fail "keep must fail, naming nothing, when no installed package's file is left (status $st, printed '$out')"
case $(cat "$t/err") in *"does apt keep its packages there"*) ;; *) fail "keep must say why it failed: $(cat "$t/err")" ;; esac
ok "nothing left fails rather than name an empty set"

st=0
"$root/scripts/package-cache.sh" >/dev/null 2>&1 || st=$?
[ "$st" = 2 ] || fail "no arguments must be a usage error (status $st)"
ok "no arguments is a usage error"

echo "package-cache.sh: $checks checks passed"
