// Package driver defines modular compute backend contracts and capability extensions.
package driver

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/KLORQ/quarshell-core"
)

// SupervisorControlSocketName is mounted inside the shared SSH directory so
// sandbox PID 1 can request lifecycle RPCs through the authenticated sidecar.
const SupervisorControlSocketName = "supervisor-control.sock"

// WorkspaceLabel carries the Gateway-verified workspace identifier to compute
// drivers. It is reserved for internal routing and tenancy boundaries; callers
// must overwrite any user-provided value before opening a driver.
const WorkspaceLabel = "quarshell.internal/workspace"

// GatewayIDLabel carries the trusted Gateway installation identity through
// compute protocols that only preserve sandbox labels.
const GatewayIDLabel = "quarshell.internal/gateway-id"

const MainProcessRestartPolicyEnv = "QUARSHELL_MAIN_PROCESS_RESTART_POLICY"

func NormalizeMainProcessRestartPolicy(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "never":
		return "never", nil
	case "on-failure":
		return "on-failure", nil
	case "always":
		return "always", nil
	default:
		return "", errors.New("main process restart policy must be never, on-failure, or always")
	}
}

// PortPublish maps a host loopback port to a guest container port.
type PortPublish struct {
	Host  int
	Guest int
}

// Handle is a live sandbox reference.
type Handle struct {
	ID      core.ID
	Name    string
	Network string
	Image   string
}

// Info is a list/status row.
type Info struct {
	ID          core.ID
	Name        string
	Network     string
	Image       string
	Status      string
	WorkloadUID uint32
	WorkloadGID uint32
}

// MainProcessWaiter provides the terminal result for a canonical sandbox
// process. Drivers that cannot observe its exit code may omit this capability.
type MainProcessWaiter interface {
	WaitMainProcess(context.Context, core.ID) (int, error)
}

// MainProcessSignaler delivers a placement-neutral signal to the canonical
// workload process group when the backend supports process handles.
type MainProcessSignaler interface {
	SignalMainProcess(context.Context, core.ID, ExecProcessSignal) error
}

// MainProcessLogAttacher opens the canonical process log stream before waiting
// begins so short-lived workloads cannot exit before the CLI attaches.
type MainProcessLogAttacher interface {
	OpenMainProcessLogs(context.Context, core.ID) (io.ReadCloser, error)
}

// MainProcessTTY provides a bidirectional terminal connection to a TTY-enabled
// canonical process and resizes the container PTY when the host terminal changes.
type MainProcessTTY interface {
	OpenMainProcessTTY(context.Context, core.ID, uint32, uint32) (io.ReadWriteCloser, error)
	ResizeMainProcessTTY(context.Context, core.ID, uint32, uint32) error
}

// ExecRequest is a one-shot or PTY-backed command. Stdin is attached for PTY
// execution by default and for non-PTY execution when explicitly provided.
type ExecRequest struct {
	Argv []string
	TTY  bool
	// Columns and Rows are the initial PTY size when TTY is true; zero uses backend defaults.
	Columns      uint32
	Rows         uint32
	Env          []string
	WorkDir      string // empty → driver default (/workspace)
	NoLoginShell bool
	// CaptureOutput returns bounded stdout/stderr in ExecResult instead of writing to the host process.
	CaptureOutput bool
	// Stdin keeps the original one-shot byte input API. StdinReader enables
	// bounded streaming input for interactive callers without breaking users
	// built against the previous driver contract.
	Stdin       []byte
	StdinReader io.Reader
}

// ExecResult carries exit status.
type ExecResult struct {
	ExitCode        int
	Stdout          []byte
	Stderr          []byte
	OutputTruncated bool
}

// LogOptions selects the source and bounded tail for container logs.
type LogOptions struct {
	Follow  bool
	Tail    uint32
	TailSet bool
	Since   string
	Source  string // all, sandbox, or proxy
}

// ErrSSHDisabled reports a sandbox created without the SSH relay.
var ErrSSHDisabled = errors.New("sandbox has no SSH relay (created without a proxy sidecar)")
