#!/usr/bin/env bash
# Disposable KVM lab for Playkeeper rehearsals: fresh guests from official
# Ubuntu and Debian cloud images on a private bridge (198.51.100.0/24, a
# documentation range) with NAT to the internet. Source this file; it defines
# functions only.
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

lab_image() {
  lab_image_named base "$LAB_IMAGE_URL"
  lab_key
}

lab_key() {
  mkdir -p "$LAB_DIR"
  [ -f "$LAB_KEY" ] || ssh-keygen -q -t ed25519 -N '' -f "$LAB_KEY"
}

# lab_os_url OS — the official cloud image of a supported release
# (internal/platform), by the name the OS matrix gives it.
lab_os_url() {
  case $1 in
    ubuntu-20.04) echo https://cloud-images.ubuntu.com/focal/current/focal-server-cloudimg-amd64.img ;;
    ubuntu-22.04) echo https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img ;;
    ubuntu-24.04) echo https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img ;;
    ubuntu-26.04) echo https://cloud-images.ubuntu.com/resolute/current/resolute-server-cloudimg-amd64.img ;;
    debian-12) echo https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-genericcloud-amd64.qcow2 ;;
    debian-13) echo https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-amd64.qcow2 ;;
    *)
      lab_log "no cloud image for $1"
      return 1
      ;;
  esac
}

# lab_image_named NAME URL — downloads a cloud image to $LAB_DIR/NAME.img once,
# verified against the checksums published next to it: Ubuntu's SHA256SUMS or
# Debian's SHA512SUMS.
lab_image_named() {
  local name=$1 url=$2 file want got sums sum
  file=$(basename "$url")
  mkdir -p "$LAB_DIR"
  [ -f "$LAB_DIR/$name.img" ] && return 0
  lab_log "downloading $file"
  curl -fsSL --retry 3 -o "$LAB_DIR/$name.img.part" "$url"
  for sums in SHA256SUMS SHA512SUMS; do
    curl -fsSL --retry 3 -o "$LAB_DIR/$name.$sums" "$(dirname "$url")/$sums" 2>/dev/null && break
    rm -f "$LAB_DIR/$name.$sums"
  done
  sum=sha256sum
  [ "$sums" = SHA512SUMS ] && sum=sha512sum
  want=$(awk -v f="$file" '$2 == "*" f || $2 == f {print $1}' "$LAB_DIR/$name.$sums" 2>/dev/null || true)
  got=$($sum "$LAB_DIR/$name.img.part" | awk '{print $1}')
  if [ -z "$want" ] || [ "$want" != "$got" ]; then
    lab_log "cloud image checksum mismatch for $file"
    return 1
  fi
  mv "$LAB_DIR/$name.img.part" "$LAB_DIR/$name.img"
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
  local name=$1 octet=$2 mem=${3:-3072} dir="$LAB_DIR/$1" dns
  # A resolver on the host's loopback (systemd-resolved's 127.0.0.53) is out
  # of the guests' reach: they get the servers it forwards to.
  dns=$(awk '/^nameserver/ && $2 !~ /^127\./ {print $2; exit}' /etc/resolv.conf /run/systemd/resolve/resolv.conf 2>/dev/null || true)
  dns=${dns:-1.1.1.1}
  rm -rf "$dir"
  mkdir -p "$dir"
  qemu-img create -q -f qcow2 -F qcow2 -b "${LAB_BASE_IMAGE:-$LAB_DIR/base.img}" "$dir/disk.qcow2" 20G
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
  [ "$LAB_ACCEL" = tcg ] && accel=(-accel tcg -cpu max)
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
  lab_log "booted $name at $LAB_NET.$octet ($mem MB RAM, 2 vCPU, 20 GB disk)"
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
