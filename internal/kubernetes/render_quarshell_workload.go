package kubernetes

import (
	"encoding/hex"
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/KLORQ/quarshell-driver/driver"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	quarshellWorkloadRuntimeDir     = "/quarshell/runtime"
	quarshellWorkloadPrivateDir     = "/quarshell/private"
	quarshellWorkloadRunDir         = "/run/quarshell"
	quarshellWorkloadBoundaryPort   = int64(5500)
	quarshellWorkloadRuntimeInit    = "quarshell-runtime-install"
	quarshellWorkloadSSHDName       = "quarshell-sshd"
	quarshellWorkloadCredentialName = "quarshell-private"
)

type quarshellWorkloadRenderParams struct {
	Base          agentSandboxRenderParams
	Command       []string
	Env           []string
	RestartPolicy string
	BoundaryPort  int64
}

// renderQuarshellAgentSandboxResource renders the untrusted workload Pod template
// with Quarshell PID 1, per-generation mTLS credentials, a private proxy route,
// and helper binaries copied from the digest-pinned trusted runtime image.
func renderQuarshellAgentSandboxResource(config SandboxDriverConfig, input quarshellWorkloadRenderParams) (map[string]any, error) {
	params, err := validateQuarshellWorkloadRenderParams(input)
	if err != nil {
		return nil, err
	}
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid kubernetes driver_config: %w", err)
	}
	if err := validateNoManagedRuntimeMountOverlap(config.Containers.Agent.VolumeMounts); err != nil {
		return nil, err
	}
	config, err = configWithRequestedGPU(config, params.Base.GPU, params.Base.GPUCount)
	if err != nil {
		return nil, err
	}
	podTemplate, err := renderQuarshellWorkloadPodTemplate(config, params)
	if err != nil {
		return nil, err
	}
	labels := map[string]any{
		labelManagedBy: managedByValue, labelSandboxID: params.Base.SandboxID,
		labelSandboxName: params.Base.SandboxName, labelSandboxWorkspace: params.Base.Workspace,
		labelGatewayID: params.Base.GatewayID,
	}
	annotations := map[string]any{
		labelSandboxID: params.Base.SandboxID, labelSandboxName: params.Base.SandboxName,
		labelSandboxWorkspace:       params.Base.Workspace,
		annotationRuntimeGeneration: params.Base.Generation,
	}
	resource := map[string]any{
		"apiVersion": sandboxAPIGroup + "/" + params.Base.APIVersion,
		"kind":       "Sandbox",
		"metadata": map[string]any{
			"name": params.Base.ResourceName, "namespace": params.Base.Namespace,
			"labels": labels, "annotations": annotations,
		},
		"spec": map[string]any{"podTemplate": podTemplate},
	}
	if quarshellWorkloadUsesDefaultWorkspace(config) {
		claimSpec := map[string]any{
			"accessModes": []any{"ReadWriteOnce"},
			"resources":   map[string]any{"requests": map[string]any{"storage": params.Base.WorkspaceStorageSize}},
		}
		if params.Base.WorkspaceStorageClass != "" {
			claimSpec["storageClassName"] = params.Base.WorkspaceStorageClass
		}
		resource["spec"].(map[string]any)["volumeClaimTemplates"] = []any{map[string]any{
			"metadata": map[string]any{"name": workspaceVolumeName}, "spec": claimSpec,
		}}
	}
	desiredState, err := sandboxDesiredStateFields(params.Base.APIVersion, false)
	if err != nil {
		return nil, err
	}
	for key, value := range desiredState {
		resource["spec"].(map[string]any)[key] = value
	}
	return resource, nil
}

func renderQuarshellWorkloadPodTemplate(config SandboxDriverConfig, params quarshellWorkloadRenderParams) (map[string]any, error) {
	fragment, err := renderDriverConfigFragment(config)
	if err != nil {
		return nil, err
	}
	fragmentSpec := fragment["spec"].(map[string]any)
	fragmentContainer := fragmentSpec["containers"].([]any)[0].(map[string]any)
	userMounts := fragmentContainer["volumeMounts"].([]any)
	userVolumes := fragmentSpec["volumes"].([]any)
	env, err := quarshellWorkloadEnvironment(params)
	if err != nil {
		return nil, err
	}
	command := append([]string(nil), params.Command...)
	if len(command) == 0 {
		command = []string{"sleep", "infinity"}
	}
	workloadCommand := []any{
		"--", quarshellWorkloadRuntimeDir + "/quarshell-init", "--policy", quarshellWorkloadPrivateDir + "/policy.yaml", "--",
	}
	for _, arg := range command {
		workloadCommand = append(workloadCommand, arg)
	}
	container := map[string]any{
		"name": "agent", "image": params.Base.Image,
		"command":    []any{quarshellWorkloadRuntimeDir + "/quarshell-supervisor"},
		"args":       workloadCommand,
		"workingDir": "/workspace",
		"env":        env,
		"ports": []any{map[string]any{
			"name": "target-relay", "containerPort": params.BoundaryPort, "protocol": "TCP",
		}},
		"securityContext": map[string]any{
			"runAsUser": params.Base.SandboxUID, "runAsGroup": params.Base.SandboxGID,
			"runAsNonRoot": true, "allowPrivilegeEscalation": false,
			"capabilities": map[string]any{"drop": []any{"ALL"}},
		},
		"volumeMounts": append(append([]any{}, userMounts...), []any{
			map[string]any{"name": "quarshell-runtime", "mountPath": quarshellWorkloadRuntimeDir, "readOnly": true},
			map[string]any{"name": quarshellWorkloadCredentialName, "mountPath": quarshellWorkloadPrivateDir, "readOnly": true},
			map[string]any{"name": "quarshell-run", "mountPath": quarshellWorkloadRunDir},
		}...),
	}
	if params.Base.ImagePullPolicy != "" {
		container["imagePullPolicy"] = params.Base.ImagePullPolicy
	}
	if resources, ok := fragmentContainer["resources"]; ok {
		container["resources"] = resources
	}
	if quarshellWorkloadUsesDefaultWorkspace(config) {
		container["volumeMounts"] = append(container["volumeMounts"].([]any), map[string]any{
			"name": workspaceVolumeName, "mountPath": "/workspace",
		})
	}
	sshd := quarshellWorkloadSSHDContainer(config, params)
	// The init image is trusted and digest-pinned. It only stages static helper
	// files into an in-memory volume; it never mounts the workload credentials.
	helperInstall := map[string]any{
		"name": quarshellWorkloadRuntimeInit, "image": params.Base.SandboxRuntimeImage,
		"command": []any{"/bin/sh", "-ceu"},
		"args": []any{strings.Join([]string{
			"install -m 0555 /usr/local/bin/quarshell-init " + quarshellWorkloadRuntimeDir + "/quarshell-init",
			"install -m 0555 /usr/local/bin/quarshell-supervisor " + quarshellWorkloadRuntimeDir + "/quarshell-supervisor",
			"install -m 0555 /usr/local/bin/quarshell-sshd " + quarshellWorkloadRuntimeDir + "/quarshell-sshd",
		}, " && ")},
		"securityContext": map[string]any{
			"runAsUser": params.Base.SandboxUID, "runAsGroup": params.Base.SandboxGID,
			"runAsNonRoot": true, "readOnlyRootFilesystem": true,
			"allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []any{"ALL"}},
		},
		"volumeMounts": []any{map[string]any{"name": "quarshell-runtime", "mountPath": quarshellWorkloadRuntimeDir}},
	}
	if params.Base.SandboxRuntimeImagePullPolicy != "" {
		helperInstall["imagePullPolicy"] = params.Base.SandboxRuntimeImagePullPolicy
	}
	initContainers := []any{helperInstall}
	if quarshellWorkloadUsesDefaultWorkspace(config) {
		initContainers = append(initContainers, map[string]any{
			"name": workspaceSeedContainerName, "image": params.Base.SandboxRuntimeImage,
			"command": []any{"/bin/sh", "-ceu"}, "args": []any{"mkdir -p /workspace"},
			"securityContext": map[string]any{
				"runAsUser": params.Base.SandboxUID, "runAsGroup": params.Base.SandboxGID,
				"runAsNonRoot": true, "readOnlyRootFilesystem": true,
				"allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []any{"ALL"}},
			},
			"volumeMounts": []any{map[string]any{"name": workspaceVolumeName, "mountPath": "/workspace"}},
		})
	}
	volumes := append(append([]any{}, userVolumes...), []any{
		map[string]any{"name": "quarshell-runtime", "emptyDir": map[string]any{"medium": "Memory", "sizeLimit": "16Mi"}},
		map[string]any{"name": quarshellWorkloadCredentialName, "secret": map[string]any{
			"secretName": params.Base.SandboxBootstrapSecretName, "defaultMode": int64(0o440),
		}},
		map[string]any{"name": "quarshell-run", "emptyDir": map[string]any{"medium": "Memory", "sizeLimit": "16Mi"}},
	}...)
	podLabels := map[string]any{
		labelSandboxID:    params.Base.SandboxID,
		labelBoundaryPair: strings.ToLower(params.Base.SandboxID),
		labelBoundaryRole: "workload",
	}
	podSpec := map[string]any{
		"hostNetwork": false, "hostPID": false, "hostIPC": false, "shareProcessNamespace": false,
		"serviceAccountName": params.Base.ServiceAccountName, "automountServiceAccountToken": false,
		"restartPolicy": "Never", "dnsPolicy": "ClusterFirst",
		"schedulingGates": []any{map[string]any{"name": sandboxBootstrapGate}},
		"securityContext": map[string]any{
			"runAsUser": params.Base.SandboxUID, "runAsGroup": params.Base.SandboxGID,
			"runAsNonRoot": true, "fsGroup": params.Base.SandboxGID,
			"fsGroupChangePolicy": "OnRootMismatch", "supplementalGroups": []any{},
			"supplementalGroupsPolicy": "Strict", "seccompProfile": map[string]any{"type": "RuntimeDefault"},
		},
		"containers": []any{container, sshd}, "initContainers": initContainers, "volumes": volumes,
	}
	for _, key := range []string{"nodeSelector", "tolerations", "priorityClassName"} {
		if value, exists := fragmentSpec[key]; exists {
			podSpec[key] = value
		}
	}
	runtimeClassName := params.Base.DefaultRuntimeClassName
	if config.Pod.RuntimeClassName != "" {
		runtimeClassName = config.Pod.RuntimeClassName
	}
	if runtimeClassName != "" {
		podSpec["runtimeClassName"] = runtimeClassName
	}
	if params.Base.EnableUserNamespaces {
		podSpec["hostUsers"] = false
	}
	if len(params.Base.ImagePullSecrets) != 0 {
		podSpec["imagePullSecrets"] = renderImagePullSecretRefs(params.Base.ImagePullSecrets)
	}
	return map[string]any{
		"metadata": map[string]any{"labels": podLabels, "annotations": map[string]any{labelSandboxID: params.Base.SandboxID}},
		"spec":     podSpec,
	}, nil
}

func quarshellWorkloadSSHDContainer(config SandboxDriverConfig, params quarshellWorkloadRenderParams) map[string]any {
	args := []any{
		"--socket", quarshellWorkloadRunDir + "/sshd.sock",
		"--init", quarshellWorkloadRuntimeDir + "/quarshell-init",
		"--workdir", "/workspace",
		"--shell", "/bin/sh",
	}
	mounts := []any{
		map[string]any{"name": "quarshell-runtime", "mountPath": quarshellWorkloadRuntimeDir, "readOnly": true},
		map[string]any{"name": "quarshell-run", "mountPath": quarshellWorkloadRunDir},
	}
	if quarshellWorkloadUsesDefaultWorkspace(config) {
		mounts = append(mounts, map[string]any{"name": workspaceVolumeName, "mountPath": "/workspace"})
	} else {
		for _, mount := range config.Containers.Agent.VolumeMounts {
			mountPath := strings.TrimSuffix(mount.MountPath, "/")
			if mountPath == "/workspace" || strings.HasPrefix(mountPath, "/workspace/") {
				mounts = append(mounts, volumeMountToUnstructured(mount))
			}
		}
	}
	return map[string]any{
		"name": "quarshell-sshd", "image": params.Base.SandboxRuntimeImage,
		"command": []any{quarshellWorkloadRuntimeDir + "/quarshell-sshd"}, "args": args,
		"workingDir": "/workspace",
		"readinessProbe": map[string]any{
			"exec":             map[string]any{"command": []any{"/bin/sh", "-ceu", "test -S " + quarshellWorkloadRunDir + "/sshd.sock"}},
			"timeoutSeconds":   int64(1),
			"periodSeconds":    int64(1),
			"successThreshold": int64(1),
			"failureThreshold": int64(30),
		},
		"securityContext": map[string]any{
			"runAsUser": params.Base.SandboxUID, "runAsGroup": params.Base.SandboxGID,
			"runAsNonRoot": true, "readOnlyRootFilesystem": true,
			"allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []any{"ALL"}},
		},
		"resources":    map[string]any{"requests": map[string]any{"cpu": "25m", "memory": "64Mi"}, "limits": map[string]any{"cpu": "250m", "memory": "128Mi"}},
		"volumeMounts": mounts,
	}
}

func volumeMountToUnstructured(mount VolumeMountConfig) map[string]any {
	result := map[string]any{"name": mount.Name, "mountPath": mount.MountPath}
	if mount.ReadOnly != nil && *mount.ReadOnly {
		result["readOnly"] = true
	}
	if mount.SubPath != "" {
		result["subPath"] = mount.SubPath
	}
	return result
}

func quarshellWorkloadEnvironment(params quarshellWorkloadRenderParams) ([]any, error) {
	values := make(map[string]string, len(params.Env)+16)
	for _, entry := range params.Env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !kubernetesEnvKey.MatchString(key) || strings.ContainsRune(value, '\x00') {
			return nil, fmt.Errorf("quarshell workload environment contains an invalid entry")
		}
		if isQuarshellProxyManagedEnvironmentKey(key) || isQuarshellWorkloadManagedEnvironmentKey(key) {
			return nil, fmt.Errorf("quarshell workload environment cannot override a managed runtime setting")
		}
		if _, duplicate := values[key]; duplicate {
			return nil, fmt.Errorf("quarshell workload environment contains a duplicate key")
		}
		values[key] = value
	}
	id := strings.ToLower(params.Base.SandboxID)
	controlService := runtimeServiceFQDN("os-supervisor-"+id, params.Base.Namespace)
	workloadService := runtimeServiceFQDN("os-boundary-"+id, params.Base.Namespace)
	proxyService := runtimeServiceFQDN("os-proxy-"+id, params.Base.Namespace)
	endpoint := net.JoinHostPort(controlService, fmt.Sprint(supervisorControlPort))
	targetListen := net.JoinHostPort("0.0.0.0", fmt.Sprint(params.BoundaryPort))
	proxyURL := "http://" + net.JoinHostPort(proxyService, fmt.Sprint(quarshellProxyListenPort))
	noProxy := strings.Join([]string{"localhost", "127.0.0.1", "::1", workloadService, controlService, proxyService}, ",")
	managed := map[string]string{
		driver.MainProcessRestartPolicyEnv:         params.RestartPolicy,
		"QUARSHELL_POLICY":                         quarshellWorkloadPrivateDir + "/policy.yaml",
		"QUARSHELL_SUPERVISOR_CONTROL_ENDPOINT":    endpoint,
		"QUARSHELL_SUPERVISOR_CONTROL_CLIENT_CA":   quarshellWorkloadPrivateDir + "/control-client-ca.pem",
		"QUARSHELL_SUPERVISOR_CONTROL_CLIENT_CERT": quarshellWorkloadPrivateDir + "/control-client.crt",
		"QUARSHELL_SUPERVISOR_CONTROL_CLIENT_KEY":  quarshellWorkloadPrivateDir + "/control-client.key",
		"QUARSHELL_SUPERVISOR_CONTROL_PRIVATE_DIR": quarshellWorkloadPrivateDir,
		"QUARSHELL_RELAY_TARGET_LISTEN":            targetListen,
		"QUARSHELL_RELAY_TARGET_CLIENT_CA":         quarshellWorkloadPrivateDir + "/target-client-ca.pem",
		"QUARSHELL_RELAY_TARGET_SERVER_CERT":       quarshellWorkloadPrivateDir + "/target-server.crt",
		"QUARSHELL_RELAY_TARGET_SERVER_KEY":        quarshellWorkloadPrivateDir + "/target-server.key",
		"QUARSHELL_RELAY_TARGET_SSH_SOCKET":        quarshellWorkloadRunDir + "/sshd.sock",
		"HTTP_PROXY":                               proxyURL, "HTTPS_PROXY": proxyURL, "ALL_PROXY": proxyURL,
		"http_proxy": proxyURL, "https_proxy": proxyURL, "all_proxy": proxyURL,
		"NO_PROXY": noProxy, "no_proxy": noProxy,
	}
	for key, value := range managed {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]any, 0, len(keys))
	for _, key := range keys {
		env = append(env, map[string]any{"name": key, "value": values[key]})
	}
	return env, nil
}

func isQuarshellWorkloadManagedEnvironmentKey(key string) bool {
	switch key {
	case driver.MainProcessRestartPolicyEnv, "QUARSHELL_POLICY", "QUARSHELL_SUPERVISOR_CONTROL_SOCKET", "QUARSHELL_SUPERVISOR_CONTROL_ENDPOINT", "QUARSHELL_SUPERVISOR_CONTROL_LISTEN", "QUARSHELL_SUPERVISOR_CONTROL_CLIENT_CA", "QUARSHELL_SUPERVISOR_CONTROL_CLIENT_CERT", "QUARSHELL_SUPERVISOR_CONTROL_CLIENT_KEY", "QUARSHELL_SUPERVISOR_CONTROL_PRIVATE_DIR", "QUARSHELL_SUPERVISOR_CONTROL_SERVER_CA", "QUARSHELL_SUPERVISOR_CONTROL_SERVER_CERT", "QUARSHELL_SUPERVISOR_CONTROL_SERVER_KEY",
		"QUARSHELL_RELAY_TARGET_ENDPOINT", "QUARSHELL_RELAY_TARGET_LISTEN", "QUARSHELL_RELAY_TARGET_CLIENT_CA", "QUARSHELL_RELAY_TARGET_SERVER_CERT", "QUARSHELL_RELAY_TARGET_SERVER_KEY", "QUARSHELL_RELAY_TARGET_SSH_SOCKET", "QUARSHELL_RELAY_TARGET_SERVER_CA", "QUARSHELL_RELAY_TARGET_CLIENT_CERT", "QUARSHELL_RELAY_TARGET_CLIENT_KEY", "QUARSHELL_RELAY_TARGET_SOCKET",
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy":
		return true
	default:
		return false
	}
}

func validateQuarshellWorkloadRenderParams(input quarshellWorkloadRenderParams) (quarshellWorkloadRenderParams, error) {
	params := input
	base, err := validateAgentSandboxRenderParams(input.Base)
	if err != nil {
		return params, err
	}
	params.Base = base
	imageParts := strings.Split(base.SandboxRuntimeImage, "@sha256:")
	if len(imageParts) != 2 || strings.TrimSpace(imageParts[0]) == "" || len(imageParts[1]) != 64 {
		return params, fmt.Errorf("quarshell helper image must be pinned by sha256 digest")
	}
	if _, err := hex.DecodeString(imageParts[1]); err != nil {
		return params, fmt.Errorf("quarshell helper image must use a valid sha256 digest")
	}
	if params.BoundaryPort == 0 {
		params.BoundaryPort = quarshellWorkloadBoundaryPort
	}
	if params.BoundaryPort < 1024 || params.BoundaryPort > 65535 {
		return params, fmt.Errorf("quarshell workload boundary port must be between 1024 and 65535")
	}
	if params.Base.WorkspaceStorageSize == "" {
		params.Base.WorkspaceStorageSize = defaultWorkspaceStorage
	}
	if _, err := resource.ParseQuantity(params.Base.WorkspaceStorageSize); err != nil {
		return params, fmt.Errorf("invalid workspace storage size %q", params.Base.WorkspaceStorageSize)
	}
	if params.Base.SandboxUID < 1 || params.Base.SandboxGID < 1 || params.Base.SandboxUID > int64(^uint32(0)) || params.Base.SandboxGID > int64(^uint32(0)) {
		return params, fmt.Errorf("quarshell workload identity must use non-root 32-bit UID/GID values")
	}
	for _, arg := range params.Command {
		if strings.ContainsRune(arg, '\x00') {
			return params, fmt.Errorf("quarshell workload command contains a NUL byte")
		}
	}
	return params, nil
}

func quarshellWorkloadUsesDefaultWorkspace(config SandboxDriverConfig) bool {
	for _, mount := range config.Containers.Agent.VolumeMounts {
		mountPath := strings.TrimSuffix(mount.MountPath, "/")
		if mountPath == "/workspace" || strings.HasPrefix(mountPath, "/workspace/") {
			return false
		}
	}
	return true
}
