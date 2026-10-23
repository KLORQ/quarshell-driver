#!/bin/sh
set -eu
unset GOROOT

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
build_dir=${CAUTEM_VM_BUILD_DIR:-"$(mktemp -d "${TMPDIR:-/tmp}/cautem-kvm-docker.XXXXXX")"}
made_build_dir=0
if [ -z "${CAUTEM_VM_BUILD_DIR:-}" ]; then made_build_dir=1; fi
fixture_dir="$build_dir/nested-docker-fixture"
mkdir -p "$build_dir"
rm -rf "$fixture_dir"
mkdir -m 700 "$fixture_dir"
owner="cautem-vm-docker-$$"
container_id=
status=0
cleanup() {
  status=$?
  if [ -n "$container_id" ]; then docker rm -f "$container_id" >/dev/null 2>&1 || true; fi
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
  echo "Docker-in-MicroVM acceptance requires native Linux KVM; found $goos/$host_arch" >&2
  exit 2
fi
case "$host_arch" in amd64|arm64) ;; *) echo "unsupported Linux architecture: $host_arch" >&2; exit 2 ;; esac
command -v docker >/dev/null 2>&1 || { echo "Docker CLI is required to prepare the guest rootfs" >&2; exit 2; }
docker info >/dev/null 2>&1 || { echo "Docker daemon is unavailable" >&2; exit 2; }

docker_dind_image=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["docker_dind"])' "$script_dir/vm-engine-fixtures.lock.json")
test_image=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["test_image"])' "$script_dir/vm-engine-fixtures.lock.json")
test_image_tag=docker.io/library/alpine:3.22
docker pull --platform "linux/$host_arch" "$docker_dind_image"
docker pull --platform "linux/$host_arch" "$test_image"
docker tag "$test_image" "$test_image_tag"
docker save --output "$fixture_dir/alpine-image.tar" "$test_image_tag"
chmod 0644 "$fixture_dir/alpine-image.tar"

container_id=$(docker create --platform "linux/$host_arch" \
  --label "cautem.vm-test.owner=$owner" --entrypoint /bin/sh "$docker_dind_image" -c 'sleep 3600')
docker start "$container_id" >/dev/null
docker exec "$container_id" addgroup -g 1000 -S sandbox
docker exec "$container_id" adduser -D -H -u 1000 -G sandbox sandbox
docker exec "$container_id" mkdir -p /opt/cautem-test
docker cp "$fixture_dir/alpine-image.tar" "$container_id:/opt/cautem-test/alpine-image.tar"
docker stop "$container_id" >/dev/null
docker export --output "$fixture_dir/docker-engine-rootfs.tar" "$container_id"
docker rm "$container_id" >/dev/null
container_id=
chmod 0644 "$fixture_dir/docker-engine-rootfs.tar"

CAUTEM_VM_BUILD_DIR=$build_dir \
CAUTEM_VM_TEST_ROOTFS_TAR="$fixture_dir/docker-engine-rootfs.tar" \
CAUTEM_VM_DOCKER_ACCEPTANCE=1 \
CAUTEM_VM_TEST_NAME=TestLiveMicroVMNestedDocker \
"$script_dir/run-live-kvm-acceptance.sh"
