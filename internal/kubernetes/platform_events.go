package kubernetes

import (
	"context"
	"fmt"
	"strings"
	"time"

	core "github.com/KLORQ/quarshell-core"
	"github.com/KLORQ/quarshell-driver/driver"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/watch"
)

var _ driver.PlatformEventStream = (*Driver)(nil)

// WatchPlatformEvents adapts Agent Sandbox API changes to the backend event
// stream. Events include only product identity and normalized lifecycle state;
// arbitrary CR labels, annotations, and condition messages are never copied.
func (d *Driver) WatchPlatformEvents(ctx context.Context, request driver.PlatformEventWatchRequest) (<-chan driver.PlatformEventResult, error) {
	if d == nil {
		return nil, fmt.Errorf("kubernetes platform events: backend is not initialized")
	}
	if ctx == nil {
		return nil, fmt.Errorf("kubernetes platform events: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.SandboxID == "" {
		return nil, fmt.Errorf("kubernetes platform events: sandbox runtime ID is required")
	}
	identity, err := parseSandboxRuntimeID(string(request.SandboxID))
	if err != nil {
		return nil, fmt.Errorf("kubernetes platform events: %w", err)
	}
	info, err := d.inspectRuntimeIdentity(ctx, identity)
	if err != nil {
		return nil, err
	}
	if request.SandboxName != "" && request.SandboxName != info.Name {
		return nil, fmt.Errorf("kubernetes platform events: sandbox name does not match runtime identity")
	}
	client, err := d.kubernetesClient()
	if err != nil {
		return nil, err
	}
	apiClient := newDynamicAgentSandboxAPIClient(client)
	version, err := d.apiVersions.Resolve(ctx, apiClient, identity.Namespace)
	if err != nil {
		return nil, fmt.Errorf("kubernetes platform events: discover Agent Sandbox API: %w", err)
	}
	results := make(chan driver.PlatformEventResult)
	go d.forwardPlatformEvents(ctx, apiClient, version, identity, info.Name, results)
	return results, nil
}

func (d *Driver) forwardPlatformEvents(ctx context.Context, client agentSandboxWatchClient, version string, identity sandboxRuntimeIdentity, sandboxName string, results chan<- driver.PlatformEventResult) {
	defer close(results)
	seen := false
	emit := func(result driver.PlatformEventResult) error {
		select {
		case results <- result:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	watchErr := runAgentSandboxWatch(ctx, client, version, identity.Namespace, sandboxWatchBackoff{Initial: 250 * time.Millisecond, Maximum: 5 * time.Second}, func(update sandboxWatchUpdate) error {
		if update.Snapshot != nil {
			for _, object := range update.Snapshot {
				if !matchesPlatformEventIdentity(object, d.gatewayID, identity, sandboxName) {
					continue
				}
				seen = true
				return emit(driver.PlatformEventResult{Event: kubernetesPlatformEvent(identity, object, "SNAPSHOT", false)})
			}
			if seen {
				return emit(driver.PlatformEventResult{Event: driver.PlatformEvent{
					SandboxID: core.ID(kubernetesRuntimeID(identity)), Time: time.Now().UTC(), Source: "kubernetes",
					Type: "DELETED", Reason: "Deleted", Message: "Agent Sandbox was deleted", Terminal: true,
					Metadata: map[string]string{"namespace": identity.Namespace},
				}})
			}
			return fmt.Errorf("kubernetes platform events: sandbox disappeared before watch started")
		}
		if update.Object == nil || !matchesPlatformEventIdentity(update.Object, d.gatewayID, identity, sandboxName) {
			return nil
		}
		seen = true
		terminal := update.Type == watch.Deleted
		return emit(driver.PlatformEventResult{Event: kubernetesPlatformEvent(identity, update.Object, string(update.Type), terminal)})
	})
	if watchErr != nil && ctx.Err() == nil {
		_ = emit(driver.PlatformEventResult{Err: fmt.Errorf("kubernetes platform event stream: %w", watchErr)})
	}
}

func matchesPlatformEventIdentity(object *unstructured.Unstructured, gatewayID string, identity sandboxRuntimeIdentity, sandboxName string) bool {
	if object == nil || object.GetNamespace() != identity.Namespace || object.GetName() != identity.Name || string(object.GetUID()) != identity.UID {
		return false
	}
	return sandboxOwnedByGateway(object, gatewayID) &&
		(object.GetLabels()[labelSandboxName] == "" || object.GetLabels()[labelSandboxName] == sandboxName)
}

func kubernetesPlatformEvent(identity sandboxRuntimeIdentity, object *unstructured.Unstructured, eventType string, terminal bool) driver.PlatformEvent {
	state := safeSandboxPlatformState(sandboxStatus(object))
	reason := state
	message := "Agent Sandbox state: " + state
	if terminal {
		reason = "Deleted"
		message = "Agent Sandbox was deleted"
	}
	metadata := map[string]string{"namespace": identity.Namespace}
	if object.GetResourceVersion() != "" {
		metadata["resource_version"] = object.GetResourceVersion()
	}
	return driver.PlatformEvent{
		SandboxID: core.ID(kubernetesRuntimeID(identity)), Time: time.Now().UTC(), Source: "kubernetes",
		Type: strings.ToUpper(eventType), Reason: reason, Message: message, Terminal: terminal, Metadata: metadata,
	}
}

func safeSandboxPlatformState(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "pending":
		return "Pending"
	case "running":
		return "Running"
	case "failed":
		return "Failed"
	case "stopped":
		return "Stopped"
	case "starting":
		return "Starting"
	case "stopping":
		return "Stopping"
	case "terminating":
		return "Terminating"
	default:
		return "Unknown"
	}
}
