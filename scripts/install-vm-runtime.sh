#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
if [ "$#" -ne 2 ]; then
  echo "usage: install-vm-runtime.sh BUNDLE_DIR PREFIX" >&2
  exit 2
fi
bundle=$1
prefix=$2
if [ ! -d "$bundle" ] || [ -L "$bundle" ]; then
  echo "bundle must be a real directory: $bundle" >&2
  exit 1
fi
manifest="$bundle/vm-runtime-manifest.json"
if [ ! -f "$manifest" ] || [ -L "$manifest" ]; then
  echo "bundle manifest is missing or unsafe: $manifest" >&2
  exit 1
fi
python3 "$script_dir/verify-vm-runtime-manifest.py" "$manifest" "$(go env GOOS)/$(go env GOARCH)" "$(go env GOARCH)" >/dev/null
version=$(python3 - "$manifest" <<'PY'
import hashlib, sys
print(hashlib.sha256(open(sys.argv[1], 'rb').read()).hexdigest()[:16])
PY
)
mkdir -p "$prefix/versions"
dest="$prefix/versions/$version"
if [ -e "$dest" ] || [ -L "$dest" ]; then
  echo "version already installed: $version" >&2
  exit 1
fi
staging=$(mktemp -d "$prefix/.install.XXXXXX")
trap 'rm -rf "$staging"' EXIT HUP INT TERM
cp "$manifest" "$staging/vm-runtime-manifest.json"
python3 - "$manifest" "$bundle" "$staging" <<'PY'
import json, os, shutil, sys
manifest, bundle, staging = sys.argv[1:]
data = json.load(open(manifest, encoding="utf-8"))
for artifact in data.get("artifacts", []):
    name = artifact["path"]
    source = os.path.join(bundle, name)
    if os.path.islink(source) or not os.path.isfile(source):
        raise SystemExit(f"unsafe artifact: {source}")
    shutil.copy2(source, os.path.join(staging, name))
    os.chmod(os.path.join(staging, name), 0o755)
PY
mv "$staging" "$dest"
trap - EXIT HUP INT TERM
previous=
if [ -L "$prefix/current" ]; then
  previous=$(readlink "$prefix/current")
fi
if [ -n "$previous" ]; then
  printf '%s\n' "$previous" > "$prefix/previous"
fi
ln -sfn "$dest" "$prefix/current"
echo "installed vm runtime $version at $dest"
