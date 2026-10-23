#!/usr/bin/env python3

import hashlib
import json
import struct
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


VERIFIER = Path(__file__).with_name("verify-vm-runtime-libraries.py")


def fake_elf(machine: int) -> bytes:
	header = bytearray(64)
	header[:4] = b"\x7fELF"
	header[4:6] = bytes((2, 1))
	struct.pack_into("<HH", header, 16, 3, machine)
	struct.pack_into("<H", header, 52, 64)
	return bytes(header)


class RuntimeLibraryVerifierTest(unittest.TestCase):
	def setUp(self):
		self.temporary = tempfile.TemporaryDirectory()
		self.addCleanup(self.temporary.cleanup)
		self.root = Path(self.temporary.name)
		self.runtime = self.root / "runtime"
		self.runtime.mkdir()
		for name in ("libkrun.so", "libkrunfw.so.5"):
			(self.runtime / name).write_bytes(fake_elf(62))
		self.lock = self.root / "approved-runtime.json"
		self.lock.write_text(json.dumps({
			"schema_version": 1,
			"host": "linux/amd64",
			"artifacts": {
				name: hashlib.sha256(fake_elf(62)).hexdigest()
				for name in ("libkrun.so", "libkrunfw.so.5")
			},
		}), encoding="utf-8")
		self.output = self.root / "reports" / "runtime-libraries.json"

	def run_verifier(self, arch="amd64"):
		if arch != "amd64":
			lock = json.loads(self.lock.read_text(encoding="utf-8"))
			lock["host"] = f"linux/{arch}"
			self.lock.write_text(json.dumps(lock), encoding="utf-8")
		return subprocess.run(
			[sys.executable, str(VERIFIER), str(self.runtime), arch, str(self.lock), str(self.output)],
			capture_output=True,
			text=True,
			check=False,
		)

	def test_records_architecture_and_exact_digests(self):
		result = self.run_verifier()
		self.assertEqual(result.returncode, 0, result.stderr)
		report = json.loads(self.output.read_text(encoding="utf-8"))
		self.assertEqual(report["host"], "linux/amd64")
		self.assertIn("operator-approved external lock", report["verification"])
		self.assertEqual(len(report["artifacts"]), 2)
		self.assertEqual(report["artifacts"][0]["sha256"], hashlib.sha256(fake_elf(62)).hexdigest())

	def test_rejects_wrong_elf_machine(self):
		result = self.run_verifier("arm64")
		self.assertNotEqual(result.returncode, 0)
		self.assertIn("expected 183 for arm64", result.stderr)

	def test_rejects_digest_mismatch(self):
		lock = json.loads(self.lock.read_text(encoding="utf-8"))
		lock["artifacts"]["libkrun.so"] = "0" * 64
		self.lock.write_text(json.dumps(lock), encoding="utf-8")
		result = self.run_verifier()
		self.assertNotEqual(result.returncode, 0)
		self.assertIn("does not match the operator-approved", result.stderr)

	def test_rejects_lock_for_another_host(self):
		lock = json.loads(self.lock.read_text(encoding="utf-8"))
		lock["host"] = "linux/arm64"
		self.lock.write_text(json.dumps(lock), encoding="utf-8")
		result = self.run_verifier()
		self.assertNotEqual(result.returncode, 0)
		self.assertIn("must be linux/amd64", result.stderr)

	def test_rejects_lock_resolving_inside_checkout(self):
		self.lock.unlink()
		self.lock.symlink_to(VERIFIER)
		result = self.run_verifier()
		self.assertNotEqual(result.returncode, 0)
		self.assertIn("must be outside the source checkout", result.stderr)

	def test_rejects_missing_library_and_removes_stale_report(self):
		self.output.parent.mkdir(parents=True)
		self.output.write_text("stale", encoding="utf-8")
		(self.runtime / "libkrunfw.so.5").unlink()
		result = self.run_verifier()
		self.assertNotEqual(result.returncode, 0)
		self.assertFalse(self.output.exists())

	def test_rejects_non_elf_library(self):
		(self.runtime / "libkrun.so").write_bytes(b"x" * 64)
		result = self.run_verifier()
		self.assertNotEqual(result.returncode, 0)
		self.assertIn("not an ELF object", result.stderr)


if __name__ == "__main__":
	unittest.main()
