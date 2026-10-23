#!/usr/bin/env python3

import runpy
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


validate_events = runpy.run_path(str(Path(__file__).with_name("run-required-go-test.py")))["validate_events"]
redact_artifact_value = runpy.run_path(str(Path(__file__).with_name("run-required-go-test.py")))["redact_artifact_value"]
TEST = "TestLiveMicroVMLifecycle"
PACKAGE = "github.com/KLORQ/quarshell-driver-vm/vm"


def passing_events():
	return [
		{"Action": "run", "Package": PACKAGE, "Test": TEST},
		{"Action": "pass", "Package": PACKAGE, "Test": TEST},
		{"Action": "pass", "Package": PACKAGE},
	]


class RequiredGoTestTest(unittest.TestCase):
	def test_accepts_required_test_that_ran_and_passed(self):
		self.assertEqual(validate_events(passing_events(), {TEST}), [])

	def test_rejects_package_pass_when_test_was_not_selected(self):
		problems = validate_events([{"Action": "pass", "Package": PACKAGE}], {TEST})
		self.assertIn(f"required test {TEST} did not run", problems)

	def test_rejects_skipped_required_test(self):
		events = passing_events() + [{"Action": "skip", "Package": PACKAGE, "Test": TEST}]
		problems = validate_events(events, {TEST})
		self.assertIn(f"required test {TEST} or one of its subtests was skipped", problems)

	def test_rejects_skipped_subtest(self):
		events = passing_events() + [{"Action": "skip", "Package": PACKAGE, "Test": TEST + "/optional"}]
		problems = validate_events(events, {TEST})
		self.assertIn(f"required test {TEST} or one of its subtests was skipped", problems)

	def test_rejects_unexpected_test(self):
		events = passing_events() + [{"Action": "run", "Package": PACKAGE, "Test": "TestOther"}]
		problems = validate_events(events, {TEST})
		self.assertIn("unexpected test TestOther ran outside the required manifest", problems)

	def test_writes_json_events_to_requested_artifact(self):
		with tempfile.TemporaryDirectory() as directory:
			artifact = Path(directory) / "nested" / "events.jsonl"
			payload = "\n".join(json.dumps(event) for event in passing_events())
			command = [
				sys.executable,
				str(Path(__file__).with_name("run-required-go-test.py")),
				"--test", TEST, "--", sys.executable, "-c", f"print({payload!r})",
			]
			environment = os.environ.copy()
			environment["CAUTEM_VM_TEST_JSON_LOG"] = str(artifact)
			result = subprocess.run(command, env=environment, capture_output=True, text=True, check=False)
			self.assertEqual(result.returncode, 0, result.stderr)
			written = [json.loads(line) for line in artifact.read_text(encoding="utf-8").splitlines()]
			self.assertEqual(written, passing_events())

	def test_redacts_sensitive_values_from_artifact_strings(self):
		redacted = redact_artifact_value({"Output": "token=abc123 Authorization: Bearer abc.def password hunter2"})
		self.assertNotIn("abc123", redacted["Output"])
		self.assertNotIn("abc.def", redacted["Output"])
		self.assertNotIn("hunter2", redacted["Output"])


if __name__ == "__main__":
	unittest.main()
