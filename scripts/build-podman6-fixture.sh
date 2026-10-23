#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
module_dir=$(CDPATH= cd -- "$script_dir/.." && pwd)
image=${1:-}
if [ -z "$image" ] || [ "$#" -ne 1 ]; then
  echo "usage: build-podman6-fixture.sh IMAGE_TAG" >&2
  exit 2
fi
if ! command -v docker >/dev/null 2>&1; then
  echo "Docker CLI is required to build the Podman 6 test fixture" >&2
  exit 2
fi
if ! docker info >/dev/null 2>&1; then
  echo "Docker daemon is unavailable" >&2
  exit 2
fi

platform=${QUARSHELL_PODMAN_FIXTURE_PLATFORM:-}
if [ -n "$platform" ]; then
  case "$platform" in linux/amd64|linux/arm64) ;; *) echo "unsupported Podman fixture platform: $platform" >&2; exit 2 ;; esac
  docker build --pull --platform "$platform" -f "$script_dir/../tests/e2e/podman/Containerfile.podman6" -t "$image" "$module_dir"
  version=$(docker run --rm --platform "$platform" "$image" podman --version)
else
  docker build --pull -f "$script_dir/../tests/e2e/podman/Containerfile.podman6" -t "$image" "$module_dir"
  version=$(docker run --rm "$image" podman --version)
fi
if [ "$version" != "podman version 6.1.4" ]; then
  echo "Podman 6 fixture must contain version 6.1.4; found $version" >&2
  exit 1
fi
image_id=$(docker image inspect --format '{{.Id}}' "$image")
if ! printf '%s\n' "$image_id" | python3 -c 'import re,sys; raise SystemExit(0 if re.fullmatch(r"sha256:[0-9a-f]{64}", sys.stdin.read().strip()) else 1)'; then
  echo "Docker returned an invalid Podman fixture image ID: $image_id" >&2
  exit 1
fi
printf 'Podman fixture verified: %s (%s), %s\n' "$image" "$version" "$image_id"
if [ -n "${QUARSHELL_PODMAN_FIXTURE_DIGEST_FILE:-}" ]; then
  mkdir -p "$(dirname -- "$QUARSHELL_PODMAN_FIXTURE_DIGEST_FILE")"
  temporary="$QUARSHELL_PODMAN_FIXTURE_DIGEST_FILE.tmp"
  printf '%s\n' "$image_id" >"$temporary"
  mv -f "$temporary" "$QUARSHELL_PODMAN_FIXTURE_DIGEST_FILE"
fi
