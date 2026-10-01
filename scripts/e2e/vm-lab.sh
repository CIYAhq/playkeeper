#!/usr/bin/env bash
# Disposable KVM lab for Playkeeper rehearsals: fresh guests from official
# cloud images (Ubuntu 24.04 by default, or any supported system's, lab_os_url)
# on a private bridge (198.51.100.0/24, a documentation range) with NAT to the
# internet. Source this file; it defines functions only.
#
# Requires: qemu-system-x86_64 with /dev/kvm, qemu-img, cloud-localds, iproute2,
# iptables, sudo. Guests are throwaway; nothing touches real hosts.
# LAB_ACCEL=tcg emulates the CPU instead of using KVM: many times slower, for
# machines whose KVM can't create a virtual CPU.

LAB_DIR=${LAB_DIR:-/tmp/pk-lab}
LAB_IMAGE_URL=${LAB_IMAGE_URL:-https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img}
LAB_BIONIC_URL=${LAB_BIONIC_URL:-https://cloud-images.ubuntu.com/bionic/current/bionic-server-cloudimg-amd64.img}
LAB_BRIDGE=pkbr0
LAB_NET=198.51.100
LAB_KEY="$LAB_DIR/id_ed25519"
LAB_QEMU_START_TIMEOUT=${LAB_QEMU_START_TIMEOUT:-60}
LAB_ACCEL=${LAB_ACCEL:-kvm}
# LAB_SSH_WAIT is how many 2-second tries a guest gets to answer ssh.
LAB_SSH_WAIT=${LAB_SSH_WAIT:-90}

lab_log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }

lab_key() {
  mkdir -p "$LAB_DIR"
  [ -f "$LAB_KEY" ] || ssh-keygen -q -t ed25519 -N '' -f "$LAB_KEY"
}

lab_image() {
  lab_image_named base "$LAB_IMAGE_URL"
  lab_key
}

# lab_os_url OS — the official cloud image of a supported release
# (internal/platform), by the name the OS matrix gives it.
lab_os_url() {
  local v=${1##*-} dir name
  case $1 in
    ubuntu-20.04) echo https://cloud-images.ubuntu.com/focal/current/focal-server-cloudimg-amd64.img ;;
    ubuntu-22.04) echo https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img ;;
    ubuntu-24.04) echo https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img ;;
    ubuntu-26.04) echo https://cloud-images.ubuntu.com/resolute/current/resolute-server-cloudimg-amd64.img ;;
    debian-12) echo https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-genericcloud-amd64.qcow2 ;;
    debian-13) echo https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-amd64.qcow2 ;;
    almalinux-9 | almalinux-10) echo "https://repo.almalinux.org/almalinux/$v/cloud/x86_64/images/AlmaLinux-$v-GenericCloud-latest.x86_64.qcow2" ;;
    rocky-9 | rocky-10) echo "https://dl.rockylinux.org/pub/rocky/$v/images/x86_64/Rocky-$v-GenericCloud-Base.latest.x86_64.qcow2" ;;
    centos-stream-9 | centos-stream-10) lab_centos_url "$v" ;;
    # Oracle keeps earlier updates' images, so this one stays (lab_os_sums).
    oraclelinux-9) echo https://yum.oracle.com/templates/OracleLinux/OL9/u8/x86_64/OL9U8_x86_64-kvm-b293.qcow2 ;;
    amazonlinux-2023)
      # The latest image is named only in the checksum list next to it.
      dir=https://cdn.amazonlinux.com/al2023/os-images/latest/kvm
      name=$(curl -fsSL --retry 3 "$dir/SHA256SUMS" | awk '/-x86_64\.xfs\.gpt\.qcow2$/ {print $2; exit}')
      if [ -z "$name" ]; then
        lab_log "no x86_64 image in $dir/SHA256SUMS"
        return 1
      fi
      echo "$dir/$name"
      ;;
    *)
      lab_log "no cloud image for $1"
      return 1
      ;;
  esac
}

# lab_centos_url V — CentOS Stream V's cloud image. CentOS publishes it on
# cloud.centos.org, but the copy there can break: on 29 Sep 2026 the latest
# image and its CHECKSUM line went missing for hours. Then it comes from the
# compose it was built in, on CentOS's compose server, whose checksum sits
# next to it (lab_os_sums).
lab_centos_url() {
  local v=$1 dir name sums compose list
  dir="https://cloud.centos.org/centos/$v-stream/x86_64/images"
  name="CentOS-Stream-GenericCloud-$v-latest.x86_64.qcow2"
  if sums=$(curl -fsSL --retry 3 "$dir/CHECKSUM" 2>/dev/null) && grep -qF "($name)" <<<"$sums" &&
    curl -fsSL --retry 3 -r 0-0 -o /dev/null "$dir/$name" 2>/dev/null; then
    echo "$dir/$name"
    return
  fi
  compose=https://composes.stream.centos.org/production/latest-CentOS-Stream/compose/BaseOS/x86_64/images
  [ "$v" = 9 ] || compose="https://composes.stream.centos.org/stream-$v/production/latest-CentOS-Stream/compose/BaseOS/x86_64/images"
  list=$(curl -fsSL --retry 3 "$compose/" 2>/dev/null) || list=
  # A listing's line names the image twice, in its link and its text.
  name=$(grep -m1 -oE "CentOS-Stream-GenericCloud-$v-[0-9.]+\.x86_64\.qcow2" <<<"$list" || true)
  name=${name%%$'\n'*}
  if [ -z "$name" ]; then
    lab_log "no CentOS Stream $v cloud image on cloud.centos.org or in $compose"
    return 1
  fi
  lab_log "cloud.centos.org can't serve CentOS Stream $v's image; using $name from its compose"
  echo "$compose/$name"
}

# lab_os_sums OS URL — where the checksum of OS's cloud image at URL, as
# lab_os_url gave it, is published, for lab_image_named: nothing for a
# SHA256SUMS or SHA512SUMS next to the image. It takes the URL because asking
# lab_os_url again can land on the other host for CentOS Stream.
lab_os_sums() {
  case $1 in
    almalinux-*) echo "$(dirname "$2")/CHECKSUM" ;;
    centos-stream-*)
      case $2 in
        https://composes.stream.centos.org/*) echo "$2.SHA256SUM" ;;
        *) echo "$(dirname "$2")/CHECKSUM" ;;
      esac
      ;;
    rocky-*) echo "$2.CHECKSUM" ;;
    # Oracle lists it only on https://yum.oracle.com/oracle-linux-templates.html.
    oraclelinux-9) echo sha256:b12103391327abee8090686759c0d62dac9a7af2bf0f45fdf6b0d085a0fbb52b ;;
  esac
}

# lab_image_named NAME URL [SUMS] — downloads a cloud image to $LAB_DIR/NAME.img
# once, verified against SUMS: the URL of a checksum list in GNU or BSD format,
# or sha256:HEX where the publisher lists the checksum only on a web page.
# Without SUMS it uses the SHA256SUMS or SHA512SUMS published next to the
# image, as Ubuntu and Debian do.
lab_image_named() {
  local name=$1 url=$2 sums=${3:-} file want got list sum=sha256sum
  file=$(basename "$url")
  mkdir -p "$LAB_DIR"
  [ -f "$LAB_DIR/$name.img" ] && return 0
  lab_log "downloading $file"
  curl -fsSL --retry 3 -o "$LAB_DIR/$name.img.part" "$url"
  case $sums in
    sha256:*) want=${sums#sha256:} ;;
    ?*)
      curl -fsSL --retry 3 -o "$LAB_DIR/$name.sums" "$sums"
      want=$(lab_sum "$file" "$LAB_DIR/$name.sums")
      ;;
    *)
      for list in SHA256SUMS SHA512SUMS; do
        curl -fsSL --retry 3 -o "$LAB_DIR/$name.sums" "$(dirname "$url")/$list" 2>/dev/null && break
        rm -f "$LAB_DIR/$name.sums"
      done
      [ "$list" = SHA512SUMS ] && sum=sha512sum
      want=$(lab_sum "$file" "$LAB_DIR/$name.sums" 2>/dev/null || true)
      ;;
  esac
  got=$($sum "$LAB_DIR/$name.img.part" | awk '{print $1}')
  if [ -z "$want" ] || [ "$want" != "$got" ]; then
    lab_log "cloud image checksum mismatch for $file (want '$want', got $got)"
    return 1
  fi
  mv "$LAB_DIR/$name.img.part" "$LAB_DIR/$name.img"
}

# lab_sum FILE LIST — FILE's checksum in LIST: "HEX  FILE" or "HEX *FILE"
# (GNU), or "SHA256 (FILE) = HEX" (BSD).
lab_sum() {
  awk -v f="$1" '$2 == "*" f || $2 == f {print $1} $1 ~ /^SHA(256|512)$/ && $2 == "(" f ")" {print $4}' "$2" | head -1
}

lab_network() {
  if ! ip link show "$LAB_BRIDGE" >/dev/null 2>&1; then
    sudo ip link add "$LAB_BRIDGE" type bridge
    sudo ip addr add "$LAB_NET.1/24" dev "$LAB_BRIDGE"
    sudo ip link set "$LAB_BRIDGE" up
  fi
  sudo sysctl -qw net.ipv4.ip_forward=1
  local out
  out=$(ip route show default | awk '{print $5; exit}')
  sudo iptables -t nat -C POSTROUTING -s "$LAB_NET.0/24" -o "$out" -j MASQUERADE 2>/dev/null ||
    sudo iptables -t nat -A POSTROUTING -s "$LAB_NET.0/24" -o "$out" -j MASQUERADE
  sudo iptables -C FORWARD -i "$LAB_BRIDGE" -j ACCEPT 2>/dev/null || sudo iptables -I FORWARD -i "$LAB_BRIDGE" -j ACCEPT
  sudo iptables -C FORWARD -o "$LAB_BRIDGE" -j ACCEPT 2>/dev/null || sudo iptables -I FORWARD -o "$LAB_BRIDGE" -j ACCEPT
  if command -v iptables-legacy >/dev/null 2>&1; then
    sudo iptables-legacy -P FORWARD ACCEPT 2>/dev/null || true
  fi
}

# lab_boot NAME OCTET MEMORY_MB — boots a fresh guest at $LAB_NET.OCTET from
# $LAB_BASE_IMAGE (default: the Ubuntu 24.04 image). NAME becomes part of the
# host name and the tap device's name, so keep it short, without dots.
lab_boot() {
  local name=$1 octet=$2 mem=${3:-3072} dir="$LAB_DIR/$1" dns base=${LAB_BASE_IMAGE:-$LAB_DIR/base.img} size
  # A resolver on the host's loopback (systemd-resolved's 127.0.0.53) is out
  # of the guests' reach: they get the servers it forwards to.
  dns=$(awk '/^nameserver/ && $2 !~ /^127\./ {print $2; exit}' /etc/resolv.conf /run/systemd/resolve/resolv.conf 2>/dev/null || true)
  dns=${dns:-1.1.1.1}
  rm -rf "$dir"
  mkdir -p "$dir"
  # 20 GB, or the image's own disk where that is larger (Oracle Linux's is
  # 37 GB): a smaller disk would cut off its partitions.
  size=$(qemu-img info --output=json "$base" | python3 -c 'import json, sys; print(max(20 * 2**30, json.load(sys.stdin)["virtual-size"]))')
  qemu-img create -q -f qcow2 -F qcow2 -b "$base" "$dir/disk.qcow2" "$size"
  cat >"$dir/user-data" <<EOF
#cloud-config
hostname: pk-$name
users:
  - name: pk
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys:
      - $(cat "$LAB_KEY.pub")
EOF
  printf 'instance-id: pk-%s-%s\nlocal-hostname: pk-%s\n' "$name" "$(date +%s)" "$name" >"$dir/meta-data"
  cat >"$dir/network-config" <<EOF
version: 2
ethernets:
  lan:
    match:
      macaddress: "52:54:00:98:51:$octet"
    set-name: eth0
    addresses: [$LAB_NET.$octet/24]
    routes:
      # Not "default": the RHEL family's cloud-init refuses it.
      - to: 0.0.0.0/0
        via: $LAB_NET.1
    nameservers:
      addresses: [$dns, 1.1.1.1]
EOF
  cloud-localds -N "$dir/network-config" "$dir/seed.iso" "$dir/user-data" "$dir/meta-data"
  local tap="pktap-$name"
  sudo ip tuntap del dev "$tap" mode tap 2>/dev/null || true
  sudo ip tuntap add dev "$tap" mode tap user "$(id -un)"
  sudo ip link set "$tap" master "$LAB_BRIDGE"
  sudo ip link set "$tap" up
  # -daemonize returns once the virtual machine is set up, in seconds. Where
  # KVM can't create a virtual CPU (the kernel oopses), qemu never returns.
  local st=0 accel=(-enable-kvm -cpu host)
  [ "$LAB_ACCEL" = tcg ] && accel=(-accel "tcg,thread=multi" -cpu max)
  timeout --kill-after=10 "$LAB_QEMU_START_TIMEOUT" qemu-system-x86_64 "${accel[@]}" -smp 2 -m "$mem" -name "pk-$name" \
    -drive "file=$dir/disk.qcow2,if=virtio" -drive "file=$dir/seed.iso,if=virtio,format=raw" \
    -netdev "tap,id=n0,ifname=$tap,script=no,downscript=no" -device "virtio-net-pci,netdev=n0,mac=52:54:00:98:51:$octet" \
    -display none -serial "file:$dir/console.log" -daemonize -pidfile "$dir/qemu.pid" || st=$?
  if [ "$st" != 0 ]; then
    case $st in
      124 | 137) lab_log "qemu did not finish starting $name within $LAB_QEMU_START_TIMEOUT s, so KVM is probably unusable on this machine: look for a KVM oops in 'sudo dmesg'. Serial log: $dir/console.log" ;;
      *) lab_log "qemu could not start $name (exit status $st)" ;;
    esac
    { [ -f "$dir/qemu.pid" ] && kill -9 "$(cat "$dir/qemu.pid")"; } 2>/dev/null || true
    lab_shutdown "$name"
    return 1
  fi
  lab_log "booted $name at $LAB_NET.$octet ($mem MB RAM, 2 vCPU, $((size / 1024 / 1024 / 1024)) GB disk)"
  lab_wait_ssh "$LAB_NET.$octet"
}

lab_ssh() { # IP command...
  local ip=$1
  shift
  ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=5 -i "$LAB_KEY" "pk@$ip" "$@"
}

lab_scp() { # SRC... IP:DEST
  scp -q -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -i "$LAB_KEY" "$@"
}

# lab_dnf IP ARG... runs sudo dnf -y ARG... in the guest, with dnf's output on
# stderr. When dnf can't get a repository's metadata or a package from any of
# its mirrors, it tries again with the metadata read afresh: a mirror part-way
# through a sync can list files it doesn't have yet, as AlmaLinux 9's extras
# repository's did on 1 Oct 2026 ("Cannot download, all mirrors were already
# tried without success"). Up to four tries, 15, 30 and 60 seconds apart; any
# other failure, such as a package that doesn't exist, fails at once with
# dnf's status. LAB_DNF_WAIT is the first wait in seconds (the tests use 0).
lab_dnf_transient='Failed to download metadata|all mirrors were already tried|Cannot download repomd\.xml|Yum repo downloading error|Curl error|Error downloading packages'
lab_dnf() {
  local ip=$1 try status out wait=${LAB_DNF_WAIT:-15}
  local refresh=()
  shift
  for try in 1 2 3 4; do
    out=$(lab_ssh "$ip" sudo dnf -y "${refresh[@]}" "$@" 2>&1) && status=0 || status=$?
    [ -z "$out" ] || printf '%s\n' "$out" >&2
    [ "$status" = 0 ] && return 0
    if [ "$try" = 4 ] || ! grep -Eq "$lab_dnf_transient" <<<"$out"; then
      return "$status"
    fi
    lab_log "dnf couldn't get what it needed from the mirrors; trying again in ${wait}s with fresh metadata (try $try of 4)"
    sleep "$wait"
    wait=$((wait * 2))
    refresh=(--refresh)
  done
}

lab_wait_ssh() {
  local ip=$1 i
  for i in $(seq 1 "$LAB_SSH_WAIT"); do
    if lab_ssh "$ip" 'cloud-init status --wait >/dev/null 2>&1; true' 2>/dev/null; then
      lab_log "$ip is up (ssh ready after ~$((i * 2)) s)"
      return 0
    fi
    sleep 2
  done
  lab_log "$ip did not come up"
  return 1
}

lab_shutdown() { # NAME
  local dir="$LAB_DIR/$1"
  if [ -f "$dir/qemu.pid" ]; then
    kill "$(cat "$dir/qemu.pid")" 2>/dev/null || true
    rm -f "$dir/qemu.pid"
  fi
  sudo ip tuntap del dev "pktap-$1" mode tap 2>/dev/null || true
}
