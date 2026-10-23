#!/usr/bin/env python3

import json
import re
import unittest
from pathlib import Path


LOCK = Path(__file__).with_name("vm-engine-fixtures.lock.json")
DIGEST_REF = re.compile(r"^[^@]+@sha256:[0-9a-f]{64}$")


class VMEngineFixtureLockTest(unittest.TestCase):
	def test_all_container_fixtures_are_digest_pinned(self):
		lock = json.loads(LOCK.read_text(encoding="utf-8"))
		self.assertEqual(lock["schema_version"], 1)
		for key in ("docker_dind", "test_image", "podman_legacy_negative"):
			with self.subTest(key=key):
				self.assertRegex(lock[key], DIGEST_REF)

	def test_legacy_podman_negative_lane_is_explicitly_versioned(self):
		lock = json.loads(LOCK.read_text(encoding="utf-8"))
		self.assertTrue(lock["podman_legacy_negative"].startswith("quay.io/podman/stable:v5.8.7@sha256:"))


if __name__ == "__main__":
	unittest.main()
