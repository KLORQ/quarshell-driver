// Package kubernetes implements the Kubernetes compute backend.
package kubernetes

import (
	"context"
	"sync"

	"github.com/KLORQ/quarshell-core"
	"github.com/KLORQ/quarshell-driver/driver"
	"k8s.io/client-go/dynamic"
)

// Driver accesses Agent Sandbox resources owned by one Gateway installation.
// Kubernetes credentials are loaded lazily so importing the driver does not
// make CLI startup depend on a cluster being reachable.
type Driver struct {
	gatewayID string

	mu            sync.Mutex
	client        dynamic.Interface
	loadClient    func() (dynamic.Interface, error)
	apiVersions   sandboxAPIVersionResolver
	runtimeConfig BackendConfig
	createBackend agentSandboxCreateSequenceBackend
}

func NewWithBackendConfig(config BackendConfig) *Driver {
	return &Driver{gatewayID: config.GatewayID, loadClient: loadDynamicKubernetesClient, runtimeConfig: config}
}

func NewWithConfig(values map[string]any) (*Driver, error) {
	config, err := BackendConfigFromMap(values)
	if err != nil {
		return nil, err
	}
	return NewWithBackendConfig(config), nil
}

func newWithClient(client dynamic.Interface, gatewayID string) *Driver {
	config := BackendConfigFromEnvironment()
	config.GatewayID = gatewayID
	return &Driver{gatewayID: gatewayID, client: client, runtimeConfig: config}
}

func (d *Driver) Start(ctx context.Context, id core.ID) error { return d.start(ctx, id) }
func (d *Driver) StartWithToken(ctx context.Context, id core.ID, token string) error {
	return d.startWithToken(ctx, id, token)
}

// UpdateSandboxPolicy replaces the immutable policy Secrets as one runtime generation.
func (d *Driver) UpdateSandboxPolicy(ctx context.Context, id core.ID, policy []byte) error {
	return d.updateSandboxPolicy(ctx, id, policy)
}
func (d *Driver) RefreshSandboxRuntimeCredentials(ctx context.Context, id core.ID) error {
	return d.refreshSandboxRuntimeCredentials(ctx, id)
}
func (d *Driver) Stop(ctx context.Context, id core.ID) error      { return d.stop(ctx, id) }
func (d *Driver) Delete(ctx context.Context, id core.ID) error    { return d.delete(ctx, id) }
func (d *Driver) List(ctx context.Context) ([]driver.Info, error) { return d.list(ctx) }
func (d *Driver) Inspect(ctx context.Context, nameOrID string) (driver.Info, error) {
	return d.inspect(ctx, nameOrID)
}
