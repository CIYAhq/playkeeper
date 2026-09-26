#!/usr/bin/env bash
# Disposable KVM lab for Playkeeper rehearsals: fresh Ubuntu 24.04 cloud-image
# guests on a private bridge (198.51.100.0/24, a documentation range) with NAT
# to the internet. Source this file; it defines functions only.
#
# Requires: qemu-system-x86_64 with /dev/kvm, qemu-img, cloud-localds, iproute2,
# iptables, sudo. Guests are throwaway; nothing touches real hosts.

LAB_DIR=${LAB_DIR:-/tmp/pk-lab}
LAB_IMAGE_URL=${LAB_IMAGE_URL:-https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img}
LAB_JAMMY_URL=${LAB_JAMMY_URL:-https://cloud-images.ubuntu.com/minimal/releases/jammy/release/ubuntu-22.04-minimal-cloudimg-amd64.img}
LAB_BRIDGE=pkbr0
LAB_NET=198.51.100
LAB_KEY="$LAB_DIR/id_ed25519"
LAB_QEMU_START_TIMEOUT=${LAB_QEMU_START_TIMEOUT:-60}

lab_log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }

lab_image() {
  lab_image_named base "$LAB_IMAGE_URL"
  [ -f "$LAB_KEY" ] || ssh-keygen -q -t ed25519 -N '' -f "$LAB_KEY"
}

# lab_image_named NAME URL — downloads a cloud image to $LAB_DIR/NAME.img once,
# verified against the SHA256SUMS published next to it.
lab_image_named() {
  local name=$1 url=$2 file want got
  file=$(basename "$url")
  mkdir -p "$LAB_DIR"
  [ -f "$LAB_DIR/$name.img" ] && return 0
  lab_log "downloading $file"
  curl -fsSL -o "$LAB_DIR/$name.img.part" "$url"
  curl -fsSL -o "$LAB_DIR/$name.SHA256SUMS" "$(dirname "$url")/SHA256SUMS"
  want=$(awk -v f="$file" '$2 == "*" f || $2 == f {print $1}' "$LAB_DIR/$name.SHA256SUMS")
  got=$(sha256sum "$LAB_DIR/$name.img.part" | awk '{print $1}')
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
# $LAB_BASE_IMAGE (default: the Ubuntu 24.04 image).
lab_boot() {
  local name=$1 octet=$2 mem=${3:-3072} dir="$LAB_DIR/$1" dns
  dns=$(awk '/^nameserver/ {print $2; exit}' /etc/resolv.conf)
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
      - to: default
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
  local st=0
  timeout --kill-after=10 "$LAB_QEMU_START_TIMEOUT" qemu-system-x86_64 -enable-kvm -cpu host -smp 2 -m "$mem" -name "pk-$name" \
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
  for i in $(seq 1 90); do
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
