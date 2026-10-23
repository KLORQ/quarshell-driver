#!/bin/sh
set -eu
unset GOROOT

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
build_dir=${QUARSHELL_VM_BUILD_DIR:-"$(mktemp -d "${TMPDIR:-/tmp}/quarshell-kvm-podman.XXXXXX")"}
made_build_dir=0
if [ -z "${QUARSHELL_VM_BUILD_DIR:-}" ]; then made_build_dir=1; fi
fixture_dir="$build_dir/nested-podman-fixture"
mkdir -p "$build_dir"
rm -rf "$fixture_dir"
mkdir -m 700 "$fixture_dir"
owner="quarshell-vm-podman-$$"
container_id=
bootstrap_container_id=
status=0
cleanup() {
  status=$?
  if [ -n "$container_id" ]; then docker rm -f "$container_id" >/dev/null 2>&1 || true; fi
  if [ -n "$bootstrap_container_id" ]; then docker rm -f "$bootstrap_container_id" >/dev/null 2>&1 || true; fi
  if [ "${E2E_KEEP_TMP:-0}" != 1 ]; then rm -rf "$fixture_dir"; fi
  if [ "$made_build_dir" -eq 1 ]; then rm -rf "$build_dir"; fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

goos=$(go env GOOS)
host_arch=$(go env GOARCH)
if [ "$goos" != linux ]; then
  echo "Podman-in-MicroVM acceptance requires native Linux KVM; found $goos/$host_arch" >&2
  exit 2
fi
case "$host_arch" in amd64|arm64) ;; *) echo "unsupported Linux architecture: $host_arch" >&2; exit 2 ;; esac
command -v docker >/dev/null 2>&1 || { echo "Docker CLI is required to prepare the guest rootfs" >&2; exit 2; }
docker info >/dev/null 2>&1 || { echo "Docker daemon is unavailable" >&2; exit 2; }

lockfile="$script_dir/vm-engine-fixtures.lock.json"
test_image=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["test_image"])' "$lockfile")
test_image_tag=docker.io/library/alpine:3.22
image="localhost/quarshell-podman6-vm-test:6.1.4"
podman_test_name=${QUARSHELL_VM_TEST_NAME:-TestLiveMicroVMNestedPodman}
QUARSHELL_PODMAN_FIXTURE_PLATFORM="linux/$host_arch" \
QUARSHELL_PODMAN_FIXTURE_DIGEST_FILE="$fixture_dir/podman6-fixture-image-id.txt" \
  "$script_dir/build-podman6-fixture.sh" "$image"
docker pull --platform "linux/$host_arch" "$test_image"
docker tag "$test_image" "$test_image_tag"
docker save --output "$fixture_dir/alpine-image.tar" "$test_image_tag"
chmod 0644 "$fixture_dir/alpine-image.tar"

# Keep the disposable VM bootstrap rootfs independent from the Podman distro
# under test. The latter remains the workload rootfs and data-disk fixture.
bootstrap_container_id=$(docker create --platform "linux/$host_arch" \
  --label "quarshell.vm-test.owner=$owner-bootstrap" --entrypoint /bin/sh "$test_image_tag" -c 'sleep 3600')
docker start "$bootstrap_container_id" >/dev/null
docker export --output "$fixture_dir/bootstrap-rootfs.tar" "$bootstrap_container_id"
docker rm -f "$bootstrap_container_id" >/dev/null
bootstrap_container_id=
chmod 0644 "$fixture_dir/bootstrap-rootfs.tar"

container_id=$(docker create --platform "linux/$host_arch" \
  --label "quarshell.vm-test.owner=$owner" --entrypoint /bin/sh "$image" -c 'sleep 3600')
docker start "$container_id" >/dev/null
docker exec "$container_id" useradd --uid 1000 --user-group --no-create-home sandbox
docker exec "$container_id" mkdir -p /opt/quarshell-test
docker cp "$fixture_dir/alpine-image.tar" "$container_id:/opt/quarshell-test/alpine-image.tar"
docker stop "$container_id" >/dev/null
docker export --output "$fixture_dir/podman-engine-rootfs.tar" "$container_id"
docker rm "$container_id" >/dev/null
container_id=
chmod 0644 "$fixture_dir/podman-engine-rootfs.tar"

QUARSHELL_VM_BUILD_DIR=$build_dir \
QUARSHELL_VM_TEST_ROOTFS_TAR="$fixture_dir/podman-engine-rootfs.tar" \
QUARSHELL_VM_TEST_BOOTSTRAP_ROOTFS_TAR="$fixture_dir/bootstrap-rootfs.tar" \
QUARSHELL_VM_PODMAN_ACCEPTANCE=1 \
QUARSHELL_VM_TEST_NAME="$podman_test_name" \
"$script_dir/run-live-kvm-acceptance.sh"
