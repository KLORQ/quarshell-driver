package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"time"

	core "github.com/KLORQ/quarshell-core"
)

// BackendID is the product-owned identity of a compute implementation.
type BackendID string

// BackendVersion is the default version reported by in-process Backends in
// development builds. Release packaging may replace it with the release value.
const BackendVersion = "0.0.0-dev"

// ConfigSchemaVersionV1 is the current backend-owned configuration schema.
const ConfigSchemaVersionV1 = "v1"

// Capability names a behavior that callers may request from a backend.
type Capability string

const (
	CapabilityPTY                      Capability = "pty"
	CapabilityInteractiveExec          Capability = "interactive-exec"
	CapabilityLogAccess                Capability = "log-access"
	CapabilityEventStream              Capability = "event-stream"
	CapabilityPlatformEvents           Capability = "platform-events"
	CapabilitySandboxWatch             Capability = "sandbox-watch"
	CapabilityFileTransfer             Capability = "file-transfer"
	CapabilityPersistentData           Capability = "persistent-data"
	CapabilityGPU                      Capability = "gpu"
	CapabilityCDI                      Capability = "cdi"
	CapabilityNativeMounts             Capability = "native-mounts"
	CapabilityServiceForward           Capability = "service-forward"
	CapabilityRootfsImport             Capability = "rootfs-import"
	CapabilityCredentialRotate         Capability = "credential-rotation"
	CapabilityTokenAwareStart          Capability = "token-aware-start"
	CapabilityPolicyUpdate             Capability = "policy-update"
	CapabilityLaunchSpecRestore        Capability = "launch-spec-restore"
	CapabilityCredentialRefresh        Capability = "runtime-credential-refresh"
	CapabilityGatewayRuntimeValidation Capability = "gateway-runtime-validation"
	CapabilityProxyReadiness           Capability = "proxy-readiness"
	CapabilityProcessWait              Capability = "process-wait"
	CapabilityProcessLogs              Capability = "process-logs"
	CapabilityProcessTerminal          Capability = "process-terminal"
	CapabilityExecTerminal             Capability = "exec-terminal"
	CapabilityRootfsTarStaging         Capability = "rootfs-tar-staging"
)

// BackendInfo is safe to expose in control-plane capability negotiation. It
// deliberately contains no upstream protocol type or backend implementation.
type BackendInfo struct {
	ID                  BackendID
	Version             string
	ConfigSchemaVersion string
	Capabilities        map[Capability]struct{}
	MaxUploadBytes      int64
	Resources           *ResourceCapabilities
}

// ResourceCapabilities describes portable resource limits understood by a
// Backend. Omitted domains are not advertised to the OpenShell control plane.
type ResourceCapabilities struct {
	CPU    *CPUResourceCapabilities
	Memory *MemoryResourceCapabilities
	GPU    *GPUResourceCapabilities
}

type CPUResourceCapabilities struct {
	LimitSupported bool
}

type MemoryResourceCapabilities struct {
	LimitSupported bool
}

type GPUResourceCapabilities struct {
	DefaultSelectionSupported bool
	CountSelectionSupported   bool
}

func (info BackendInfo) Has(capability Capability) bool {
	_, ok := info.Capabilities[capability]
	return ok
}

func (info BackendInfo) Clone() BackendInfo {
	info.Capabilities = maps.Clone(info.Capabilities)
	if info.Resources != nil {
		resources := *info.Resources
		if info.Resources.CPU != nil {
			cpu := *info.Resources.CPU
			resources.CPU = &cpu
		}
		if info.Resources.Memory != nil {
			memory := *info.Resources.Memory
			resources.Memory = &memory
		}
		if info.Resources.GPU != nil {
			gpu := *info.Resources.GPU
			resources.GPU = &gpu
		}
		info.Resources = &resources
	}
	return info
}

// BackendCreateRequest is the domain request passed from orchestration to a
// backend. Values in Runtime and Network are ephemeral and must not be stored
// by the backend. Config is a backend-owned typed value, not a wire-format map.
type BackendCreateRequest struct {
	SandboxID        core.ID
	Name             string
	Workspace        WorkspaceTarget
	Workload         WorkloadSpec
	Resources        ResourceLimits
	Mounts           []Mount
	Network          NetworkSpec
	Runtime          RuntimeAssets
	Labels           map[string]string
	AllowUnsafeMount bool
	Config           any
}

type WorkspaceTarget struct {
	Kind     WorkspaceKind
	ID       string
	HostPath string
	Persist  bool
}

// WorkspaceKind selects the storage boundary owned by the backend. The empty
// value is interpreted as WorkspaceHostPath.
type WorkspaceKind string

const (
	WorkspaceHostPath       WorkspaceKind = "host-path"
	WorkspaceGuestOverlay   WorkspaceKind = "guest-overlay"
	WorkspaceGatewayManaged WorkspaceKind = "gateway-managed"
)

type WorkloadSpec struct {
	Image                      string
	Command                    []string
	Environment                []string
	TTY                        bool
	AwaitMainProcessAttachment bool
	RestartPolicy              string
}

type ResourceLimits struct {
	CPUCores    float64
	MemoryBytes int64
	PIDs        int64
	GPU         bool
	GPUCount    int
	CDIDevices  []string
}

// Mount describes a host path admitted by orchestration. Backend validation
// still checks that its engine can honor the requested isolation and mapping.
type Mount struct {
	Source      string
	Destination string
	ReadOnly    bool
	UID         *uint32
	GID         *uint32
}

type NetworkSpec struct {
	GatewayID   string
	GatewayURL  string
	Proxy       *ProxySpec
	ExtraHosts  []string
	PortPublish []PortPublish
}

// ProxySpec includes secret values only for the short-lived Create call.
type ProxySpec struct {
	Binary      string
	Image       string
	PolicyPath  string
	Port        int
	Environment []string
}

type RuntimeAssets struct {
	InitBinary        string
	SupervisorBinary  string
	TelemetryEndpoint string
	SSHBinary         string
	NoHarden          bool
	EnableSSH         bool
	DisplayMode       string
	DisplayPort       int
	DisplayPassword   string
}

// Backend is the required lifecycle contract. Optional operations live in
// extension interfaces so each backend implements only the features it
// advertises. Backend-specific configuration stays typed inside its package.
type Backend interface {
	ID() BackendID
	// Capabilities returns static, safe-to-expose metadata. Implementations must
	// not initialize the runtime, perform network I/O, or depend on live backend
	// availability while answering this method.
	Capabilities(context.Context) (BackendInfo, error)
	ValidateCreate(context.Context, BackendCreateRequest) error
	Create(context.Context, BackendCreateRequest) (Handle, error)
	Start(context.Context, core.ID) error
	Stop(context.Context, core.ID) error
	Delete(context.Context, core.ID) error
	List(context.Context) ([]Info, error)
	Inspect(context.Context, string) (Info, error)
}

// MainProcessExitRecorder lets a backend persist the canonical workload exit
// reported by its authenticated supervisor. Control planes call this only
// after validating the supervisor identity and durably recording the result.
type MainProcessExitRecorder interface {
	RecordMainProcessExit(context.Context, core.ID, string, int32) error
}

// InteractiveExec is optional. Backends advertise
// CapabilityInteractiveExec and, for terminal sessions, CapabilityPTY.
type InteractiveExec interface {
	Exec(context.Context, core.ID, ExecRequest) (ExecResult, error)
}

// ExecTerminalRequest starts a command attached to a guest PTY. Columns and
// Rows describe the initial size; subsequent changes use ExecTerminalSession.Resize.
type ExecTerminalRequest struct {
	Argv         []string
	Env          []string
	WorkDir      string
	SessionID    string
	NoLoginShell bool
	Columns      uint32
	Rows         uint32
}

// ExecTerminalSession is a full-duplex PTY stream. EOF from Read signals the
// process has exited; Wait returns its normalized exit status.
type ExecTerminalSession interface {
	io.ReadWriteCloser
	Resize(context.Context, uint32, uint32) error
	Wait(context.Context) (int, error)
}

// ExecTerminal is an optional streaming PTY contract for interactive command
// sessions. Backends advertise CapabilityExecTerminal only when implemented.
type ExecTerminal interface {
	OpenExecTerminal(context.Context, core.ID, ExecTerminalRequest) (ExecTerminalSession, error)
}

// ExecTerminalAttach optionally reconnects to a still running terminal process
// by its stable session ID. The returned session streams the same process.
type ExecTerminalAttach interface {
	AttachExecTerminal(context.Context, core.ID, string) (ExecTerminalSession, error)
}

// ExecTerminalSessionID exposes the stable identifier needed to reattach after
// a transport disconnect.
type ExecTerminalSessionID interface {
	SessionID() string
}

// ExecTerminalTerminator terminates a retained PTY process by its stable ID,
// including while no stream is attached.
type ExecTerminalTerminator interface {
	TerminateExecTerminal(context.Context, core.ID, string) error
}

// ExecStreamOutput is one event from a non-TTY interactive process. Exited is
// terminal and carries the normalized process status.
type ExecStreamOutput struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Exited   bool
}

// ExecStreamSession is a full-duplex non-TTY process stream. Output channels
// remain distinct, and CloseStdin half-closes input without dropping output.
type ExecStreamSession interface {
	Write([]byte) (int, error)
	ReadOutput() (ExecStreamOutput, error)
	CloseStdin() error
	Close() error
}

// InteractiveExecStream is an optional streamed non-TTY exec contract.
type InteractiveExecStream interface {
	OpenExecStream(context.Context, core.ID, ExecTerminalRequest) (ExecStreamSession, error)
}

// ExecTerminalInputCloser signals end-of-input without closing the terminal
// output stream.
type ExecTerminalInputCloser interface {
	CloseStdin() error
}

// ExecTerminalSignaler optionally delivers a process signal to the process
// group owned by an interactive terminal session. The session remains the
// process owner; callers never address a host or guest PID directly.
type ExecTerminalSignaler interface {
	Signal(context.Context, ExecProcessSignal) error
}

type ExecProcessSignal string

const (
	ExecSignalTerminate ExecProcessSignal = "terminate"
	ExecSignalKill      ExecProcessSignal = "kill"
	ExecSignalInterrupt ExecProcessSignal = "interrupt"
	ExecSignalHangup    ExecProcessSignal = "hangup"
)

// LogAccess is optional and streams workload logs to the caller.
type LogAccess interface {
	Logs(context.Context, core.ID, bool, io.Writer) error
}

// EventStream is optional until a backend provides durable generation and
// sequence semantics. Callers must negotiate CapabilityEventStream first.
type EventStream interface {
	Watch(context.Context, WatchCursor) (<-chan Event, error)
}

// PlatformEventWatchRequest identifies the sandbox whose backend-native
// platform events are requested. SandboxName supports backends whose native
// platform resource name differs from the product's stable sandbox ID.
type PlatformEventWatchRequest struct {
	SandboxID   core.ID
	SandboxName string
}

// PlatformEvent is a backend-native observation without durable cursor
// guarantees. Implementations must use EventStream instead when they can
// provide resumable generation and sequence semantics.
type PlatformEvent struct {
	// SandboxID must identify the sandbox requested by PlatformEventWatchRequest.
	SandboxID core.ID
	Time      time.Time
	Source    string
	Type      string
	Reason    string
	Message   string
	Metadata  map[string]string
	Terminal  bool
}

// PlatformEventResult carries stream errors that occur after the watch starts.
type PlatformEventResult struct {
	Event PlatformEvent
	Err   error
}

// PlatformEventStream is optional. Unlike EventStream, it does not promise a
// durable cursor and is suitable for adapting a backend's native watch RPC.
type PlatformEventStream interface {
	WatchPlatformEvents(context.Context, PlatformEventWatchRequest) (<-chan PlatformEventResult, error)
}

// SandboxWatchEvent is a driver-wide snapshot update or deletion observation.
// It intentionally carries only the portable fields available in Info.
type SandboxWatchEvent struct {
	Sandbox   *Info
	DeletedID core.ID
}

// SandboxWatchResult carries a driver-wide watch update or stream error.
type SandboxWatchResult struct {
	Event SandboxWatchEvent
	Err   error
}

// SandboxWatchStream is an optional best-effort feed of current sandbox
// snapshots followed by lifecycle updates. It does not promise durable replay.
type SandboxWatchStream interface {
	WatchSandboxes(context.Context) (<-chan SandboxWatchResult, error)
}

// FileTransfer is optional. Implementations enforce archive and path limits
// before writing into a sandbox or the host filesystem.
type FileTransfer interface {
	CopyTo(context.Context, core.ID, string, string) error
	CopyFrom(context.Context, core.ID, string, string) error
}

type ServiceForwardRequest struct {
	Name       string
	TargetPort uint16
	BindHost   string
}

type ServiceForwardHandle struct {
	ID      string
	Address string
}

// ServiceForward is optional. Closing the same backend-owned handle more than
// once must be safe.
type ServiceForward interface {
	OpenServiceForward(context.Context, core.ID, ServiceForwardRequest) (ServiceForwardHandle, error)
	CloseServiceForward(context.Context, core.ID, string) error
}

type RootfsImportRequest struct {
	Archive  io.Reader
	MaxBytes uint64
	Digest   string
}

type RootfsArtifact struct {
	ImageRef  string
	Digest    string
	SizeBytes uint64
}

// RootfsImporter is optional. Implementations validate limits, digest and
// archive paths before publishing an immutable artifact.
type RootfsImporter interface {
	ImportRootfs(context.Context, RootfsImportRequest) (RootfsArtifact, error)
}

type CredentialBinding struct {
	Name  string
	Value string
}

// CredentialRotator receives short-lived values and must not retain them in
// durable backend metadata, logs or inspection output.
type CredentialRotator interface {
	RotateCredentials(context.Context, core.ID, []CredentialBinding) error
}

// RuntimeTokenStarter replaces short-lived supervisor credentials before a
// stopped sandbox starts again. The value is transient and must not be stored.
type RuntimeTokenStarter interface {
	StartWithToken(context.Context, core.ID, string) error
}

// RuntimeLaunchCredentials carry the complete short-lived supervisor auth
// material into a runtime launch. Implementations must not persist the bearer
// values or expose them through inspection and logs.
type RuntimeLaunchCredentials struct {
	// SandboxID is the stable control-plane sandbox identifier embedded in
	// OpenShell session JWTs. It can differ from the backend's runtime ID.
	SandboxID         string
	RuntimeGeneration string
	SessionID         string
	GatewayID         string
	VerificationKeys  []RuntimeVerificationKey
	GatewayToken      string
	GatewayExpiresAt  time.Time
	SandboxToken      string
	SandboxExpiresAt  time.Time
	CredentialEpoch   uint64
	SessionRotation   uint64
}

// RuntimeVerificationKey is a Gateway-owned public key used by the sandbox
// boundary to verify OpenShell session JWTs. Key material is public; callers
// must still deliver it only through the launch authentication channel.
type RuntimeVerificationKey struct {
	KeyID        string
	PublicKeyPEM []byte
}

// RuntimeCredentialStarter starts a backend with paired Gateway and Sandbox
// Protocol credentials from one authorization epoch.
type RuntimeCredentialStarter interface {
	StartWithCredentials(context.Context, core.ID, RuntimeLaunchCredentials) error
}

// RuntimePolicyUpdater delivers an effective policy to a runtime whose policy
// source is not shared with the control plane. The backend must return only
// after the policy is durably attached to the sandbox.
type RuntimePolicyUpdater interface {
	UpdateSandboxPolicy(context.Context, core.ID, []byte) error
}

// RuntimeLaunchSpecRestorer restores process-local workload settings after a
// control-plane restart. Implementations must not persist supplied secrets.
type RuntimeLaunchSpecRestorer interface {
	RestoreLaunchSpec(context.Context, core.ID, BackendCreateRequest) error
}

// RuntimeCredentialRefresher asks a backend to refresh credentials already
// mounted into a running runtime without changing its effective policy.
type RuntimeCredentialRefresher interface {
	RefreshRuntimeCredentials(context.Context, core.ID) error
}

// RuntimeContractValidator checks whether a backend can satisfy the Gateway's
// required runtime guarantees before it consumes one-shot create inputs.
type RuntimeContractValidator interface {
	ValidateRuntimeContract(context.Context) error
}

// RuntimeProxyReadiness waits until the backend-owned egress proxy is ready.
type RuntimeProxyReadiness interface {
	WaitProxyReady(context.Context, string) error
}

// RuntimeProcessWaiter reports the exit status of the canonical sandbox process.
type RuntimeProcessWaiter interface {
	WaitProcess(context.Context, core.ID) (int, error)
}

// RuntimeProcessLogs opens output for the canonical sandbox process.
type RuntimeProcessLogs interface {
	OpenProcessLogs(context.Context, core.ID) (io.ReadCloser, error)
}

// RuntimeProcessTerminal opens and resizes the canonical sandbox terminal.
type RuntimeProcessTerminal interface {
	OpenProcessTerminal(context.Context, core.ID, uint32, uint32) (io.ReadWriteCloser, error)
	ResizeProcessTerminal(context.Context, core.ID, uint32, uint32) error
}

// RuntimeRootfsTarStager allocates a backend-owned staging root for a bounded
// upload. It follows the pinned OpenShell path-based staging contract.
type RuntimeRootfsTarStager interface {
	RootfsTarStaging(context.Context) (directory string, maxBytes uint64, err error)
}

type WatchCursor struct {
	Generation uint64
	Sequence   uint64
}

type Event struct {
	Generation uint64
	Sequence   uint64
	Type       string
	SandboxID  core.ID
	Message    string
}

var ErrUnsupported = errors.New("backend capability is unsupported")

// UnsupportedError identifies the requested behavior without leaking backend
// internals into API responses.
type UnsupportedError struct {
	Backend    BackendID
	Capability Capability
	Operation  string
}

func (err *UnsupportedError) Error() string {
	return fmt.Sprintf("backend %q does not support %q for %s", err.Backend, err.Capability, err.Operation)
}

func (err *UnsupportedError) Unwrap() error { return ErrUnsupported }

// RequireExtension verifies that a backend both advertises a capability and
// implements its corresponding extension interface. This prevents a silent
// downgrade when capability metadata and implementation drift apart.
func RequireExtension[T any](ctx context.Context, backend Backend, capability Capability, operation string) (T, error) {
	var zero T
	if backend == nil {
		return zero, errors.New("backend is required")
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	info, err := backend.Capabilities(ctx)
	if err != nil {
		return zero, err
	}
	unsupported := func() (T, error) {
		return zero, &UnsupportedError{Backend: backend.ID(), Capability: capability, Operation: operation}
	}
	if !info.Has(capability) {
		return unsupported()
	}
	extension, ok := any(backend).(T)
	if !ok {
		return unsupported()
	}
	return extension, nil
}
