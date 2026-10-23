// Package compute defines resource allocation contracts. It does not define
// permissions inside an allocated sandbox; that belongs to the isolation SPI.
package compute

import (
	"context"
	"time"

	"github.com/KLORQ/quarshell-core/domain"
)

// State is the normalized observed lifecycle state of a sandbox.
type State string

const (
	StateUnknown      State = "unknown"
	StateRequested    State = "requested"
	StateProvisioning State = "provisioning"
	StateConfiguring  State = "configuring"
	StateReady        State = "ready"
	StateRunning      State = "running"
	StateStopping     State = "stopping"
	StateStopped      State = "stopped"
	StateFailed       State = "failed"
	StateDeleting     State = "deleting"
	StateDeleted      State = "deleted"
)

// Capabilities describes the backend and host capabilities visible to callers.
type Capabilities struct {
	Backend      string
	Version      string
	Capabilities domain.CapabilitySet
	Platforms    []string
}

// NetworkSpec is the normalized network request for a sandbox.
type NetworkSpec struct {
	Mode      string
	Endpoints []Endpoint
}

// Endpoint is one allowed network endpoint.
type Endpoint struct {
	Host     string
	Port     int
	Protocol string
	Access   string
}

// CreateSpec is the validated, backend-independent sandbox request.
type CreateSpec struct {
	Image     string
	Command   []string
	Workspace string
	Resources domain.Resources
	Network   NetworkSpec
	Labels    map[string]string
}

// DesiredSandbox binds a desired spec to a sandbox generation and operation.
type DesiredSandbox struct {
	Ref        domain.SandboxRef
	Generation domain.Generation
	Operation  domain.OperationID
	Spec       CreateSpec
}

// ObservedSandbox is the durable observation after Ensure or Lookup.
type ObservedSandbox struct {
	Ref        domain.SandboxRef
	Instance   domain.InstanceID
	Generation domain.Generation
	State      State
	BackendID  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Metadata   map[string]string
}

// Compute allocates container, pod, VM, or session resources. It must not be
// used to decide whether a workload may read a file or open a socket.
type Compute interface {
	Describe(context.Context) (Capabilities, error)
	Validate(context.Context, CreateSpec) error
	Ensure(context.Context, DesiredSandbox) (ObservedSandbox, error)
	Delete(context.Context, domain.SandboxRef) error
}

// Lookup is an optional capability for backends that can resolve a stable
// resource after an ambiguous timeout.
type Lookup interface {
	Lookup(context.Context, domain.SandboxRef) (ObservedSandbox, error)
}

// InteractiveExec is an optional capability for interactive process sessions.
type InteractiveExec interface {
	ExecInteractive(context.Context, ExecRequest) (ExecSession, error)
}

// ExecRequest describes one non-interactive or interactive exec.
type ExecRequest struct {
	Ref         domain.InstanceRef
	Command     []string
	Environment map[string]string
	Workdir     string
	TTY         bool
}

// ExecSession is a running exec session.
type ExecSession interface {
	PID() int
	Resize(context.Context, uint16, uint16) error
	Signal(context.Context, string) error
	Close() error
}
