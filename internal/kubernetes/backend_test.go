package kubernetes

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KLORQ/quarshell-driver/driver"
)

func TestRegisteredKubernetesBackendConsumesDomainRequest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	config := map[string]any{
		internalGatewayIDConfigKey: "gateway-a",
		"default_image":            "registry.example/agent:latest",
		"proxy_image":              "registry.example/proxy@" + digest,
		"sandbox_runtime_image":    "registry.example/runtime@" + digest,
		"grpc_endpoint":            "http://gateway.example:7443",
	}
	backend, registered, err := driver.OpenRegisteredBackendWithConfig("kubernetes", config)
	if err != nil || !registered {
		t.Fatalf("registered backend=%v err=%v", registered, err)
	}
	native, ok := backend.(*Driver)
	if !ok {
		t.Fatalf("backend type=%T, want *Driver", backend)
	}

	policyPath := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(policyPath, []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := driver.BackendCreateRequest{
		SandboxID: "agent", Name: "agent",
		Workspace: driver.WorkspaceTarget{Kind: driver.WorkspaceGatewayManaged, ID: "team-a", Persist: true},
		Workload:  driver.WorkloadSpec{Image: "registry.example/agent:latest", Command: []string{"python", "-V"}, Environment: []string{"A=B"}, RestartPolicy: "never"},
		Network: driver.NetworkSpec{
			GatewayID: "gateway-a", GatewayURL: "http://gateway.example:7443",
			Proxy: &driver.ProxySpec{PolicyPath: policyPath, Environment: []string{"QUARSHELL_GATEWAY_URL=http://gateway.example:7443", "QUARSHELL_SANDBOX=agent"}},
		},
		Runtime: driver.RuntimeAssets{TelemetryEndpoint: "http://collector:4317"},
	}
	if err := native.ValidateCreate(context.Background(), request); err != nil {
		t.Fatalf("ValidateCreate(domain request): %v", err)
	}
	input, err := kubernetesCreateInputFromBackend(request)
	if err != nil {
		t.Fatal(err)
	}
	if input.Name != request.Name || input.Workspace != "team-a" || input.GatewayID != "gateway-a" || input.PolicyPath != policyPath || len(input.Command) != 2 {
		t.Fatalf("mapped backend request=%+v", input)
	}

	request.Resources.PIDs = 128
	if err := native.ValidateCreate(context.Background(), request); err == nil || !strings.Contains(err.Error(), "PIDs") {
		t.Fatalf("unsupported PIDs request error=%v", err)
	}
	request.Resources.PIDs = 0
	request.Workspace.Kind = driver.WorkspaceHostPath
	request.Workspace.HostPath = "/gateway/workspaces/team-a"
	if err := native.ValidateCreate(context.Background(), request); err == nil || !strings.Contains(err.Error(), "Gateway-managed workspace") {
		t.Fatalf("host-path workspace request error=%v", err)
	}
	if _, err := native.Create(context.Background(), request); err == nil || !strings.Contains(err.Error(), "Gateway-managed workspace") {
		t.Fatalf("Create accepted an unsupported workspace: %v", err)
	}
}
