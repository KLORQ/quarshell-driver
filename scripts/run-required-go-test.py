#!/usr/bin/env python3
"""Run a Go test command and require named tests to execute and pass."""

from __future__ import annotations

import json
import os
import re
import subprocess
import sys
from collections import defaultdict
from pathlib import Path

SENSITIVE_VALUE = re.compile(
	r"(?i)\b(authorization|password|token|secret|credential)(\s*[:=]\s*|\s+)[^\s,;\"']+"
)
BEARER_VALUE = re.compile(r"(?i)\b(Bearer\s+)[A-Za-z0-9._~+/-]+=*")


def redact_artifact_value(value):
	if isinstance(value, str):
		value = BEARER_VALUE.sub(r"\1[REDACTED]", value)
		return SENSITIVE_VALUE.sub(r"\1\2[REDACTED]", value)
	if isinstance(value, list):
		return [redact_artifact_value(item) for item in value]
	if isinstance(value, dict):
		return {key: redact_artifact_value(item) for key, item in value.items()}
	return value


def validate_events(events: list[dict], required: set[str]) -> list[str]:
	seen: dict[str, set[str]] = defaultdict(set)
	package_pass = False
	unexpected: set[str] = set()
	for event in events:
		action = event.get("Action")
		name = event.get("Test")
		if not name:
			if event.get("Package") and action == "pass":
				package_pass = True
			continue
		root = name.split("/", 1)[0]
		if action in {"run", "pass", "fail", "skip"}:
			if root not in required:
				unexpected.add(root)
			else:
				seen[root].add(action)

	problems: list[str] = []
	for name in sorted(required):
		actions = seen[name]
		if "run" not in actions:
			problems.append(f"required test {name} did not run")
		if "pass" not in actions:
			problems.append(f"required test {name} did not pass")
		if "skip" in actions:
			problems.append(f"required test {name} or one of its subtests was skipped")
		if "fail" in actions:
			problems.append(f"required test {name} or one of its subtests failed")
	for name in sorted(unexpected):
		problems.append(f"unexpected test {name} ran outside the required manifest")
	if not package_pass:
		problems.append("go test package did not report PASS")
	return problems


def main(argv: list[str]) -> int:
	if len(argv) < 4 or argv[1] != "--test" or "--" not in argv[2:]:
		print("usage: run-required-go-test.py --test TEST [--test TEST ...] -- COMMAND ...", file=sys.stderr)
		return 2
	separator = argv.index("--", 2)
	required = {argv[index + 1] for index in range(1, separator, 2) if argv[index] == "--test"}
	command = argv[separator + 1 :]
	if not required or not command:
		print("at least one required test and a command are required", file=sys.stderr)
		return 2
	json_log = os.environ.get("QUARSHELL_VM_TEST_JSON_LOG", "").strip()
	json_log_file = None
	if json_log:
		try:
			path = Path(json_log)
			path.parent.mkdir(parents=True, exist_ok=True)
			json_log_file = path.open("w", encoding="utf-8")
		except OSError as error:
			print(f"cannot create Go test event artifact {json_log}: {error}", file=sys.stderr)
			return 2

	events: list[dict] = []
	try:
		process = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, bufsize=1)
		events = []
		assert process.stdout is not None
		for line in process.stdout:
			try:
				event = json.loads(line)
			except json.JSONDecodeError:
				sys.stdout.write(line)
				continue
			events.append(event)
			if json_log_file is not None:
				redacted = redact_artifact_value(event)
				json_log_file.write(json.dumps(redacted, separators=(",", ":"), ensure_ascii=False) + "\n")
				json_log_file.flush()
			if event.get("Action") == "output":
				sys.stdout.write(event.get("Output", ""))
				sys.stdout.flush()
		return_code = process.wait()
	finally:
		if json_log_file is not None:
			json_log_file.close()
	problems = validate_events(events, required)
	if return_code != 0:
		problems.insert(0, f"go test exited with status {return_code}")
	if problems:
		for problem in problems:
			print(f"required Go test gate: FAIL: {problem}", file=sys.stderr)
		return return_code if return_code != 0 else 1
	print("required Go test gate: PASS (" + ", ".join(sorted(required)) + ")")
	return 0


if __name__ == "__main__":
	raise SystemExit(main(sys.argv))
