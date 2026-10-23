package kubernetes

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/KLORQ/quarshell-driver/driver"
)

func TestDriverCreateRunsQuarshellCreateSequence(t *testing.T) {
	workspace := "team-a"
	policyPath := t.TempDir() + "/policy.yaml"
	if err := os.WriteFile(policyPath, []byte("version: 1\n"), 0o600); err != nil {
		t.Fatalf("write policy fixture: %v", err)
	}
	config := BackendConfig{
		GatewayID: "gateway-1", ProxyImage: "ghcr.io/quarshell/quarshell-proxy@sha256:" + strings.Repeat("a", 64),
		SandboxRuntimeImage: "ghcr.io/quarshell/runtime@sha256:" + strings.Repeat("b", 64),
		GRPCEndpoint:        "gateway.example:7443",
	}
	config.applyDefaults()
	backend := newFakeAgentSandboxCreateBackend()
	backend.useRequestedQuarshellPods = true
	backend.sandboxID = "team-a-demo"
	d := newWithClient(newFakeAgentSandboxDynamicClient(), config.GatewayID)
	d.runtimeConfig = config
	d.apiVersions.version = sandboxAPIVersionV1Beta1
	d.createBackend = backend
	request := driver.BackendCreateRequest{
		SandboxID: "team-a-demo", Name: "team-a-demo",
		Workspace: driver.WorkspaceTarget{Kind: driver.WorkspaceGatewayManaged, ID: workspace, Persist: true},
		Workload:  driver.WorkloadSpec{Image: "registry.example/sandbox:v1", Command: []string{"/bin/sh"}},
		Network: driver.NetworkSpec{
			GatewayID: config.GatewayID, GatewayURL: "http://gateway.example:7443",
			Proxy: &driver.ProxySpec{PolicyPath: policyPath, Environment: []string{
				"QUARSHELL_GATEWAY_URL=http://gateway.example:7443",
				"QUARSHELL_SANDBOX=team-a-demo",
				"QUARSHELL_SANDBOX_TOKEN=test-sandbox-token",
			}},
		},
	}
	native := d
	if err := native.ValidateCreate(context.Background(), request); err != nil {
		t.Fatalf("ValidateCreate(): %v", err)
	}
	handle, err := native.Create(context.Background(), request)
	if err != nil {
		t.Fatalf("Backend.Create() error = %v", err)
	}
	if !strings.HasPrefix(string(handle.ID), kubernetesRuntimeIDPrefix) || handle.Name != request.Name || handle.Image != request.Workload.Image {
		t.Fatalf("Backend.Create() handle = %#v", handle)
	}
	if len(backend.createdSecrets) != 3 || backend.calls[len(backend.calls)-1] != "mark-runtime-released" {
		t.Fatalf("Backend.Create() secrets/calls = %d/%#v", len(backend.createdSecrets), backend.calls)
	}

	backend.sandboxID = "team-a-native"
	backend.sandbox.Name = "team-a-native"
	backend.sandbox.Namespace, _, _, err = renderWorkspaceIsolationResources(config.GatewayID, workspace)
	if err != nil {
		t.Fatal(err)
	}
	request = driver.BackendCreateRequest{
		SandboxID: "team-a-native", Name: "team-a-native",
		Workspace: driver.WorkspaceTarget{Kind: driver.WorkspaceGatewayManaged, ID: workspace, Persist: true},
		Workload:  driver.WorkloadSpec{Image: "registry.example/sandbox:v1", Command: []string{"/bin/sh"}},
		Network: driver.NetworkSpec{
			GatewayID: config.GatewayID, GatewayURL: "http://gateway.example:7443",
			Proxy: &driver.ProxySpec{PolicyPath: policyPath, Environment: []string{"QUARSHELL_GATEWAY_URL=http://gateway.example:7443", "QUARSHELL_SANDBOX=team-a-native", "QUARSHELL_SANDBOX_TOKEN=native-token"}},
		},
	}
	if err := native.ValidateCreate(context.Background(), request); err != nil {
		t.Fatalf("Backend.ValidateCreate(): %v", err)
	}
	nativeHandle, err := native.Create(context.Background(), request)
	if err != nil {
		t.Fatalf("Backend.Create(domain request): %v", err)
	}
	if !strings.HasPrefix(string(nativeHandle.ID), kubernetesRuntimeIDPrefix) || nativeHandle.Name != request.Name || nativeHandle.Image != request.Workload.Image {
		t.Fatalf("Backend.Create() handle=%#v", nativeHandle)
	}
}

func TestKubernetesBackendRegistrationUsesValidatedConfig(t *testing.T) {
	backend, registered, err := driver.OpenRegisteredBackendWithConfig("kubernetes", map[string]any{
		internalGatewayIDConfigKey: "gateway-1",
		"proxy_image":              "ghcr.io/quarshell/proxy@sha256:" + strings.Repeat("a", 64),
		"sandbox_runtime_image":    "ghcr.io/quarshell/runtime@sha256:" + strings.Repeat("b", 64),
		"grpc_endpoint":            "gateway.example:7443",
	})
	if err != nil || !registered || backend == nil {
		t.Fatalf("OpenRegisteredBackendWithConfig(kubernetes) = (%T, %t, %v)", backend, registered, err)
	}
	if _, registered, err := driver.OpenRegisteredBackendWithConfig("kubernetes", map[string]any{"unknown": true}); err == nil || !registered {
		t.Fatalf("native Backend registration did not reject an unknown setting: registered=%t err=%v", registered, err)
	}
}
