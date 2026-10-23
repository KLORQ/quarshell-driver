#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
temporary_dir=$(mktemp -d "${TMPDIR:-/tmp}/cautem-podman-fixture-test.XXXXXX")
trap 'rm -rf "$temporary_dir"' EXIT HUP INT TERM
mkdir "$temporary_dir/bin"
cat >"$temporary_dir/bin/docker" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >>"$CAUTEM_TEST_DOCKER_LOG"
case "${1:-}" in
  info) exit 0 ;;
  build) exit 0 ;;
  run)
    if [ "${CAUTEM_TEST_WRONG_PODMAN_VERSION:-0}" = 1 ]; then
      echo 'podman version 6.1.3'
    else
      echo 'podman version 6.1.4'
    fi
    ;;
  image)
    [ "${2:-}" = inspect ] || exit 2
    echo "${CAUTEM_TEST_IMAGE_ID:-sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}"
    ;;
  *) echo "unexpected docker invocation: $*" >&2; exit 2 ;;
esac
EOF
chmod 0755 "$temporary_dir/bin/docker"
export CAUTEM_TEST_DOCKER_LOG="$temporary_dir/docker.log"
export PATH="$temporary_dir/bin:$PATH"

CAUTEM_PODMAN_FIXTURE_PLATFORM=linux/arm64 \
CAUTEM_PODMAN_FIXTURE_DIGEST_FILE="$temporary_dir/evidence/image-id.txt" \
  "$script_dir/build-podman6-fixture.sh" cautem-podman-test:arm64 >"$temporary_dir/build.log"
grep -F -- 'build --pull --platform linux/arm64' "$CAUTEM_TEST_DOCKER_LOG" >/dev/null
grep -F -- 'run --rm --platform linux/arm64 cautem-podman-test:arm64 podman --version' "$CAUTEM_TEST_DOCKER_LOG" >/dev/null
test "$(cat "$temporary_dir/evidence/image-id.txt")" = sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
grep -F -- 'Podman fixture verified: cautem-podman-test:arm64 (podman version 6.1.4)' "$temporary_dir/build.log" >/dev/null

: >"$CAUTEM_TEST_DOCKER_LOG"
if CAUTEM_TEST_WRONG_PODMAN_VERSION=1 \
  "$script_dir/build-podman6-fixture.sh" cautem-podman-test:wrong >"$temporary_dir/wrong-version.log" 2>&1; then
  echo "Podman fixture builder accepted a mismatched engine version" >&2
  exit 1
fi
grep -F -- 'must contain version 6.1.4' "$temporary_dir/wrong-version.log" >/dev/null
if grep -F -- 'image inspect' "$CAUTEM_TEST_DOCKER_LOG" >/dev/null; then
  echo "Podman fixture builder recorded an image ID for an unapproved engine version" >&2
  exit 1
fi

echo "Podman fixture builder contract: PASS"
