#!/bin/sh
set -eu
unset GOROOT

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
module_dir=$(CDPATH= cd -- "$script_dir/.." && pwd)
workspace_dir=$(CDPATH= cd -- "$module_dir/.." && pwd)
runtime_module_dir="$workspace_dir/quarshell-runtime"
vm_module_dir="$workspace_dir/quarshell-driver-vm"
vm_control_protocol_file="$runtime_module_dir/supervisorcontrol/protocol_version.txt"
output_dir=${CAUTEM_VM_BUILD_DIR:-"$module_dir/dist/vm"}
guest_arch=${CAUTEM_VM_GUEST_ARCH:-$(go env GOARCH)}
host_os=$(go env GOOS)
host_arch=$(go env GOARCH)

case "$host_os/$host_arch" in
  linux/amd64|linux/arm64|darwin/arm64) ;;
  *) echo "unsupported libkrun worker host: $host_os/$host_arch" >&2; exit 1 ;;
esac
case "$guest_arch" in
  amd64|arm64) ;;
  *) echo "unsupported Linux guest architecture: $guest_arch" >&2; exit 1 ;;
esac
if [ ! -f "$vm_control_protocol_file" ] || [ -L "$vm_control_protocol_file" ]; then
  echo "VM control protocol version file is missing or unsafe: $vm_control_protocol_file" >&2
  exit 1
fi
vm_control_protocol=$(tr -d '\r\n' <"$vm_control_protocol_file")
case "$vm_control_protocol" in
  ''|*[!0-9]*) echo "VM control protocol version must be a positive integer" >&2; exit 1 ;;
esac
if [ "$vm_control_protocol" -lt 1 ]; then
  echo "VM control protocol version must be a positive integer" >&2
  exit 1
fi

mkdir -p "$output_dir"
output_dir=$(CDPATH= cd -- "$output_dir" && pwd)

umoci_path=${CAUTEM_VM_UMOCI:-}
umoci_sha256=${CAUTEM_VM_UMOCI_SHA256:-}
umoci_tmp=
manifest_tmp=
cleanup_umoci_tmp() {
  if [ -n "$umoci_tmp" ]; then
    rm -f "$umoci_tmp"
  fi
  if [ -n "$manifest_tmp" ]; then
    rm -f "$manifest_tmp"
  fi
}
trap cleanup_umoci_tmp EXIT
trap 'cleanup_umoci_tmp; exit 129' HUP
trap 'cleanup_umoci_tmp; exit 130' INT
trap 'cleanup_umoci_tmp; exit 143' TERM
# A manifest marks a complete bundle. Remove an older marker before changing
# any artifact so a failed build cannot leave a stale success marker.
rm -f "$output_dir/vm-runtime-manifest.json"
if [ -z "$umoci_path" ]; then
  umoci_version=${CAUTEM_VM_UMOCI_VERSION:-v0.6.0}
  case "$guest_arch" in
    amd64) umoci_release_arch=amd64 ;;
    arm64) umoci_release_arch=arm64 ;;
  esac
  umoci_path="$output_dir/cautem-vm-umoci-linux-$guest_arch"
  if [ -z "$umoci_sha256" ]; then
    umoci_sha256=$(awk -v version="$umoci_version" -v arch="$guest_arch" '$1 == version && $2 == arch { print $3 }' "$script_dir/vm-runtime-artifacts.lock")
  fi
  case "$umoci_sha256" in
    *[!0123456789abcdef]*|'') echo "no valid pinned umoci SHA-256 for $umoci_version/linux-$guest_arch; add it to vm-runtime-artifacts.lock or set CAUTEM_VM_UMOCI_SHA256" >&2; exit 1 ;;
  esac
  if [ "${#umoci_sha256}" -ne 64 ]; then
    echo "umoci SHA-256 must contain exactly 64 lowercase hexadecimal digits" >&2
    exit 1
  fi
  umoci_tmp=$(mktemp "$output_dir/.cautem-vm-umoci.XXXXXX")
  umoci_url="https://github.com/opencontainers/umoci/releases/download/$umoci_version/umoci.linux.$umoci_release_arch"
  curl --fail --location --silent --show-error "$umoci_url" --output "$umoci_tmp"
else
  if [ -z "$umoci_sha256" ]; then
    echo "CAUTEM_VM_UMOCI_SHA256 is required when supplying CAUTEM_VM_UMOCI" >&2
    exit 1
  fi
  case "$umoci_sha256" in
    *[!0123456789abcdef]*|'') echo "CAUTEM_VM_UMOCI_SHA256 must be lowercase hexadecimal" >&2; exit 1 ;;
  esac
  if [ "${#umoci_sha256}" -ne 64 ]; then
    echo "CAUTEM_VM_UMOCI_SHA256 must contain exactly 64 lowercase hexadecimal digits" >&2
    exit 1
  fi
  umoci_tmp=$(mktemp "$output_dir/.cautem-vm-umoci.XXXXXX")
  cp "$umoci_path" "$umoci_tmp"
fi
if [ ! -f "$umoci_path" ] || [ ! -x "$umoci_path" ] || [ -L "$umoci_path" ]; then
  if [ -n "${CAUTEM_VM_UMOCI:-}" ]; then
    echo "guest umoci must be an executable regular file: $umoci_path" >&2
    exit 1
  fi
fi
if [ ! -f "$umoci_tmp" ] || [ ! -s "$umoci_tmp" ]; then
  echo "guest umoci download or staged binary is missing or empty" >&2
  exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
  actual_umoci_sha256=$(sha256sum "$umoci_tmp" | awk '{print $1}')
else
  actual_umoci_sha256=$(shasum -a 256 "$umoci_tmp" | awk '{print $1}')
fi
if [ "$actual_umoci_sha256" != "$umoci_sha256" ]; then
  echo "guest umoci SHA-256 mismatch: expected $umoci_sha256, got $actual_umoci_sha256" >&2
  exit 1
fi
chmod 0755 "$umoci_tmp"
mv -f "$umoci_tmp" "$output_dir/cautem-vm-umoci-linux-$guest_arch"
umoci_tmp=
umoci_path="$output_dir/cautem-vm-umoci-linux-$guest_arch"

(
  cd "$module_dir"
  CGO_ENABLED=0 go -C "$vm_module_dir" build -trimpath -o "$output_dir/cautem-vm-worker" ./cmd/cautem-vm-worker
)
for helper in cautem-init cautem-supervisor cautem-sshd; do
  CGO_ENABLED=0 GOOS=linux GOARCH="$guest_arch" go -C "$runtime_module_dir" build \
    -trimpath -ldflags="-s -w" -o "$output_dir/$helper-linux-$guest_arch" "./cmd/$helper"
done
CGO_ENABLED=0 GOOS=linux GOARCH="$guest_arch" go -C "$vm_module_dir" build \
  -trimpath -ldflags="-s -w" \
  -o "$output_dir/cautem-vm-security-probe-linux-$guest_arch" \
  ./vm/testdata/guest-security-probe
CGO_ENABLED=0 GOOS=linux GOARCH="$guest_arch" go -C "$vm_module_dir" build \
  -trimpath -ldflags="-s -w" \
  -o "$output_dir/cautem-vm-guest-agent-linux-$guest_arch" \
  ./cmd/cautem-vm-guest-agent
chmod 0755 "$output_dir/cautem-vm-worker" "$output_dir/cautem-vm-guest-agent-linux-$guest_arch" "$output_dir/cautem-vm-umoci-linux-$guest_arch"
chmod 0755 "$output_dir/cautem-init-linux-$guest_arch" "$output_dir/cautem-supervisor-linux-$guest_arch" "$output_dir/cautem-sshd-linux-$guest_arch"
chmod 0755 "$output_dir/cautem-vm-security-probe-linux-$guest_arch"
if [ "$host_os/$host_arch" = darwin/arm64 ]; then
  codesign_identity=${CAUTEM_VM_CODESIGN_IDENTITY:--}
  codesign --force --sign "$codesign_identity" \
    --entitlements "$script_dir/vm-worker-entitlements.plist" \
    "$output_dir/cautem-vm-worker"
  expected_team_id=${CAUTEM_VM_WORKER_TEAM_ID:-}
  if [ -n "$expected_team_id" ]; then
    codesign --verify --strict --verbose=2 "$output_dir/cautem-vm-worker"
    signature_metadata=$(codesign -dv --verbose=4 "$output_dir/cautem-vm-worker" 2>&1)
    actual_team_id=$(printf '%s\n' "$signature_metadata" | sed -n 's/^TeamIdentifier=//p')
    if [ -z "$actual_team_id" ] || [ "$actual_team_id" = "not set" ] || [ "$actual_team_id" != "$expected_team_id" ]; then
      echo "signed VM worker Team ID '$actual_team_id' does not match CAUTEM_VM_WORKER_TEAM_ID" >&2
      exit 1
    fi
  fi
fi

artifacts="cautem-vm-worker cautem-vm-umoci-linux-$guest_arch cautem-vm-guest-agent-linux-$guest_arch cautem-init-linux-$guest_arch cautem-supervisor-linux-$guest_arch cautem-sshd-linux-$guest_arch cautem-vm-security-probe-linux-$guest_arch"
manifest_tmp=$(mktemp "$output_dir/.vm-runtime-manifest.XXXXXX")
{
  printf '{\n  "schema_version": 1,\n  "vm_control_protocol": %s,\n  "host": "%s/%s",\n  "guest": "linux/%s",\n  "artifacts": [\n' "$vm_control_protocol" "$host_os" "$host_arch" "$guest_arch"
  separator=
  for artifact in $artifacts; do
    artifact_path="$output_dir/$artifact"
    if [ ! -f "$artifact_path" ] || [ ! -x "$artifact_path" ] || [ -L "$artifact_path" ]; then
      echo "runtime artifact is missing, non-executable or a symlink: $artifact_path" >&2
      exit 1
    fi
    if command -v sha256sum >/dev/null 2>&1; then
      artifact_sha256=$(sha256sum "$artifact_path" | awk '{print $1}')
    else
      artifact_sha256=$(shasum -a 256 "$artifact_path" | awk '{print $1}')
    fi
    if [ -n "$separator" ]; then
      printf ',\n'
    fi
    printf '    {"path": "%s", "sha256": "%s", "executable": true}' "$artifact" "$artifact_sha256"
    separator=1
  done
  printf '\n  ]\n}\n'
} >"$manifest_tmp"
mv -f "$manifest_tmp" "$output_dir/vm-runtime-manifest.json"
manifest_tmp=
echo "Built worker, linux/$guest_arch guest agent, umoci and runtime helpers in $output_dir"
