#!/usr/bin/env python3
"""Verify the exact file set and hashes in a built VM runtime manifest."""

import hashlib
import json
import re
import stat
import sys
from pathlib import Path


def fail(message: str) -> "NoReturn":
    raise SystemExit(f"VM runtime manifest: {message}")


if len(sys.argv) != 4:
    fail("usage: verify-vm-runtime-manifest.py MANIFEST HOST_PLATFORM GUEST_ARCH")

manifest_path = Path(sys.argv[1])
expected_host = sys.argv[2]
expected_guest_arch = sys.argv[3]
try:
    raw = json.loads(manifest_path.read_text(encoding="utf-8"))
except (OSError, json.JSONDecodeError) as error:
    fail(f"cannot read valid JSON: {error}")

if set(raw) != {"schema_version", "vm_control_protocol", "host", "guest", "artifacts"}:
    fail("unexpected or missing top-level fields")
if raw["schema_version"] != 1 or raw["vm_control_protocol"] != 2 or raw["host"] != expected_host or raw["guest"] != f"linux/{expected_guest_arch}":
    fail("manifest version, VM control protocol, or platform does not match the requested build")

expected_names = {
    "cautem-vm-worker",
    f"cautem-vm-umoci-linux-{expected_guest_arch}",
    f"cautem-vm-guest-agent-linux-{expected_guest_arch}",
    f"cautem-init-linux-{expected_guest_arch}",
    f"cautem-supervisor-linux-{expected_guest_arch}",
    f"cautem-sshd-linux-{expected_guest_arch}",
    f"cautem-vm-security-probe-linux-{expected_guest_arch}",
}
records = raw["artifacts"]
if not isinstance(records, list):
    fail("artifacts must be a list")
seen: set[str] = set()
root = manifest_path.parent
for record in records:
    if not isinstance(record, dict) or set(record) != {"path", "sha256", "executable"}:
        fail("artifact entry has unexpected or missing fields")
    name = record["path"]
    digest = record["sha256"]
    if not isinstance(name, str) or name in seen or name not in expected_names:
        fail(f"unexpected or duplicate artifact path: {name!r}")
    if not isinstance(digest, str) or not re.fullmatch(r"[0-9a-f]{64}", digest):
        fail(f"invalid SHA-256 for {name}")
    if record["executable"] is not True:
        fail(f"artifact is not marked executable: {name}")
    path = root / name
    try:
        info = path.lstat()
    except OSError as error:
        fail(f"cannot inspect {name}: {error}")
    if not stat.S_ISREG(info.st_mode) or not info.st_mode & 0o111:
        fail(f"artifact is not a regular executable file: {name}")
    actual = hashlib.sha256(path.read_bytes()).hexdigest()
    if actual != digest:
        fail(f"SHA-256 mismatch for {name}")
    seen.add(name)

if seen != expected_names:
    fail(f"artifact set differs; missing={sorted(expected_names - seen)}")

print(f"VM runtime manifest verified: {len(seen)} artifacts, {raw['host']} -> {raw['guest']}")
