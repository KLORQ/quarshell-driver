package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	core "github.com/KLORQ/quarshell-core"
	"github.com/KLORQ/quarshell-driver/driver"
)

func openBackendWithConfig(values map[string]any) (driver.Backend, error) {
	d, err := NewWithConfig(values)
	if err != nil {
		return nil, err
	}
	return d, nil
}

func (*Driver) ID() driver.BackendID { return "kubernetes" }
func (d *Driver) Capabilities(ctx context.Context) (driver.BackendInfo, error) {
	if d == nil {
		return driver.BackendInfo{}, fmt.Errorf("kubernetes backend is not initialized")
	}
	if ctx == nil {
		return driver.BackendInfo{}, fmt.Errorf("kubernetes backend capabilities context is nil")
	}
	if err := ctx.Err(); err != nil {
		return driver.BackendInfo{}, err
	}
	return d.backendInfo(), nil
}
func (d *Driver) ValidateCreate(ctx context.Context, request driver.BackendCreateRequest) error {
	if d == nil {
		return fmt.Errorf("kubernetes backend is not initialized")
	}
	if ctx == nil {
		return fmt.Errorf("kubernetes backend validation context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	input, err := kubernetesCreateInputFromBackend(request)
	if err != nil {
		return err
	}
	if d.gatewayID == "" || d.gatewayID != d.runtimeConfig.GatewayID || input.GatewayID != d.gatewayID {
		return fmt.Errorf("kubernetes backend Gateway identity is missing or inconsistent")
	}
	if err := d.runtimeConfig.Validate(); err != nil {
		return err
	}
	if _, err := ParseSandboxDriverConfig(input.DriverConfigJSON); err != nil {
		return err
	}
	if _, err := driver.NormalizeMainProcessRestartPolicy(input.RestartPolicy); err != nil {
		return fmt.Errorf("kubernetes backend: %w", err)
	}
	image := input.Image
	if image == "" {
		image = d.runtimeConfig.DefaultImage
	}
	if image == "" {
		return fmt.Errorf("kubernetes sandbox image is required")
	}
	if _, err := readKubernetesSandboxPolicy(input.PolicyPath); err != nil {
		return err
	}
	gatewayURL, _, err := resolveKubernetesGatewayEndpoint(input.GatewayURL, d.runtimeConfig.GRPCEndpoint)
	if err != nil {
		return err
	}
	guestTLS, err := readKubernetesGuestTLS(d.runtimeConfig)
	if err != nil {
		return err
	}
	parsedGatewayURL, _ := url.Parse(gatewayURL)
	if (parsedGatewayURL.Scheme == "https") != (guestTLS != nil) {
		return fmt.Errorf("kubernetes guest TLS files must be configured exactly when the Gateway endpoint uses HTTPS")
	}
	if err := requireProxyEnvironmentValue(input.ProxyEnv, "QUARSHELL_GATEWAY_URL", gatewayURL); err != nil {
		return err
	}
	if err := requireProxyEnvironmentValue(input.ProxyEnv, "QUARSHELL_SANDBOX", input.Name); err != nil {
		return err
	}
	return nil
}
func (d *Driver) Create(ctx context.Context, request driver.BackendCreateRequest) (driver.Handle, error) {
	if err := d.ValidateCreate(ctx, request); err != nil {
		return driver.Handle{}, err
	}
	if ctx == nil {
		return driver.Handle{}, fmt.Errorf("kubernetes backend create context is nil")
	}
	if err := ctx.Err(); err != nil {
		return driver.Handle{}, err
	}
	input, err := kubernetesCreateInputFromBackend(request)
	if err != nil {
		return driver.Handle{}, err
	}
	input.Environment = driver.SupervisorTelemetryEnvironment(ctx, request.Runtime.TelemetryEndpoint, input.Environment)
	return d.createInput(ctx, input)
}
func (d *Driver) RefreshRuntimeCredentials(ctx context.Context, id core.ID) error {
	return d.RefreshSandboxRuntimeCredentials(ctx, id)
}

func (d *Driver) WaitProxyReady(ctx context.Context, sandboxName string) error {
	info, err := d.inspect(ctx, sandboxName)
	if err != nil {
		return err
	}
	return d.start(ctx, info.ID)
}

var _ driver.Backend = (*Driver)(nil)
var _ driver.RuntimeTokenStarter = (*Driver)(nil)
var _ driver.RuntimePolicyUpdater = (*Driver)(nil)
var _ driver.RuntimeCredentialRefresher = (*Driver)(nil)
var _ driver.RuntimeProxyReadiness = (*Driver)(nil)

func kubernetesCreateInputFromBackend(request driver.BackendCreateRequest) (kubernetesCreateInput, error) {
	if request.Name == "" {
		return kubernetesCreateInput{}, fmt.Errorf("kubernetes sandbox name is required")
	}
	if request.SandboxID != "" && string(request.SandboxID) != request.Name {
		return kubernetesCreateInput{}, fmt.Errorf("kubernetes backend requires sandbox id to match its name")
	}
	if len(request.Mounts) != 0 {
		return kubernetesCreateInput{}, fmt.Errorf("kubernetes backend does not support Gateway host mount requests")
	}
	if request.Resources.PIDs != 0 {
		return kubernetesCreateInput{}, fmt.Errorf("kubernetes backend does not support the PIDs limit")
	}
	if len(request.Resources.CDIDevices) != 0 {
		return kubernetesCreateInput{}, fmt.Errorf("kubernetes backend does not support host CDI device IDs")
	}
	if len(request.Network.ExtraHosts) != 0 || len(request.Network.PortPublish) != 0 {
		return kubernetesCreateInput{}, fmt.Errorf("kubernetes backend does not support container extra hosts or published ports")
	}
	if request.Workspace.ID == "" {
		return kubernetesCreateInput{}, fmt.Errorf("kubernetes backend requires a workspace ID")
	}
	if request.Workspace.Kind != driver.WorkspaceGatewayManaged {
		return kubernetesCreateInput{}, fmt.Errorf("kubernetes backend requires a Gateway-managed workspace namespace")
	}
	var driverConfigJSON string
	if request.Config != nil {
		encoded, err := json.Marshal(request.Config)
		if err != nil {
			return kubernetesCreateInput{}, fmt.Errorf("encode Kubernetes backend config: %w", err)
		}
		driverConfigJSON = string(encoded)
	}
	input := kubernetesCreateInput{
		Name: request.Name, Image: request.Workload.Image, Workspace: request.Workspace.ID,
		GatewayID: request.Network.GatewayID, GatewayURL: request.Network.GatewayURL,
		Command: append([]string(nil), request.Workload.Command...), Environment: append([]string(nil), request.Workload.Environment...),
		RestartPolicy: request.Workload.RestartPolicy, DriverConfigJSON: driverConfigJSON,
		GPU: request.Resources.GPU, GPUCount: request.Resources.GPUCount,
	}
	if request.Network.Proxy != nil {
		input.PolicyPath = request.Network.Proxy.PolicyPath
		input.ProxyEnv = append([]string(nil), request.Network.Proxy.Environment...)
	}
	return input, nil
}
