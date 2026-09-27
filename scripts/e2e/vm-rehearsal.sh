#!/usr/bin/env bash
# Steps of the VM rehearsal workflow (.github/workflows/vm-rehearsal.yml) that
# are tested without KVM (scripts/e2e/vm-rehearsal_test.sh).
#   kvm-check    checks that KVM can create a virtual CPU. It gives up after
#                KVM_CHECK_TIMEOUT seconds (default 60): where the kernel
#                oopses, that ioctl never returns.
#   redact DIR   replaces the setup codes in the evidence under DIR.
# Usage: scripts/e2e/vm-rehearsal.sh kvm-check | redact DIR
set -euo pipefail

case ${1:-} in
  kvm-check)
    limit=${KVM_CHECK_TIMEOUT:-60}
    st=0
    timeout --kill-after=10 "$limit" python3 -c 'import fcntl, os; vm = fcntl.ioctl(os.open("/dev/kvm", os.O_RDWR), 0xAE01, 0); fcntl.ioctl(vm, 0xAE41, 0); print("KVM can create a virtual CPU")' || st=$?
    case $st in
      0) ;;
      124 | 137)
        echo "KVM did not create a virtual CPU within $limit s: look for a KVM oops in 'sudo dmesg'." >&2
        exit 1
        ;;
      *)
        echo "KVM can't create a virtual CPU (exit status $st)." >&2
        exit 1
        ;;
    esac
    ;;
  redact)
    dir=${2:?usage: scripts/e2e/vm-rehearsal.sh redact DIR}
    # grep finds nothing (status 1) when the run stopped before any setup code.
    { grep -rlIEZ 'setup code: |#code=' "$dir" || [ $? = 1 ]; } | xargs -0 -r sed -i -E 's/(setup code: |#code=)[a-z0-9-]+/\1<redacted>/g'
    ;;
  *)
    echo "Usage: scripts/e2e/vm-rehearsal.sh kvm-check | redact DIR" >&2
    exit 2
    ;;
esac
