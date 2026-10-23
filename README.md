<h1 align="center">quarshell-driver</h1>

<p align="center">
  <strong>Compute drivers</strong><br>
  Docker-first sandbox runtime — mounts, ExtraHosts, egress sidecar wiring.
</p>
<p align="center">
  <a href="https://github.com/KLORQ/quarshell-driver/actions/workflows/ci.yml"><img src="https://github.com/KLORQ/quarshell-driver/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://pkg.go.dev/github.com/KLORQ/quarshell-driver"><img src="https://pkg.go.dev/badge/github.com/KLORQ/quarshell-driver.svg" alt="Go Reference"></a>
  <a href="https://www.apache.org/licenses/LICENSE-2.0"><img src="https://img.shields.io/badge/License-Apache--2.0-blue.svg" alt="License"></a>
  <a href="https://github.com/KLORQ/quarshell-driver"><img src="https://img.shields.io/badge/Go-1.27+-00ADD8?logo=go" alt="Go Version"></a>
</p>
<p align="center">
  <sub>Part of the <a href="https://github.com/KLORQ">cautem / cautem</a> ecosystem</sub>
</p>

---

## Overview

Backend setup and runtime limits are maintained in the [Docker](https://klorq.github.io/sandbox.dev/providers/docker/) and [Podman](https://klorq.github.io/sandbox.dev/providers/podman/) guides. The [OpenShell compatibility page](https://klorq.github.io/sandbox.dev/reference/openshell-compatibility/) records the current comparison scope.

**quarshell-driver** implements modular `Backend` contracts for cautem. Docker and Podman provide the local container lifecycle; Kubernetes provisions Agent Sandbox workloads and a paired Cautem proxy companion. The Kubernetes backend remains experimental until the target cluster passes the live runtime and NetworkPolicy checks in the [Gateway packaging guide](https://github.com/KLORQ/quarshell/blob/main/quarshell-gateway/packaging/README.md).

### Key Features

| Category | Capabilities |
|----------|--------------|
| **Docker** | Engine API create/exec/logs (Podman-compatible socket) |
| **Kubernetes** | Agent Sandbox lifecycle, workspace namespace/PVC, gated workload and proxy Pods, immutable credential generations |
| **Sidecar** | Proxy container on dual-home network + CA bundle env |
| **Hosts** | `HostGatewayExtraHosts()` → `host.cautem.internal` / `host.docker.internal` |
| **Mounts** | Workdir + reserved system paths validation |
| **Experimental** | Kubernetes requires Agent Sandbox, digest-pinned Cautem images, persistent storage, and a CNI that enforces NetworkPolicy |
| **Experimental** | MicroVM has per-VM policy networking, authenticated host/guest supervisor isolation, Gateway paired credentials, SSH relay, one-shot and interactive PTY exec, bounded Gateway logs and authenticated file copy; macOS ARM64/HVF Gateway lifecycle is live-tested, while Linux KVM, durable log/watch and remaining OpenShell VM parity are pending |

---

## Installation

Run `go test ./...` from this checkout. The module is also available as a standalone Go dependency.

**Requirements:** Go 1.27+, Docker-compatible Engine API 1.40 or newer.

The driver uses the separate Moby `client` and `api` SDK modules pinned in
`go.mod`. API negotiation is enabled by default; Docker configuration uses
`DOCKER_*`, while Podman selects its compatible socket. SDK dependency checks
cover the client code; keep the Engine daemon updated independently.

---

## Quick Start

```go
import (
    "github.com/KLORQ/quarshell-driver/driver"
    _ "github.com/KLORQ/quarshell-driver/driver/all" // register backends
)

backend, registered, err := driver.OpenRegisteredBackendWithConfig("docker", nil)
_ = backend
_ = registered
_ = err
hosts := driver.HostGatewayExtraHosts()
// []string{"host.cautem.internal:host-gateway", "host.docker.internal:host-gateway"}
_ = hosts
```

Built-in registrations include immutable `BackendInfo` metadata. A control plane
can read it with `driver.RegisteredBackendInfo("docker")` before opening the
backend; this reports capabilities without initializing the Docker runtime.
Extensions can use `RegisterBackendWithConfigAndInfo` to provide the same
static snapshot. `Capabilities` on a constructed Backend remains available for
runtime operations but must not initialize a runtime or perform network I/O.
Backends can also register a `BackendConfigValidator` for deployment preflight;
it must validate supplied config without opening the backend, probing host
tools, connecting to services, or mutating host state.

---

## Package Structure

| Path | Purpose |
|------|---------|
| `driver/` | Modular backend API and capability contracts |
| `driver/all/` | Blank-import to register the Docker, Podman, and Kubernetes backends |
| `internal/docker/` | Docker Engine implementation |
| `podman/` | Podman socket discovery → Docker API client |
| `quarshell-driver-vm` module (`vm/`, `vmguest/`, `cmd/`) | OpenShell-style libkrun MicroVM driver (worker supervision, persisted lifecycle, guest vsock control, one-shot Exec, logs, and file copy; import `github.com/KLORQ/quarshell-driver-vm/vm` in the composition root to register it) |
| `internal/kubernetes/` | Native Gateway `Backend` registration for Agent Sandbox lifecycle, workspace isolation, immutable runtime Secrets, and paired Cautem Pods |
| `internal/mounts/` | Bind-mount policy |
| `internal/sidecar/` | CA / env helpers for the proxy sidecar |

## Kubernetes status

The Kubernetes driver creates and recovers Agent Sandbox resources, uses a PVC
for the default workspace, and rotates bearer and mTLS credentials on restart.
Gateway create requests enter through the domain `BackendCreateRequest` for all
built-in drivers. Kubernetes, Docker, Podman, and VM register native Backend
factories; remote compute plugins use the same native Backend contract. The
driver package exposes only the modular Backend API. VM Gateway provisioning
now launches and recovers on the local macOS ARM64/HVF host through the Gateway
composition, including paired credentials, supervisor relay, logs, and
stop/restart. Linux KVM, managed release installation, durable watch/log
semantics, and the remaining OpenShell VM parity rows are still incomplete.
Policy changes replace immutable Secret generations; an active sandbox is
restarted so the workload and proxy load the same policy. The Gateway waits for
the supervisor to report the new revision.

Configure `CAUTEM_KUBERNETES_PROXY_IMAGE` and
`CAUTEM_KUBERNETES_RUNTIME_IMAGE` with immutable `@sha256:` references, and set
`CAUTEM_KUBERNETES_GRPC_ENDPOINT` to an endpoint reachable from Pods. HTTPS
endpoints also require the `CAUTEM_KUBERNETES_GUEST_TLS_CA`,
`CAUTEM_KUBERNETES_GUEST_TLS_CERT`, and `CAUTEM_KUBERNETES_GUEST_TLS_KEY`
files. The cluster needs the Agent Sandbox controller, a working PVC storage
class, and a NetworkPolicy-enforcing CNI. Run the live traffic probe against
the target cluster before enabling this experimental backend; fake API tests
and object admission do not verify workload startup or traffic isolation.

## MicroVM status

The VM backend is experimental and is not yet a production replacement for
Docker or OpenShell. It implements Create/Start/Stop/Delete/List/Inspect,
one-shot Exec, console logs, and file copy. The driver launches an isolated
`cautem-vm-worker`; the guest runs a static Go control agent on a host-initiated
virtio-vsock port. The authenticated host↔guest control channel uses
length-prefixed JSON protocol v1, and each side rejects unsupported versions.
Windows builds include compile-only stubs for Unix guest-preparation helpers;
the MicroVM backend is unavailable on Windows because libkrun and guest image
preparation are supported only on Linux and macOS.
Worker settings and command environments travel over a
private pipe/control socket and are not written to sandbox state. On Linux the
worker starts in private user and mount namespaces and makes mount propagation
private; it fails closed when namespace support is unavailable or the Gateway
runs as host root. The namespace UID maps to the Gateway account, so this is not
a separate host UID. On macOS the worker is a separate process but remains under
the Gateway account. The Gateway now requests a persistent guest overlay as the
VM workspace and does not pass or create a host workspace directory, matching
the OpenShell VM storage model.
When a guest supervisor is configured, its lifecycle bridge now uses mutual TLS
across the vsock relay. The driver issues per-boot credentials bound to the
sandbox ID, rootfs digest, runtime artifact digest and generation; each request
carries versioned claims and a fresh nonce that the host checks against the
authenticated URI SAN. The guest client key is installed root-only under
`/run/cautem` before the workload starts. This establishes the transport
boundary; moving provider and
policy execution fully to a host supervisor and running the hostile-guest and
cross-host acceptance suite are still required before production use.
The worker receives only the configured libkrun loader path from the host
environment. Non-TTY Exec
captures stdout/stderr (up to 8 MiB per stream) in `driver.ExecResult`, accepts
guest stdin, and applies context cancellation to the guest process group. State
and VM workers can be recovered after the Gateway process restarts. Each
Gateway-launched VM and image-prep worker also receives a private parent
liveness pipe; if the Gateway exits unexpectedly, the worker terminates its
process group, including the policy broker. This protects current-process
ownership but does not replace the separate host-supervisor and host filesystem
isolation required before Gateway VM creation is enabled.

VM create accepts an OCI image reference in the workload request, falling back to
the operator-configured `OPENSHELL_SANDBOX_IMAGE` when the sandbox leaves it
empty. The default matches OpenShell v0.1.4 (`nvcr.io/nvidia/base/ubuntu:24.04`);
Gateway configuration can override it with `default_image`. Create also accepts
`rootfs_tar_path` in VM `driver_config`. The resolved default is persisted with
sandbox state for recovery. Gateway-issued
`rootfs_tar_staging_token` resolves to a single-use upload slot returned by
the OpenShell staging RPC; clients write to its gateway-local path, matching
the upstream CLI contract. Before registry fallback, the
driver checks the local Docker-compatible Engine (Docker or Podman) and imports
an image that is already present there. Local export is pinned to the Engine
image ID, capped at 16 GiB, and recovery fails if the local tag now points to a
different ID or the image is gone. `openshell/sandbox-from:*` references
require a matching local image. Registry pulls use the default Docker/Podman
credential keychain. Registry tags are resolved to manifest digests, and the
driver selects the Linux image for the host architecture. Both source paths
check config platform, layer digest, size and uncompressed DiffID; plain, gzip
and zstd layers, normal/opaque whiteouts, and Unix setuid, setgid and sticky
permission bits are supported with root-confined paths.

The two-disk path uses `OPENSHELL_VM_BOOTSTRAP_IMAGE` as the trusted immutable VM
boot root. When it is unset, the operator-configured `OPENSHELL_SANDBOX_IMAGE`
(including its built-in trusted default) is used as bootstrap, matching
OpenShell's fallback. The requested workload OCI image or staged rootfs tar is
prepared on a separate read-only disk under `/image-rootfs`, with the writable
sandbox overlay layered over that workload root. Gateway supervisor/isolation
and target-host acceptance remain release gates.

The rootfs cache lives under `<state_dir>/images`; base ext4 images are
immutable and keyed by source manifest, selected platform manifest, platform,
rootfs layout version and guest-agent digest. The default quota is 64 GiB and
can be changed with `CAUTEM_VM_IMAGE_CACHE_MAX_BYTES`. GC keeps images referenced
by durable sandbox state or protected by an active create lease. Uploaded rootfs
archives are limited to 10 GiB compressed; decompressed layer/rootfs data is
limited to 32 GiB. The image must match the host architecture and provide Linux
`mount`, `chroot`, ext4 and overlayfs support. Rootfs preparation requires
e2fsprogs. Registry OCI layers download concurrently with a default of four
workers; `OPENSHELL_VM_IMAGE_PULL_CONCURRENCY` selects 1–16 workers (invalid,
empty, or zero values use the default). Layers are still verified, extracted,
and applied in manifest order.

OCI image `Entrypoint`, `Cmd`, `Env`, `User` and `WorkingDir` are applied to the
in-memory VM workload spec. After Gateway recovery, registry image config is
reloaded by pinned platform manifest digest and local image config by immutable
Engine ID instead of following a moved tag.
Environment and identity values are not written to durable VM state; workload
input cannot override the guest's reserved identity variables, and root OCI
identities fail closed. Unit tests cover config merge, defaults, validation and
digest-pinned recovery. The rootfs must include any runtime files needed by the
guest. Gateway runtime validation remains fail-closed until the OpenShell-style
host supervisor and authenticated host/guest isolation contract are implemented.
The driver-level macOS ARM64 HVF live lifecycle acceptance passes on the
development host; Gateway/CLI end-to-end acceptance, Linux KVM, installed
release bundles and GPU/VFIO lanes remain required.

Build the native worker, static Linux guest agent, `cautem-init`,
`cautem-supervisor`, and `cautem-sshd` with `scripts/build-vm-runtime.sh`.
The build verifies umoci v0.6.0 against `scripts/vm-runtime-artifacts.lock`
and writes `vm-runtime-manifest.json` only after all seven executable artifacts
are present and hashed. Custom umoci binaries require
`CAUTEM_VM_UMOCI_SHA256`.
The Linux KVM acceptance also checks that `libkrun.so` and `libkrunfw.so.5`
are ELF64 shared libraries for the host architecture and records their SHA-256
values in `vm-runtime-libraries.json`. Those hashes identify the tested inputs;
they are not a vendor signature or a trusted digest lock.
The guest agent is cross-compiled with the host Go toolchain, so Docker is not
needed to build these artifacts. Then set
`CAUTEM_VM_WORKER` and `CAUTEM_VM_GUEST_AGENT` to the resulting files. Set
`OPENSHELL_VM_RUNTIME_DIR` to a compatible libkrun runtime bundle and
`OPENSHELL_VM_DRIVER_STATE_DIR` to override the OpenShell state directory.
If the runtime override is unset, the driver looks under
`$XDG_DATA_HOME/openshell/vm-runtime` or `$HOME/.local/share/openshell/vm-runtime`.
The bundle must be installed by the operator; this Go build does not yet embed
and extract libkrun as the pinned upstream build does.
Rootfs tar uploads stage under `<state_dir>/rootfs-tar-staging` by default;
set `OPENSHELL_VM_ROOTFS_TAR_STAGING_DIR` to choose another private Gateway
staging directory. The state default is `$XDG_STATE_HOME/openshell/vm-driver`
or `$HOME/.local/state/openshell/vm-driver`.
The driver and guest agent must be built from the same release: every guest
vsock control request is authenticated with a per-sandbox token stored in the
private VM state directory and excluded from workload environment variables.
Linux requires read/write access to `/dev/kvm`; preflight opens the configured
KVM character device and checks its KVM API version, so a regular file or
unrelated device cannot satisfy the check. macOS support targets Apple
Silicon with Hypervisor.framework. The macOS build signs the worker with the
Hypervisor and library-loading entitlements; set `CAUTEM_VM_CODESIGN_IDENTITY`
to the signing identity for a distributable build (local builds use ad-hoc
signing). Gateway-launched macOS workers must be trusted either by configured
`CAUTEM_VM_WORKER_TEAM_ID` or by `CAUTEM_VM_WORKER_SHA256`, which pins the
ad-hoc-signed worker's exact binary digest. The build manifest records that
digest. The hash-pinned path verifies the ad-hoc code signature and rejects a
changed worker; use Team ID signing for distributed installs. The worker sets
its dynamic loader search path from the configured runtime bundle and strips
inherited loader overrides.

The opt-in live acceptance test exercises guest boot, broker health and an
actual default-deny HTTP 403 from the Cautem policy engine over guest vsock,
supervisor control handshake, SSH command execution over vsock, file copy,
worker recovery, stop/start and delete against a real host hypervisor.
Provide an ext4 rootfs tar with a `sandbox` user (UID/GID 1000 by default), the
matching static guest agent and runtime helpers, worker and libkrun bundle, then
run:

```sh
CAUTEM_VM_ACCEPTANCE=1 \
CAUTEM_VM_TEST_ROOTFS_TAR=/path/to/rootfs.tar \
CAUTEM_VM_TEST_GUEST_AGENT=/path/to/cautem-vm-guest-agent-linux-arm64 \
CAUTEM_VM_TEST_INIT_BIN=/path/to/cautem-init-linux-arm64 \
CAUTEM_VM_TEST_SUPERVISOR_BIN=/path/to/cautem-supervisor-linux-arm64 \
CAUTEM_VM_TEST_SSH_BIN=/path/to/cautem-sshd-linux-arm64 \
CAUTEM_VM_TEST_E2FS_FORMATTER=/path/to/mke2fs \
CAUTEM_VM_WORKER=/path/to/cautem-vm-worker \
OPENSHELL_VM_RUNTIME_DIR=/path/to/libkrun-runtime \
go test -C ../quarshell-driver-vm ./vm -run '^TestLiveMicroVMLifecycle$' -count=1 -v
```

For a reproducible Linux KVM run, configure a self-hosted GitHub Actions runner
with the `cautem-kvm` label, Docker, e2fsprogs, read/write access to `/dev/kvm`,
and a compatible Linux libkrun bundle. Set the repository variable
`OPENSHELL_VM_RUNTIME_DIR` to the bundle path and run the manual **Live VM KVM
acceptance** workflow, or invoke `scripts/run-live-kvm-acceptance.sh` directly.
The script builds the worker, guest agent, and runtime helpers; it prepares an
Alpine rootfs with the `sandbox` user through Docker when one is not supplied,
then runs the full lifecycle acceptance test.

The separate Docker-in-MicroVM profile is available as `task vm:live-linux-docker`.
It boots the pinned Docker 28 guest rootfs in a disposable 4 GiB/2 vCPU VM and
attaches a separate sparse 20 GiB ext4 data disk for Docker layers. It checks
engine startup, container execution, and guest cgroup limits. This is a trusted
test fixture; it does not enable privileged Docker access in ordinary sandbox
workloads.

The rootful Podman 6.1.4 profile is available as `task vm:live-linux-podman`.
It uses the pinned Fedora fixture, the same isolated VM resource budget, and a
separate ext4-backed graphroot. It checks the Podman API, offline container
execution, and guest cgroup limits. Both engine-in-MicroVM profiles require a
native Linux KVM runner.

When `proxy_bin` and `policy_path` are supplied, each VM worker starts its own
Cautem policy proxy on a mode-0600 Unix socket. libkrun maps guest-initiated
vsock port 5501 to that socket. The guest agent exposes only a loopback HTTP
proxy and forces workload HTTP(S) proxy variables to it. The VM has no
virtio-net device, and `krun_add_vsock` receives TSI feature mask `0`, which
disables libkrun's transparent IP and Unix socket interception. This leaves the
policy broker as the only guest egress route. The worker waits for the proxy
health endpoint before creating the VM and stops the VM if the broker exits.
Policy file reload and Gateway policy acknowledgements use the existing proxy
implementation.

When `supervisor_bin` is configured, the driver installs the supervisor, hardening
init, and policy into the private writable overlay. The guest agent launches the
workload through that supervisor, exposes its bounded lifecycle control socket
over guest-initiated vsock port 5503, and wraps one-shot Exec with the same
hardening init. Gateway lifecycle readiness still waits for its authenticated
supervisor session.

SSH uses the same host proxy relay pattern as the container backends. The host
proxy connects to a private UDS mapped through vsock port 5502; inside the VM,
the guest agent bridges it to `cautem-sshd` on a private Unix socket. The daemon
does not listen on guest TCP, and SSH only becomes available when the policy
proxy and supervisor relay are configured.

The VM driver provides authenticated full-duplex PTY sessions with stdin,
resize and terminal signal handling; macOS ARM64/HVF live acceptance covers
resize, Ctrl-C, exit status and OCI env/workdir inheritance. Gateway VM creation
remains fail-closed while the host supervisor/isolation contract, GPU passthrough
and Linux KVM acceptance are open.
libkrun warns that virtiofs can escape the selected host directory without host
mount-namespace isolation, so the Gateway rejects VM before consuming rootfs
staging or writing sandbox state. The Linux KVM live acceptance workflow still
needs a self-hosted runner with `/dev/kvm`; macOS and Linux live runs are release
gates. The VM backend validates every requested runtime option it receives and
rejects guarantees it does not implement instead of silently ignoring them.


---

## Related

| Resource | Link |
|----------|------|
| Roadmap | [ROADMAP.md](./ROADMAP.md) |
| Organization | [https://github.com/KLORQ](https://github.com/KLORQ) |
| Organization overview | [github.com/KLORQ](https://github.com/KLORQ) |
| pkg.go.dev | [`github.com/KLORQ/quarshell-driver`](https://pkg.go.dev/github.com/KLORQ/quarshell-driver) |

## License

[Apache-2.0](./LICENSE) © cautem
