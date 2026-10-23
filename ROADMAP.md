# Roadmap — quarshell-driver

Status: pre-stable API · Current module tag: `v0.1.1` · Dependencies are listed in `go.mod`.

## This module

| ID | Item | Notes |
|----|------|-------|
| D1 | **Kubernetes driver** | Experimental Agent Sandbox lifecycle, PVC workspace, runtime and proxy Pods; direct exec/log/copy remains unsupported |
| D2 | **MicroVM driver** | Experimental libkrun lifecycle, per-VM policy/network broker, guest supervisor and SSH relay; safe host workspace isolation and live Linux KVM acceptance remain open |
| D3 | **GPU / CDI polish** | Inventory-based device pick; Podman parity |
| D4 | **Windows build matrix** | Keep SIGWINCH / TTY paths build-tagged clean |


## Release

This module follows its own pre-stable 0.x version sequence. Publish dependencies
first, update consumers to the published tags, and verify with `GOWORK=off`. Never
rewrite a published tag.
