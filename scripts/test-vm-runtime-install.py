#!/usr/bin/env python3
import hashlib, json, os, pathlib, subprocess, sys, tempfile

script_dir = pathlib.Path(__file__).resolve().parent
install = script_dir / "install-vm-runtime.sh"
rollback = script_dir / "rollback-vm-runtime.sh"
host = subprocess.check_output(["go", "env", "GOOS"], text=True).strip() + "/" + subprocess.check_output(["go", "env", "GOARCH"], text=True).strip()
arch = subprocess.check_output(["go", "env", "GOARCH"], text=True).strip()
names = [
    "quarshell-vm-worker",
    f"quarshell-vm-umoci-linux-{arch}",
    f"quarshell-vm-guest-agent-linux-{arch}",
    f"quarshell-init-linux-{arch}",
    f"quarshell-supervisor-linux-{arch}",
    f"quarshell-sshd-linux-{arch}",
    f"quarshell-vm-security-probe-linux-{arch}",
]

def bundle(root: pathlib.Path, marker: str) -> pathlib.Path:
    root.mkdir(parents=True)
    artifacts = []
    for name in names:
        path = root / name
        path.write_text(f"#!/bin/sh\necho {marker}\n")
        path.chmod(0o755)
        artifacts.append({"path": name, "sha256": hashlib.sha256(path.read_bytes()).hexdigest(), "executable": True})
    manifest = {"schema_version": 1, "vm_control_protocol": 2, "host": host, "guest": f"linux/{arch}", "artifacts": artifacts}
    (root / "vm-runtime-manifest.json").write_text(json.dumps(manifest))
    return root

with tempfile.TemporaryDirectory() as temporary:
    temp = pathlib.Path(temporary)
    prefix = temp / "prefix"
    first = bundle(temp / "v1", "v1")
    subprocess.run([str(install), str(first), str(prefix)], check=True)
    current = (prefix / "current").resolve()
    assert (current / "quarshell-vm-worker").read_text().endswith("v1\n")
    second = bundle(temp / "v2", "v2")
    subprocess.run([str(install), str(second), str(prefix)], check=True)
    assert (prefix / "current").resolve() != current
    subprocess.run([str(rollback), str(prefix)], check=True)
    assert (prefix / "current").resolve() == current
    tampered = bundle(temp / "bad", "bad")
    (tampered / "quarshell-vm-worker").write_text("#!/bin/sh\nexit 1\n")
    if subprocess.run([str(install), str(tampered), str(temp / "prefix2")], capture_output=True).returncode == 0:
        raise SystemExit("tampered bundle accepted")
    linked = bundle(temp / "link", "link")
    victim = temp / "victim"
    victim.write_text("#!/bin/sh\n")
    victim.chmod(0o755)
    (linked / "quarshell-vm-worker").unlink()
    (linked / "quarshell-vm-worker").symlink_to(victim)
    if subprocess.run([str(install), str(linked), str(temp / "prefix3")], capture_output=True).returncode == 0:
        raise SystemExit("symlink artifact accepted")
print("vm runtime install/upgrade/rollback: PASS")
