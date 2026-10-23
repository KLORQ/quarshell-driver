package kubernetes

import (
	"strings"
	"testing"

	"github.com/KLORQ/quarshell-driver/driver"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestRenderQuarshellAgentSandboxResourceInstallsRuntimeAndUsesPrivateProxyBoundary(t *testing.T) {
	params := testQuarshellWorkloadRenderParams()
	params.RestartPolicy = "on-failure"
	resource, err := renderQuarshellAgentSandboxResource(SandboxDriverConfig{}, params)
	if err != nil {
		t.Fatalf("renderQuarshellAgentSandboxResource() error = %v", err)
	}
	metadata := renderMap(t, resource["metadata"], "sandbox metadata")
	annotations := renderMap(t, metadata["annotations"], "sandbox annotations")
	if annotations[annotationRuntimeGeneration] != params.Base.Generation {
		t.Fatalf("runtime generation annotation = %#v", annotations)
	}
	spec := renderMap(t, resource["spec"], "sandbox spec")
	if _, ok := spec["volumeClaimTemplates"]; !ok {
		t.Fatal("default workspace PVC was not rendered")
	}
	template := renderMap(t, spec["podTemplate"], "pod template")
	labels := renderMap(t, renderMap(t, template["metadata"], "pod metadata")["labels"], "pod labels")
	if labels[labelBoundaryPair] != params.Base.SandboxID || labels[labelBoundaryRole] != "workload" {
		t.Fatalf("workload boundary labels = %#v", labels)
	}
	podSpec := renderMap(t, template["spec"], "pod spec")
	if podSpec["automountServiceAccountToken"] != false || podSpec["dnsPolicy"] != "ClusterFirst" {
		t.Fatalf("workload Pod identity/DNS settings = %#v", podSpec)
	}
	if got := renderList(t, podSpec["schedulingGates"], "scheduling gates"); len(got) != 1 || renderMap(t, got[0], "scheduling gate")["name"] != sandboxBootstrapGate {
		t.Fatalf("workload scheduling gates = %#v", got)
	}
	init := renderList(t, podSpec["initContainers"], "init containers")
	if len(init) != 2 {
		t.Fatalf("init container count = %d; want helper install and workspace init", len(init))
	}
	helper := renderMap(t, init[0], "runtime helper init")
	if helper["image"] != params.Base.SandboxRuntimeImage || !strings.Contains(strings.Join(toStrings(t, helper["args"]), " "), "quarshell-supervisor") {
		t.Fatalf("runtime helper init = %#v", helper)
	}
	if quarshellHasVolumeMount(t, helper, quarshellWorkloadCredentialName) {
		t.Fatal("trusted helper install init received workload credentials")
	}
	containers := renderList(t, podSpec["containers"], "containers")
	if len(containers) != 2 {
		t.Fatalf("workload container count = %d; want agent and relay-only sshd", len(containers))
	}
	container := renderMap(t, containers[0], "agent container")
	if got := renderList(t, container["command"], "agent command"); len(got) != 1 || got[0] != quarshellWorkloadRuntimeDir+"/quarshell-supervisor" {
		t.Fatalf("agent command = %#v", got)
	}
	args := toStrings(t, container["args"])
	if strings.Join(args, " ") != "-- /quarshell/runtime/quarshell-init --policy /quarshell/private/policy.yaml -- python3 -m http.server" {
		t.Fatalf("agent args = %#v", args)
	}
	if renderEnvValue(t, container, "QUARSHELL_SUPERVISOR_CONTROL_ENDPOINT") != runtimeServiceFQDN("os-supervisor-"+params.Base.SandboxID, params.Base.Namespace)+":5502" {
		t.Fatalf("control endpoint env = %#v", container["env"])
	}
	if got := renderEnvValue(t, container, driver.MainProcessRestartPolicyEnv); got != "on-failure" {
		t.Fatalf("main process restart policy = %q; want on-failure", got)
	}
	proxyURL := "http://" + runtimeServiceFQDN("os-proxy-"+params.Base.SandboxID, params.Base.Namespace) + ":3128"
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		if got := renderEnvValue(t, container, key); got != proxyURL {
			t.Fatalf("%s = %q; want %q", key, got, proxyURL)
		}
	}
	if hasEnv(container, "QUARSHELL_SANDBOX_TOKEN") || hasEnv(container, "QUARSHELL_RELAY_TARGET_CLIENT_KEY") {
		t.Fatalf("workload Pod contains a gateway token or key override: %#v", container["env"])
	}
	sshd := renderMap(t, containers[1], "relay-only sshd container")
	if sshd["name"] != quarshellWorkloadSSHDName || !strings.Contains(strings.Join(toStrings(t, sshd["command"]), " "), "quarshell-sshd") {
		t.Fatalf("relay-only sshd container = %#v", sshd)
	}
	if quarshellHasVolumeMount(t, sshd, quarshellWorkloadCredentialName) || quarshellHasVolumeMount(t, sshd, "quarshell-run") == false {
		t.Fatal("relay-only sshd must share only the runtime helper and SSH socket volumes")
	}
	volumes := renderList(t, podSpec["volumes"], "volumes")
	credentials := findRenderMap(t, volumes, quarshellWorkloadCredentialName)
	secret := renderMap(t, credentials["secret"], "credential Secret volume")
	if secret["secretName"] != params.Base.SandboxBootstrapSecretName {
		t.Fatalf("workload runtime Secret volume = %#v", secret)
	}
}

func TestQuarshellWorkloadPodIdentityVerifiesAdmittedTemplate(t *testing.T) {
	params := testQuarshellWorkloadRenderParams()
	resource, err := renderQuarshellAgentSandboxResource(SandboxDriverConfig{}, params)
	if err != nil {
		t.Fatalf("renderQuarshellAgentSandboxResource() error = %v", err)
	}
	spec := renderMap(t, resource["spec"], "sandbox spec")
	template := renderMap(t, spec["podTemplate"], "pod template")
	templateMetadata := renderMap(t, template["metadata"], "pod template metadata")
	podSpec := renderMap(t, template["spec"], "pod template spec")
	actualSpec := cloneAnyMap(podSpec)
	actualSpec["volumes"] = sandboxPodVolumesWithClaimTemplates(resource, podSpec)
	labels := renderMap(t, templateMetadata["labels"], "pod template labels")
	pod := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"name": params.Base.ResourceName, "namespace": params.Base.Namespace, "uid": "workload-pod-uid",
			"labels":          labels,
			"ownerReferences": []any{map[string]any{"kind": "Sandbox", "uid": "sandbox-cr-uid"}},
		},
		"spec": actualSpec,
	}}
	identity := workloadIdentityFromPod(pod, resource)
	runtimeParams := testAgentSandboxRuntimeRenderParams()
	runtimeParams.Generation = params.Base.Generation
	runtimeParams.QuarshellProxyImage = testQuarshellProxyPodRenderParams().ProxyImage
	if err := validateAgentSandboxWorkloadIdentity(identity, runtimeParams, "sandbox-cr-uid", resource); err != nil {
		t.Fatalf("validateAgentSandboxWorkloadIdentity() error = %v", err)
	}
}

func TestQuarshellWorkloadEnvironmentRejectsCredentialAndProxyOverrides(t *testing.T) {
	for _, entry := range []string{
		"QUARSHELL_SUPERVISOR_CONTROL_ENDPOINT=attacker:1",
		"QUARSHELL_RELAY_TARGET_SERVER_KEY=/tmp/key",
		driver.MainProcessRestartPolicyEnv + "=always",
		"HTTP_PROXY=http://attacker:1",
	} {
		params := testQuarshellWorkloadRenderParams()
		params.Env = []string{entry}
		if _, err := quarshellWorkloadEnvironment(params); err == nil {
			t.Errorf("quarshellWorkloadEnvironment(%q) succeeded; want managed env rejection", entry)
		}
	}
}

func TestRenderQuarshellAgentSandboxResourceRejectsUnpinnedHelperImage(t *testing.T) {
	params := testQuarshellWorkloadRenderParams()
	params.Base.SandboxRuntimeImage = "ghcr.io/klorq/quarshell/sandboxes/base:latest"
	if _, err := renderQuarshellAgentSandboxResource(SandboxDriverConfig{}, params); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("renderQuarshellAgentSandboxResource() error = %v; want digest pin requirement", err)
	}
}

func testQuarshellWorkloadRenderParams() quarshellWorkloadRenderParams {
	base := testAgentSandboxRenderParams()
	base.SandboxRuntimeImage = "ghcr.io/klorq/quarshell/sandboxes/base@sha256:" + strings.Repeat("a", 64)
	base.SandboxRuntimeImagePullPolicy = "IfNotPresent"
	return quarshellWorkloadRenderParams{
		Base: base, Command: []string{"python3", "-m", "http.server"}, Env: []string{"APP_MODE=demo"}, BoundaryPort: 5500,
	}
}

func toStrings(t *testing.T, values any) []string {
	t.Helper()
	list := renderList(t, values, "string list")
	out := make([]string, 0, len(list))
	for _, value := range list {
		text, ok := value.(string)
		if !ok {
			t.Fatalf("list entry has type %T; want string", value)
		}
		out = append(out, text)
	}
	return out
}

func quarshellHasVolumeMount(t *testing.T, container map[string]any, name string) bool {
	t.Helper()
	for _, raw := range renderList(t, container["volumeMounts"], "volume mounts") {
		if renderMap(t, raw, "volume mount")["name"] == name {
			return true
		}
	}
	return false
}
