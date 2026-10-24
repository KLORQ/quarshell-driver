#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
python3 "$script_dir/test-required-go-test.py"
python3 "$script_dir/test-vm-runtime-libraries.py"
python3 "$script_dir/test-vm-engine-fixtures.py"
"$script_dir/test-build-podman6-fixture.sh"
workflow_file="$script_dir/../.github/workflows/live-vm-kvm.yml"
if grep -Eq '^  pull_request(_target)?:' "$workflow_file"; then
  echo "privileged live KVM workflow must not run untrusted pull request code" >&2
  exit 1
fi
if grep -F -- 'self-hosted' "$workflow_file" >/dev/null; then
  echo "live KVM workflow must use GitHub-hosted runners" >&2
  exit 1
fi
grep -F -- 'runs-on: ubuntu-24.04' "$workflow_file" >/dev/null
grep -F -- 'sudo udevadm trigger --settle --name-match=kvm' "$workflow_file" >/dev/null
grep -F -- 'exec 3<>/dev/kvm' "$workflow_file" >/dev/null
grep -F -- 'Build pinned libkrun runtime' "$workflow_file" >/dev/null
grep -F -- 'if: always()' "$workflow_file" >/dev/null
grep -F -- '"${{ runner.temp }}/quarshell-vm-runtime"' "$workflow_file" >/dev/null
if grep -F -- "if: matrix.arch == 'amd64'" "$workflow_file" >/dev/null; then
  echo "engine-in-MicroVM acceptance must run on every declared KVM architecture" >&2
  exit 1
fi
grep -F -- 'name: engines-in-microvm-amd64-${{ github.run_id }}' "$workflow_file" >/dev/null
temporary_dir=$(mktemp -d "${TMPDIR:-/tmp}/quarshell-kvm-script-test.XXXXXX")
cleanup() { rm -rf "$temporary_dir"; }
assert_single_force_cleanup() {
  count=$(grep -Fc -- 'rm -f fixture-container-id' "$1" || true)
  if [ "$count" != 1 ]; then
    echo "expected exactly one force cleanup in $1, got $count" >&2
    cat "$1" >&2
    exit 1
  fi
}
trap cleanup EXIT
trap 'cleanup; exit 129' HUP
trap 'cleanup; exit 130' INT
trap 'cleanup; exit 143' TERM

mkdir -p "$temporary_dir/bin" "$temporary_dir/runtime" "$temporary_dir/build"
for binary in quarshell-vm-worker quarshell-vm-umoci-linux-amd64 quarshell-vm-guest-agent-linux-amd64 quarshell-init-linux-amd64 quarshell-supervisor-linux-amd64 quarshell-sshd-linux-amd64 quarshell-vm-security-probe-linux-amd64; do
  printf '#!/bin/sh\nexit 0\n' >"$temporary_dir/build/$binary"
  chmod 0755 "$temporary_dir/build/$binary"
done
python3 - "$temporary_dir/build" "$temporary_dir/runtime" <<'PY'
import hashlib
import json
import pathlib
import struct
import sys

root = pathlib.Path(sys.argv[1])
runtime = pathlib.Path(sys.argv[2])
artifacts = sorted(path for path in root.iterdir() if path.name.startswith("quarshell-"))
manifest = {
    "schema_version": 1,
    "vm_control_protocol": 2,
    "host": "linux/amd64",
    "guest": "linux/amd64",
    "artifacts": [
        {"path": path.name, "sha256": hashlib.sha256(path.read_bytes()).hexdigest(), "executable": True}
        for path in artifacts
    ],
}
(root / "vm-runtime-manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
header = bytearray(64)
header[:4] = b"\x7fELF"
header[4:6] = bytes((2, 1))
struct.pack_into("<HH", header, 16, 3, 62)
struct.pack_into("<H", header, 52, 64)
for name in ("libkrun.so", "libkrunfw.so.5"):
    (runtime / name).write_bytes(header)
    digest = hashlib.sha256(header).hexdigest()
    if name == "libkrun.so":
        libkrun_digest = digest
    else:
        libkrunfw_digest = digest
(runtime.parent / "approved-runtime.json").write_text(json.dumps({
    "schema_version": 1,
    "host": "linux/amd64",
    "artifacts": {
        "libkrun.so": libkrun_digest,
        "libkrunfw.so.5": libkrunfw_digest,
    },
}), encoding="utf-8")
PY
export QUARSHELL_VM_RUNTIME_TRUST_LOCK="$temporary_dir/approved-runtime.json"
printf '#!/bin/sh\nexit 0\n' >"$temporary_dir/formatter"
chmod 0755 "$temporary_dir/formatter"

cat >"$temporary_dir/bin/go" <<'EOF'
#!/bin/sh
if [ "${1:-}" = env ]; then
  case "${2:-}" in
    GOOS) echo linux ;;
    GOARCH) echo "${QUARSHELL_TEST_GOARCH:-amd64}" ;;
    *) exit 2 ;;
  esac
  exit 0
fi
if [ "${1:-}" = build ]; then
  output=
  while [ "$#" -gt 0 ]; do
    if [ "$1" = -o ]; then
      shift
      output=${1:-}
      break
    fi
    shift
  done
  if [ -z "$output" ]; then
    echo "mock go build did not receive -o" >&2
    exit 3
  fi
  mkdir -p "$(dirname "$output")"
  printf '#!/bin/sh\nexit 0\n' >"$output"
  chmod 0755 "$output"
  exit 0
fi
if [ "${1:-}" = test ] && [ "${QUARSHELL_VM_TEST_ROOTFS_TAR##*/}" = alpine-rootfs.tar ]; then
  if [ "${QUARSHELL_TEST_INVALID_ROOTFS:-0}" = 1 ] && [ "$(cat "$QUARSHELL_VM_TEST_ROOTFS_TAR")" != 'fixture rootfs archive' ]; then
    printf '%s\n' \
      '{"Action":"run","Package":"github.com/KLORQ/quarshell-driver-vm/vm","Test":"TestLiveMicroVMLifecycle"}' \
      '{"Action":"fail","Package":"github.com/KLORQ/quarshell-driver-vm/vm","Test":"TestLiveMicroVMLifecycle"}' \
      '{"Action":"fail","Package":"github.com/KLORQ/quarshell-driver-vm/vm","Elapsed":0.0}'
    exit 1
  fi
  printf '%s\n' \
    '{"Action":"run","Package":"github.com/KLORQ/quarshell-driver-vm/vm","Test":"TestLiveMicroVMLifecycle"}' \
    '{"Action":"output","Package":"github.com/KLORQ/quarshell-driver-vm/vm","Test":"TestLiveMicroVMLifecycle","Output":"--- PASS: TestLiveMicroVMLifecycle (0.00s)\n"}' \
    '{"Action":"pass","Package":"github.com/KLORQ/quarshell-driver-vm/vm","Test":"TestLiveMicroVMLifecycle"}' \
    '{"Action":"pass","Package":"github.com/KLORQ/quarshell-driver-vm/vm","Elapsed":0.0}'
  exit 0
fi
echo "unexpected go invocation: $*" >&2
exit 3
EOF
cat >"$temporary_dir/bin/id" <<'EOF'
#!/bin/sh
if [ "${1:-}" = -u ]; then
  echo "${QUARSHELL_TEST_UID:-1000}"
  exit 0
fi
exit 2
EOF
cat >"$temporary_dir/bin/docker" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >>"$QUARSHELL_TEST_DOCKER_LOG"
case "${1:-}" in
  run) echo fixture-container-id ;;
  exec)
    if [ "${QUARSHELL_TEST_DOCKER_TERM:-0}" = 1 ]; then
      kill -TERM "$PPID"
      sleep 1
      exit 143
    fi
    exit 0
    ;;
  export)
    if [ "${QUARSHELL_TEST_DOCKER_EXPORT_FAIL:-0}" = 1 ]; then exit 5; fi
    output=
    previous=
    for argument in "$@"; do
      if [ "$previous" = output ]; then output=$argument; break; fi
      if [ "$argument" = --output ]; then previous=output; fi
    done
    [ -n "$output" ] || exit 2
    printf 'fixture rootfs archive\n' >"$output"
    ;;
  rm)
    if [ "${2:-}" != -f ] || [ "${3:-}" != fixture-container-id ]; then
      echo "running fixture must be removed with docker rm -f" >&2
      exit 4
    fi
    ;;
  *) echo "unexpected docker invocation: $*" >&2; exit 3 ;;
esac
EOF
chmod 0755 "$temporary_dir/bin/go" "$temporary_dir/bin/docker"
chmod 0755 "$temporary_dir/bin/id"

if PATH="$temporary_dir/bin:$PATH" \
  QUARSHELL_TEST_UID=0 \
  QUARSHELL_KVM_DEVICE=/dev/null \
  "$script_dir/run-live-kvm-acceptance.sh" >"$temporary_dir/root.log" 2>&1; then
  echo "KVM acceptance accepted a root runner" >&2
  exit 1
fi
grep -F -- 'must run as a non-root user' "$temporary_dir/root.log" >/dev/null || {
  cat "$temporary_dir/root.log" >&2
  echo "KVM acceptance root preflight did not explain the requirement" >&2
  exit 1
}

PATH="$temporary_dir/bin:$PATH" \
QUARSHELL_TEST_UID=1000 \
QUARSHELL_KVM_DEVICE=/dev/null \
QUARSHELL_VM_BUILD_DIR="$temporary_dir/build" \
OPENSHELL_VM_RUNTIME_DIR="$temporary_dir/runtime" \
QUARSHELL_VM_TEST_E2FS_FORMATTER="$temporary_dir/formatter" \
QUARSHELL_TEST_DOCKER_LOG="$temporary_dir/docker.log" \
"$script_dir/run-live-kvm-acceptance.sh" >"$temporary_dir/run.log" 2>&1 || {
  cat "$temporary_dir/run.log" >&2
  cat "$temporary_dir/docker.log" >&2
  echo "mocked KVM acceptance orchestration failed" >&2
  exit 1
}

grep -F -- 'run -d' "$temporary_dir/docker.log" >/dev/null
grep -F -- 'export --output' "$temporary_dir/docker.log" >/dev/null
assert_single_force_cleanup "$temporary_dir/docker.log"
grep -F -- '--- PASS: TestLiveMicroVMLifecycle' "$temporary_dir/run.log" >/dev/null
grep -F -- 'required Go test gate: PASS (TestLiveMicroVMLifecycle)' "$temporary_dir/run.log" >/dev/null

# A runtime digest mismatch must fail before rootfs preparation or VM launch.
cp "$QUARSHELL_VM_RUNTIME_TRUST_LOCK" "$temporary_dir/approved-runtime.good.json"
python3 - "$QUARSHELL_VM_RUNTIME_TRUST_LOCK" <<'PY'
import json
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
lock = json.loads(path.read_text(encoding="utf-8"))
lock["artifacts"]["libkrun.so"] = "0" * 64
path.write_text(json.dumps(lock), encoding="utf-8")
PY
: >"$temporary_dir/digest-mismatch-docker.log"
if PATH="$temporary_dir/bin:$PATH" \
  QUARSHELL_TEST_UID=1000 \
  QUARSHELL_KVM_DEVICE=/dev/null \
  QUARSHELL_VM_BUILD_DIR="$temporary_dir/build" \
  OPENSHELL_VM_RUNTIME_DIR="$temporary_dir/runtime" \
  QUARSHELL_VM_TEST_ROOTFS_TAR="$temporary_dir/alpine-rootfs.tar" \
  QUARSHELL_TEST_DOCKER_LOG="$temporary_dir/digest-mismatch-docker.log" \
  "$script_dir/run-live-kvm-acceptance.sh" >"$temporary_dir/digest-mismatch.log" 2>&1; then
  echo "KVM acceptance accepted runtime libraries outside the approved digest lock" >&2
  exit 1
fi
grep -F -- 'does not match the operator-approved runtime digest lock' "$temporary_dir/digest-mismatch.log" >/dev/null
if [ -s "$temporary_dir/digest-mismatch-docker.log" ]; then
  echo "KVM acceptance invoked Docker before rejecting runtime digest mismatch" >&2
  exit 1
fi
mv "$temporary_dir/approved-runtime.good.json" "$QUARSHELL_VM_RUNTIME_TRUST_LOCK"

# A supplied rootfs bypasses Docker fixture creation and still reaches the test.
printf 'fixture rootfs archive\n' >"$temporary_dir/alpine-rootfs.tar"
: >"$temporary_dir/supplied-docker.log"
PATH="$temporary_dir/bin:$PATH" \
QUARSHELL_TEST_UID=1000 \
QUARSHELL_KVM_DEVICE=/dev/null \
QUARSHELL_VM_BUILD_DIR="$temporary_dir/build" \
OPENSHELL_VM_RUNTIME_DIR="$temporary_dir/runtime" \
QUARSHELL_VM_TEST_E2FS_FORMATTER="$temporary_dir/formatter" \
QUARSHELL_VM_TEST_ROOTFS_TAR="$temporary_dir/alpine-rootfs.tar" \
QUARSHELL_TEST_DOCKER_LOG="$temporary_dir/supplied-docker.log" \
"$script_dir/run-live-kvm-acceptance.sh" >"$temporary_dir/supplied.log" 2>&1 || {
  cat "$temporary_dir/supplied.log" >&2
  echo "prebuilt rootfs acceptance path failed" >&2
  exit 1
}
if [ -s "$temporary_dir/supplied-docker.log" ]; then
  echo "prebuilt rootfs unexpectedly invoked Docker" >&2
  cat "$temporary_dir/supplied-docker.log" >&2
  exit 1
fi

# Export failure must trigger force cleanup and must not reach Go acceptance.
: >"$temporary_dir/export-fail-docker.log"
if PATH="$temporary_dir/bin:$PATH" \
  QUARSHELL_TEST_UID=1000 \
  QUARSHELL_TEST_DOCKER_EXPORT_FAIL=1 \
  QUARSHELL_KVM_DEVICE=/dev/null \
  QUARSHELL_VM_BUILD_DIR="$temporary_dir/build" \
  OPENSHELL_VM_RUNTIME_DIR="$temporary_dir/runtime" \
  QUARSHELL_VM_TEST_E2FS_FORMATTER="$temporary_dir/formatter" \
  QUARSHELL_TEST_DOCKER_LOG="$temporary_dir/export-fail-docker.log" \
  "$script_dir/run-live-kvm-acceptance.sh" >"$temporary_dir/export-fail.log" 2>&1; then
  echo "KVM acceptance ignored rootfs export failure" >&2
  exit 1
fi
assert_single_force_cleanup "$temporary_dir/export-fail-docker.log"
if grep -F -- 'required Go test gate:' "$temporary_dir/export-fail.log" >/dev/null; then
  echo "KVM acceptance ran Go tests after rootfs export failure" >&2
  exit 1
fi

# A malformed prebuilt archive is passed through to the required live test and fails the gate.
printf 'not a tar archive\n' >"$temporary_dir/alpine-rootfs.tar"
if PATH="$temporary_dir/bin:$PATH" \
  QUARSHELL_TEST_UID=1000 \
  QUARSHELL_TEST_INVALID_ROOTFS=1 \
  QUARSHELL_KVM_DEVICE=/dev/null \
  QUARSHELL_VM_BUILD_DIR="$temporary_dir/build" \
  OPENSHELL_VM_RUNTIME_DIR="$temporary_dir/runtime" \
  QUARSHELL_VM_TEST_E2FS_FORMATTER="$temporary_dir/formatter" \
  QUARSHELL_VM_TEST_ROOTFS_TAR="$temporary_dir/alpine-rootfs.tar" \
  QUARSHELL_TEST_DOCKER_LOG="$temporary_dir/invalid-rootfs-docker.log" \
  "$script_dir/run-live-kvm-acceptance.sh" >"$temporary_dir/invalid-rootfs.log" 2>&1; then
  echo "KVM acceptance accepted the mocked malformed rootfs" >&2
  exit 1
fi
grep -F -- 'required Go test gate: FAIL' "$temporary_dir/invalid-rootfs.log" >/dev/null

# Runtime absence and unsupported architecture fail before Docker is invoked.
if PATH="$temporary_dir/bin:$PATH" \
  QUARSHELL_TEST_UID=1000 \
  QUARSHELL_KVM_DEVICE=/dev/null \
  QUARSHELL_VM_BUILD_DIR="$temporary_dir/build" \
  OPENSHELL_VM_RUNTIME_DIR="$temporary_dir/missing-runtime" \
  QUARSHELL_VM_TEST_ROOTFS_TAR="$temporary_dir/alpine-rootfs.tar" \
  QUARSHELL_TEST_DOCKER_LOG="$temporary_dir/runtime-missing-docker.log" \
  "$script_dir/run-live-kvm-acceptance.sh" >"$temporary_dir/runtime-missing.log" 2>&1; then
  echo "KVM acceptance accepted an unavailable runtime" >&2
  exit 1
fi
grep -F -- 'runtime bundle is incomplete' "$temporary_dir/runtime-missing.log" >/dev/null
if [ -s "$temporary_dir/runtime-missing-docker.log" ]; then
  echo "KVM acceptance invoked Docker before runtime preflight" >&2
  exit 1
fi
if PATH="$temporary_dir/bin:$PATH" \
  QUARSHELL_TEST_UID=1000 \
  QUARSHELL_TEST_GOARCH=ppc64le \
  QUARSHELL_KVM_DEVICE=/dev/null \
  QUARSHELL_VM_BUILD_DIR="$temporary_dir/build" \
  OPENSHELL_VM_RUNTIME_DIR="$temporary_dir/runtime" \
  QUARSHELL_VM_TEST_ROOTFS_TAR="$temporary_dir/alpine-rootfs.tar" \
  QUARSHELL_TEST_DOCKER_LOG="$temporary_dir/arch-mismatch-docker.log" \
  "$script_dir/run-live-kvm-acceptance.sh" >"$temporary_dir/arch-mismatch.log" 2>&1; then
  echo "KVM acceptance accepted an unsupported architecture" >&2
  exit 1
fi
grep -F -- 'does not support Linux/ppc64le' "$temporary_dir/arch-mismatch.log" >/dev/null
if [ -s "$temporary_dir/arch-mismatch-docker.log" ]; then
  echo "KVM acceptance invoked Docker before architecture preflight" >&2
  exit 1
fi

# Deliver TERM during Docker setup and verify the running fixture is force-removed.
: >"$temporary_dir/term-docker.log"
if PATH="$temporary_dir/bin:$PATH" \
  QUARSHELL_TEST_UID=1000 \
  QUARSHELL_TEST_DOCKER_TERM=1 \
  QUARSHELL_KVM_DEVICE=/dev/null \
  QUARSHELL_VM_BUILD_DIR="$temporary_dir/build" \
  OPENSHELL_VM_RUNTIME_DIR="$temporary_dir/runtime" \
  QUARSHELL_VM_TEST_E2FS_FORMATTER="$temporary_dir/formatter" \
  QUARSHELL_TEST_DOCKER_LOG="$temporary_dir/term-docker.log" \
  "$script_dir/run-live-kvm-acceptance.sh" >"$temporary_dir/term.log" 2>&1; then
  echo "KVM acceptance ignored SIGTERM during Docker setup" >&2
  exit 1
fi
assert_single_force_cleanup "$temporary_dir/term-docker.log"

echo "KVM acceptance fixture lifecycle: PASS"
