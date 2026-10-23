package kubernetes

import (
	"strings"
	"testing"
)

func TestBackendConfigDefaultRuntimeClassName(t *testing.T) {
	t.Setenv("QUARSHELL_GATEWAY_ID", "test-gateway")
	digest := strings.Repeat("a", 64)
	config, err := BackendConfigFromMap(map[string]any{
		"default_runtime_class_name":        "gvisor",
		"proxy_image":                       "registry.example/proxy@sha256:" + digest,
		"sandbox_runtime_image":             "registry.example/runtime@sha256:" + digest,
		"grpc_endpoint":                     "http://gateway.example:8080",
		"image_pull_policy":                 "if_not_present",
		"proxy_image_pull_policy":           "always",
		"sandbox_runtime_image_pull_policy": "Never",
	})
	if err != nil {
		t.Fatalf("BackendConfigFromMap: %v", err)
	}
	if config.DefaultRuntimeClassName != "gvisor" {
		t.Fatalf("runtime class = %q", config.DefaultRuntimeClassName)
	}
	if config.ImagePullPolicy != "IfNotPresent" || config.ProxyImagePullPolicy != "Always" || config.SandboxRuntimeImagePullPolicy != "Never" {
		t.Fatalf("pull policies = %q/%q/%q", config.ImagePullPolicy, config.ProxyImagePullPolicy, config.SandboxRuntimeImagePullPolicy)
	}
	if _, err := BackendConfigFromMap(map[string]any{"image_pull_policy": "sometimes"}); err == nil {
		t.Fatalf("invalid image pull policy accepted")
	}
	if _, err := BackendConfigFromMap(map[string]any{"runtime_class_name": "gvisor"}); err == nil {
		t.Fatalf("legacy runtime_class_name accepted")
	}
}
