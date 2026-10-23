package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	core "github.com/KLORQ/quarshell-core"
	"github.com/KLORQ/quarshell-driver/driver"
)

var _ driver.Backend = (*Driver)(nil)
var _ driver.InteractiveExec = (*Driver)(nil)
var _ driver.ExecTerminal = (*Driver)(nil)
var _ driver.LogAccess = (*Driver)(nil)
var _ driver.FileTransfer = (*Driver)(nil)

func (d *Driver) ID() driver.BackendID {
	if d != nil && d.runtimeConfig.BackendID != "" {
		return driver.CanonicalBackendID(d.runtimeConfig.BackendID)
	}
	return "docker"
}

func (d *Driver) Capabilities(ctx context.Context) (driver.BackendInfo, error) {
	if ctx == nil {
		return driver.BackendInfo{}, fmt.Errorf("docker backend: Capabilities context is nil")
	}
	if err := ctx.Err(); err != nil {
		return driver.BackendInfo{}, err
	}
	return d.backendInfo(), nil
}

func (d *Driver) ValidateCreate(ctx context.Context, request driver.BackendCreateRequest) error {
	if ctx == nil {
		return fmt.Errorf("docker backend: ValidateCreate context is nil")
	}
	spec, err := dockerSpecFromBackendRequest(ctx, request)
	if err != nil {
		return err
	}
	_, err = d.validateCreate(ctx, spec)
	return err
}

func (d *Driver) Create(ctx context.Context, request driver.BackendCreateRequest) (driver.Handle, error) {
	if ctx == nil {
		return driver.Handle{}, fmt.Errorf("docker backend: Create context is nil")
	}
	spec, err := dockerSpecFromBackendRequest(ctx, request)
	if err != nil {
		return driver.Handle{}, err
	}
	validated, err := d.validateCreate(ctx, spec)
	if err != nil {
		return driver.Handle{}, err
	}
	return d.createValidated(ctx, spec, validated)
}

func dockerSpecFromBackendRequest(ctx context.Context, request driver.BackendCreateRequest) (sandboxCreateSpec, error) {
	if request.Name == "" {
		return sandboxCreateSpec{}, fmt.Errorf("docker backend: sandbox name is required")
	}
	if request.SandboxID != "" && request.SandboxID != core.ID(request.Name) {
		return sandboxCreateSpec{}, fmt.Errorf("docker backend cannot honor a distinct sandbox id")
	}
	if request.Workspace.Kind != "" && request.Workspace.Kind != driver.WorkspaceHostPath {
		return sandboxCreateSpec{}, fmt.Errorf("docker backend cannot honor workspace kind %q", request.Workspace.Kind)
	}
	if request.Workspace.HostPath == "" {
		return sandboxCreateSpec{}, fmt.Errorf("docker backend requires a host-path workspace")
	}
	if len(request.Mounts) != 0 {
		return sandboxCreateSpec{}, fmt.Errorf("docker backend does not support domain mount requests")
	}
	configJSON := ""
	if request.Config != nil {
		encoded, err := json.Marshal(request.Config)
		if err != nil {
			return sandboxCreateSpec{}, fmt.Errorf("docker backend: encode driver config: %w", err)
		}
		configJSON = string(encoded)
	}
	env := driver.SupervisorTelemetryEnvironment(ctx, request.Runtime.TelemetryEndpoint, request.Workload.Environment)
	spec := sandboxCreateSpec{
		Name: request.Name, Image: request.Workload.Image, Workspace: request.Workspace.HostPath,
		GatewayID: request.Network.GatewayID,
		Command:   slices.Clone(request.Workload.Command), TTY: request.Workload.TTY,
		MainProcessRestartPolicy: request.Workload.RestartPolicy, Env: env,
		IKnow:   request.AllowUnsafeMount,
		InitBin: request.Runtime.InitBinary, SupervisorBin: request.Runtime.SupervisorBinary,
		NoHarden: request.Runtime.NoHarden, EnableSSH: request.Runtime.EnableSSH,
		SSHBin:      request.Runtime.SSHBinary,
		DisplayMode: request.Runtime.DisplayMode, DisplayPort: request.Runtime.DisplayPort,
		DisplayPassword: request.Runtime.DisplayPassword,
		Labels:          request.Labels, ExtraHosts: slices.Clone(request.Network.ExtraHosts),
		GatewayURL: request.Network.GatewayURL, PersistVolume: request.Workspace.Persist,
		GPU: request.Resources.GPU, GPUCount: request.Resources.GPUCount,
		CDIDevices: slices.Clone(request.Resources.CDIDevices),
		CPU:        request.Resources.CPUCores, MemoryBytes: request.Resources.MemoryBytes,
		PidsLimit:        request.Resources.PIDs,
		PublishPorts:     slices.Clone(request.Network.PortPublish),
		DriverConfigJSON: configJSON,
	}
	if request.Network.Proxy != nil {
		spec.ProxyBin = request.Network.Proxy.Binary
		spec.ProxyImage = request.Network.Proxy.Image
		spec.PolicyPath = request.Network.Proxy.PolicyPath
		spec.ProxyPort = request.Network.Proxy.Port
		spec.ProxyEnv = slices.Clone(request.Network.Proxy.Environment)
	}
	return spec, nil
}
