#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
temporary_dir=$(mktemp -d "${TMPDIR:-/tmp}/quarshell-vm-artifact-test.XXXXXX")
cleanup() { rm -rf "$temporary_dir"; }
trap cleanup EXIT
trap 'cleanup; exit 129' HUP
trap 'cleanup; exit 130' INT
trap 'cleanup; exit 143' TERM

mkdir -p "$temporary_dir/bin" "$temporary_dir/output"
cat >"$temporary_dir/bin/go" <<'EOF'
#!/bin/sh
if [ "${1:-}" = env ]; then
  case "${2:-}" in
    GOOS) echo linux ;;
    GOARCH) echo amd64 ;;
    *) exit 2 ;;
  esac
  exit 0
fi
echo "$*" >>"$QUARSHELL_TEST_GO_LOG"
output=
previous=
for argument in "$@"; do
  if [ "$previous" = output ]; then
    output=$argument
    break
  fi
  if [ "$argument" = -o ]; then
    previous=output
  fi
done
if [ -z "$output" ]; then
  echo "mock go build was not given -o" >&2
  exit 2
fi
printf 'test binary for %s\n' "$output" >"$output"
chmod 0755 "$output"
EOF
chmod 0755 "$temporary_dir/bin/go"

custom_umoci="$temporary_dir/custom-umoci"
cp /usr/bin/true "$custom_umoci"
chmod 0755 "$custom_umoci"
if command -v sha256sum >/dev/null 2>&1; then
  valid_digest=$(sha256sum "$custom_umoci" | awk '{print $1}')
else
  valid_digest=$(shasum -a 256 "$custom_umoci" | awk '{print $1}')
fi
invalid_digest=0000000000000000000000000000000000000000000000000000000000000000
published="$temporary_dir/output/quarshell-vm-umoci-linux-amd64"
printf '%s\n' keep-existing >"$published"

if PATH="$temporary_dir/bin:$PATH" \
  QUARSHELL_VM_BUILD_DIR="$temporary_dir/output" \
  QUARSHELL_VM_GUEST_ARCH=amd64 \
  QUARSHELL_VM_UMOCI="$custom_umoci" \
  QUARSHELL_VM_UMOCI_SHA256="$invalid_digest" \
  "$script_dir/build-vm-runtime.sh" >"$temporary_dir/invalid.log" 2>&1; then
  echo "runtime build accepted an umoci digest mismatch" >&2
  exit 1
fi
if [ "$(cat "$published")" != keep-existing ]; then
  echo "digest mismatch replaced the previously published umoci" >&2
  exit 1
fi
if find "$temporary_dir/output" -name '.quarshell-vm-umoci.*' -print | grep . >/dev/null; then
  echo "digest mismatch left a temporary umoci artifact" >&2
  exit 1
fi

PATH="$temporary_dir/bin:$PATH" \
QUARSHELL_TEST_GO_LOG="$temporary_dir/go.log" \
QUARSHELL_VM_BUILD_DIR="$temporary_dir/output" \
QUARSHELL_VM_GUEST_ARCH=amd64 \
QUARSHELL_VM_UMOCI="$custom_umoci" \
QUARSHELL_VM_UMOCI_SHA256="$valid_digest" \
"$script_dir/build-vm-runtime.sh" >"$temporary_dir/valid.log" 2>&1 || {
  cat "$temporary_dir/valid.log" >&2
  echo "verified artifact did not complete the mocked runtime build" >&2
  exit 1
}
if [ ! -x "$published" ] || [ ! -s "$published" ]; then
  echo "verified umoci was not atomically published as executable" >&2
  exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
  published_digest=$(sha256sum "$published" | awk '{print $1}')
else
  published_digest=$(shasum -a 256 "$published" | awk '{print $1}')
fi
if [ "$published_digest" != "$valid_digest" ]; then
  echo "published umoci digest differs from the verified input" >&2
  exit 1
fi

"$script_dir/verify-vm-runtime-manifest.py" "$temporary_dir/output/vm-runtime-manifest.json" linux/amd64 amd64
python3 - "$temporary_dir/output/vm-runtime-manifest.json" "$temporary_dir/old-protocol.json" <<'PY'
import json
import pathlib
import sys

source, target = map(pathlib.Path, sys.argv[1:])
manifest = json.loads(source.read_text(encoding="utf-8"))
manifest["vm_control_protocol"] = 1
target.write_text(json.dumps(manifest), encoding="utf-8")
PY
if "$script_dir/verify-vm-runtime-manifest.py" "$temporary_dir/old-protocol.json" linux/amd64 amd64 >"$temporary_dir/old-protocol.log" 2>&1; then
  echo "runtime manifest accepted an obsolete VM control protocol" >&2
  exit 1
fi
if "$script_dir/verify-vm-runtime-manifest.py" "$temporary_dir/output/vm-runtime-manifest.json" linux/arm64 arm64 >"$temporary_dir/platform-mismatch.log" 2>&1; then
  echo "runtime manifest accepted a mismatched host/guest architecture" >&2
  exit 1
fi
missing="$temporary_dir/output/quarshell-vm-guest-agent-linux-amd64"
mv "$missing" "$temporary_dir/missing-guest-agent"
if "$script_dir/verify-vm-runtime-manifest.py" "$temporary_dir/output/vm-runtime-manifest.json" linux/amd64 amd64 >"$temporary_dir/missing.log" 2>&1; then
  echo "runtime manifest accepted a missing guest agent" >&2
  exit 1
fi
mv "$temporary_dir/missing-guest-agent" "$missing"
printf 'tampered\n' >>"$temporary_dir/output/quarshell-vm-worker"
if "$script_dir/verify-vm-runtime-manifest.py" "$temporary_dir/output/vm-runtime-manifest.json" linux/amd64 amd64 >"$temporary_dir/tampered.log" 2>&1; then
  echo "runtime manifest accepted a tampered worker" >&2
  exit 1
fi

echo "VM runtime artifact digest checks: PASS"
