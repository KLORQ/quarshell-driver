#!/bin/sh
set -eu
unset GOROOT

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
module_dir=$(CDPATH= cd -- "$script_dir/.." && pwd)
workspace_dir=$(CDPATH= cd -- "$module_dir/.." && pwd)
vm_module_dir="$module_dir/../quarshell-driver-vm"
host_os=$(go env GOOS)
host_arch=$(go env GOARCH)

if [ "$host_os" != linux ]; then
  echo "live KVM acceptance requires a native Linux host; found $host_os/$host_arch" >&2
  exit 2
fi
case "$host_arch" in
  amd64|arm64) ;;
  *) echo "live KVM acceptance does not support Linux/$host_arch" >&2; exit 2 ;;
esac

runner_uid=$(id -u)
case "$runner_uid" in
  ''|*[!0-9]*) echo "cannot determine numeric runner UID" >&2; exit 2 ;;
  0) echo "live KVM acceptance must run as a non-root user with explicit access to KVM" >&2; exit 2 ;;
esac

kvm_device=${QUARSHELL_KVM_DEVICE:-/dev/kvm}
if [ ! -c "$kvm_device" ] || [ ! -r "$kvm_device" ] || [ ! -w "$kvm_device" ]; then
  echo "live KVM acceptance requires a readable and writable KVM character device at $kvm_device" >&2
  exit 2
fi

runtime_dir=${OPENSHELL_VM_RUNTIME_DIR:-}
if [ -z "$runtime_dir" ]; then
  echo "OPENSHELL_VM_RUNTIME_DIR must point to a compatible libkrun Linux runtime bundle" >&2
  exit 2
fi
if [ ! -s "$runtime_dir/libkrun.so" ] || [ ! -s "$runtime_dir/libkrunfw.so.5" ]; then
  echo "libkrun Linux runtime bundle is incomplete in $runtime_dir" >&2
  exit 2
fi
runtime_trust_lock=${QUARSHELL_VM_RUNTIME_TRUST_LOCK:-}
if [ -z "$runtime_trust_lock" ] || [ ! -s "$runtime_trust_lock" ]; then
  echo "QUARSHELL_VM_RUNTIME_TRUST_LOCK must point to an operator-approved runtime digest lock outside the checkout" >&2
  exit 2
fi

build_dir=${QUARSHELL_VM_BUILD_DIR:-"$(mktemp -d "${TMPDIR:-/tmp}/quarshell-kvm-acceptance.XXXXXX")"}
mkdir -p "$build_dir"
python3 "$script_dir/verify-vm-runtime-libraries.py" "$runtime_dir" "$host_arch" "$runtime_trust_lock" "$build_dir/vm-runtime-libraries.json"
cleanup_dir=
if [ -z "${QUARSHELL_VM_BUILD_DIR:-}" ]; then
  cleanup_dir=$build_dir
fi
container_id=
cleanup() {
  status=$?
  if [ -n "$container_id" ]; then
    docker rm -f "$container_id" >/dev/null 2>&1 || true
  fi
  if [ -n "$cleanup_dir" ]; then
    rm -rf "$cleanup_dir"
  fi
  trap - EXIT
  exit "$status"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

guest_agent=${QUARSHELL_VM_TEST_GUEST_AGENT:-"$build_dir/quarshell-vm-guest-agent-linux-$host_arch"}
worker=${QUARSHELL_VM_WORKER:-"$build_dir/quarshell-vm-worker"}
security_probe=${QUARSHELL_VM_TEST_SECURITY_PROBE:-"$build_dir/quarshell-vm-security-probe-linux-$host_arch"}
proxy_cli=${QUARSHELL_VM_TEST_PROXY_CLI:-"$build_dir/quarshell-cli-live-test"}
manifest="$build_dir/vm-runtime-manifest.json"
if ! python3 "$script_dir/verify-vm-runtime-manifest.py" "$manifest" "linux/$host_arch" "$host_arch" >/dev/null 2>&1; then
  QUARSHELL_VM_BUILD_DIR=$build_dir QUARSHELL_VM_GUEST_ARCH=$host_arch "$script_dir/build-vm-runtime.sh"
fi
python3 "$script_dir/verify-vm-runtime-manifest.py" "$manifest" "linux/$host_arch" "$host_arch"
if [ ! -x "$guest_agent" ] || [ ! -x "$worker" ]; then
  echo "VM worker and static guest agent paths must point to executable runtime artifacts" >&2
  exit 1
fi

if [ -n "${QUARSHELL_VM_TEST_PROXY_CLI:-}" ]; then
  if [ ! -f "$proxy_cli" ] || [ ! -x "$proxy_cli" ]; then
    echo "QUARSHELL_VM_TEST_PROXY_CLI must point to an executable file: $proxy_cli" >&2
    exit 2
  fi
else
  go build -C "$workspace_dir/quarshell-cli" -o "$proxy_cli" ./cmd/quarshell
fi

rootfs_tar=${QUARSHELL_VM_TEST_ROOTFS_TAR:-}
if [ -z "$rootfs_tar" ]; then
  if ! command -v docker >/dev/null 2>&1; then
    echo "Docker is required to prepare the default Alpine Linux rootfs" >&2
    exit 2
  fi
  container_id=$(docker run -d --platform "linux/$host_arch" docker.io/library/alpine:3.22@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8 /bin/sh -c 'while :; do sleep 3600; done')
  docker exec "$container_id" adduser -D -u 1000 sandbox
  rootfs_tar="$build_dir/alpine-rootfs.tar"
  docker export --output "$rootfs_tar" "$container_id"
  docker rm -f "$container_id" >/dev/null
  container_id=
fi
if [ ! -s "$rootfs_tar" ]; then
  echo "live KVM rootfs tar is missing or empty: $rootfs_tar" >&2
  exit 2
fi

formatter=${QUARSHELL_VM_TEST_E2FS_FORMATTER:-}
if [ -z "$formatter" ]; then
  formatter=$(command -v mke2fs || command -v mkfs.ext4 || true)
fi
if [ -z "$formatter" ] || [ ! -x "$formatter" ]; then
  echo "e2fsprogs is required; set QUARSHELL_VM_TEST_E2FS_FORMATTER or install mke2fs" >&2
  exit 2
fi

cd "$vm_module_dir"
test_name=${QUARSHELL_VM_TEST_NAME:-TestLiveMicroVMLifecycle}
case "$test_name" in
  TestLiveMicroVMLifecycle|TestLiveMicroVMNestedDocker|TestLiveMicroVMNestedPodman|TestLiveMicroVMNestedPodmanRootless) ;;
  *) echo "unsupported live KVM acceptance test: $test_name" >&2; exit 2 ;;
esac
QUARSHELL_KVM_DEVICE=$kvm_device \
QUARSHELL_VM_ACCEPTANCE=1 \
QUARSHELL_VM_TEST_ROOTFS_TAR=$rootfs_tar \
QUARSHELL_VM_TEST_GUEST_AGENT=$guest_agent \
QUARSHELL_VM_TEST_INIT_BIN=${QUARSHELL_VM_TEST_INIT_BIN:-"$build_dir/quarshell-init-linux-$host_arch"} \
QUARSHELL_VM_TEST_SUPERVISOR_BIN=${QUARSHELL_VM_TEST_SUPERVISOR_BIN:-"$build_dir/quarshell-supervisor-linux-$host_arch"} \
QUARSHELL_VM_TEST_SSH_BIN=${QUARSHELL_VM_TEST_SSH_BIN:-"$build_dir/quarshell-sshd-linux-$host_arch"} \
QUARSHELL_VM_TEST_SECURITY_PROBE=$security_probe \
QUARSHELL_VM_TEST_PROXY_CLI=$proxy_cli \
QUARSHELL_VM_TEST_E2FS_FORMATTER=$formatter \
QUARSHELL_VM_WORKER=$worker \
OPENSHELL_VM_RUNTIME_DIR=$runtime_dir \
QUARSHELL_VM_TEST_ARTIFACT_DIR=$build_dir \
QUARSHELL_VM_TEST_JSON_LOG="$build_dir/test-events-$test_name.jsonl" \
python3 "$script_dir/run-required-go-test.py" --test "$test_name" -- \
  go test -timeout=20m -json ./vm -run "^$test_name$" -count=1 -v
