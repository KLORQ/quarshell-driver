#!/usr/bin/env python3
"""Validate Linux libkrun ELF inputs against an operator-approved digest lock."""

import hashlib
import json
import os
import stat
import sys
from pathlib import Path


def fail(message: str) -> "NoReturn":
    raise SystemExit(f"VM runtime libraries: {message}")


if len(sys.argv) != 5:
    fail("usage: verify-vm-runtime-libraries.py RUNTIME_DIR HOST_ARCH TRUST_LOCK_JSON OUTPUT_JSON")

runtime_dir = Path(sys.argv[1])
host_arch = sys.argv[2]
trust_lock_path = Path(sys.argv[3])
output = Path(sys.argv[4])
try:
    output.unlink(missing_ok=True)
except OSError as error:
    fail(f"cannot remove stale evidence {output}: {error}")
if not trust_lock_path.is_absolute():
    fail("approved runtime digest lock path must be absolute")
checkout_root = Path(__file__).resolve().parent.parent
try:
    resolved_lock_path = trust_lock_path.resolve(strict=True)
    resolved_lock_path.relative_to(checkout_root)
except ValueError:
    pass
except OSError as error:
    fail(f"cannot resolve approved runtime digest lock {trust_lock_path}: {error}")
else:
    fail("approved runtime digest lock must be outside the source checkout")
try:
    lock_info = resolved_lock_path.stat()
except OSError as error:
    fail(f"cannot stat approved runtime digest lock {resolved_lock_path}: {error}")
if not stat.S_ISREG(lock_info.st_mode) or lock_info.st_mode & 0o022:
    fail("approved runtime digest lock must be a regular file not writable by group or others")
machines = {"amd64": 62, "arm64": 183}
if host_arch not in machines:
    fail(f"unsupported Linux architecture {host_arch!r}")

try:
    trust_lock = json.loads(trust_lock_path.read_text(encoding="utf-8"))
except (OSError, json.JSONDecodeError) as error:
    fail(f"cannot read approved runtime digest lock {trust_lock_path}: {error}")
if not isinstance(trust_lock, dict) or trust_lock.get("schema_version") != 1:
    fail("approved runtime digest lock must use schema_version 1")
if trust_lock.get("host") != f"linux/{host_arch}":
    fail(f"approved runtime digest lock host must be linux/{host_arch}")
expected = trust_lock.get("artifacts")
names = ("libkrun.so", "libkrunfw.so.5")
if not isinstance(expected, dict) or set(expected) != set(names):
    fail("approved runtime digest lock must contain exactly libkrun.so and libkrunfw.so.5")
for name, digest in expected.items():
    if not isinstance(digest, str) or len(digest) != 64 or any(char not in "0123456789abcdef" for char in digest):
        fail(f"approved runtime digest lock has an invalid SHA-256 for {name}")

records = []
for name in names:
    path = runtime_dir / name
    try:
        link_info = path.lstat()
        info = path.stat()
        data = path.read_bytes()
    except OSError as error:
        fail(f"cannot read {path}: {error}")
    if not stat.S_ISREG(info.st_mode) or info.st_size < 64:
        fail(f"{path} must resolve to a regular, non-empty ELF file")
    if data[:4] != b"\x7fELF":
        fail(f"{path} is not an ELF object")
    if data[4] != 2 or data[5] != 1:
        fail(f"{path} must be a little-endian ELF64 object")
    if int.from_bytes(data[52:54], "little") != 64:
        fail(f"{path} has an invalid ELF64 header size")
    elf_type = int.from_bytes(data[16:18], "little")
    machine = int.from_bytes(data[18:20], "little")
    if elf_type != 3:
        fail(f"{path} has ELF type {elf_type}, expected ET_DYN (3)")
    if machine != machines[host_arch]:
        fail(f"{path} has ELF machine {machine}, expected {machines[host_arch]} for {host_arch}")
    digest = hashlib.sha256(data).hexdigest()
    if digest != expected[name]:
        fail(f"{path} SHA-256 does not match the operator-approved runtime digest lock")
    record = {
        "path": name,
        "sha256": digest,
        "elf_class": 64,
        "elf_type": "ET_DYN",
        "machine": "EM_X86_64" if host_arch == "amd64" else "EM_AARCH64",
    }
    if stat.S_ISLNK(link_info.st_mode):
        record["symlink_target"] = os.readlink(path)
    records.append(record)

report = {
    "schema_version": 1,
    "host": f"linux/{host_arch}",
    "verification": "ELF format, architecture, and SHA-256 match against an operator-approved external lock; upstream signature/provenance is not asserted",
    "trust_lock": str(trust_lock_path),
    "artifacts": records,
}
try:
    output.parent.mkdir(parents=True, exist_ok=True)
    temporary = output.with_name(output.name + ".tmp")
    temporary.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    temporary.replace(output)
except OSError as error:
    fail(f"cannot write runtime library evidence {output}: {error}")
print(f"VM runtime libraries verified: {len(records)} ELF files, linux/{host_arch}")
