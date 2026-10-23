package kubernetes

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	quarshellRuntimePolicyKey  = "policy.yaml"
	quarshellProxyTokenKey     = "sandbox-token"
	quarshellGatewayTLSCAKey   = "gateway-ca.pem"
	quarshellGatewayTLSCertKey = "gateway-client.crt"
	quarshellGatewayTLSKeyKey  = "gateway-client.key"
	proxyEnvSecretPrefix       = "os-proxy-env"
	maxRuntimePolicyBytes      = 768 << 10
	maxKubernetesSecretBytes   = 1 << 20
)

var kubernetesEnvKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// buildQuarshellRuntimeSecrets prepares the per-generation Secret payloads used by
// the Quarshell workload and trusted proxy Pods. The bearer token is mounted as a
// file, while provider credentials are exposed only to the trusted proxy
// container through a separate env Secret.
func buildQuarshellRuntimeSecrets(identity agentSandboxCreateIdentity, policy []byte, proxyEnv []string) ([]map[string]any, error) {
	if len(policy) == 0 || len(policy) > maxRuntimePolicyBytes {
		return nil, fmt.Errorf("quarshell runtime policy must contain between 1 byte and %d bytes", maxRuntimePolicyBytes)
	}
	if err := validateQuarshellSecretIdentity(identity); err != nil {
		return nil, err
	}

	token, credentials, err := splitQuarshellProxyEnvironment(proxyEnv)
	if err != nil {
		return nil, err
	}
	workloadFiles := map[string][]byte{quarshellRuntimePolicyKey: policy}
	supervisorFiles := map[string][]byte{
		quarshellRuntimePolicyKey: policy,
		quarshellProxyTokenKey:    []byte(token + "\n"),
	}
	for name, files := range map[string]map[string][]byte{
		"workload": workloadFiles, "supervisor": supervisorFiles, "proxy environment": credentials,
	} {
		if err := validateQuarshellSecretPayload(name, files); err != nil {
			return nil, err
		}
	}
	workload := quarshellRuntimeSecret(
		generationSecretName("os-sandbox", identity.SandboxID, identity.Generation),
		identity.SandboxID,
		identity.Namespace,
		"workload",
		identity.SandboxName,
		identity.SandboxUID,
		identity.APIVersion,
		workloadFiles,
	)
	supervisor := quarshellRuntimeSecret(
		generationSecretName("os-supervisor", identity.SandboxID, identity.Generation),
		identity.SandboxID,
		identity.Namespace,
		"supervisor",
		identity.SandboxName,
		identity.SandboxUID,
		identity.APIVersion,
		supervisorFiles,
	)
	proxyEnvironment := quarshellRuntimeSecret(
		generationSecretName(proxyEnvSecretPrefix, identity.SandboxID, identity.Generation),
		identity.SandboxID,
		identity.Namespace,
		"supervisor",
		identity.SandboxName,
		identity.SandboxUID,
		identity.APIVersion,
		credentials,
	)
	return []map[string]any{workload, supervisor, proxyEnvironment}, nil
}

// buildQuarshellGenerationSecrets builds the complete set of immutable Quarshell
// Secrets for one runtime generation, including the independent mTLS bridge
// identities. It validates the final payload sizes after certificates have
// been attached so Kubernetes' Secret size limit covers the actual objects.
func buildQuarshellGenerationSecrets(identity agentSandboxCreateIdentity, policy []byte, proxyEnv []string) ([]map[string]any, error) {
	return buildQuarshellGenerationSecretsWithGuestTLS(identity, policy, proxyEnv, nil)
}

type guestTLSMaterial struct {
	CA   []byte
	Cert []byte
	Key  []byte
}

func buildQuarshellGenerationSecretsWithGuestTLS(identity agentSandboxCreateIdentity, policy []byte, proxyEnv []string, guestTLS *guestTLSMaterial) ([]map[string]any, error) {
	secrets, err := buildQuarshellRuntimeSecrets(identity, policy, proxyEnv)
	if err != nil {
		return nil, err
	}
	if err := attachRuntimeBridgeCredentials(secrets, identity); err != nil {
		return nil, fmt.Errorf("attach Quarshell runtime bridge credentials: %w", err)
	}
	if guestTLS != nil {
		if err := attachGuestTLSCredentials(secrets, identity, *guestTLS); err != nil {
			return nil, err
		}
	}
	if err := validateQuarshellGenerationSecrets(secrets, identity); err != nil {
		return nil, err
	}
	return secrets, nil
}

// rotateQuarshellGenerationSecrets copies the sandbox's current policy and
// provider credentials into a new immutable generation while replacing the
// supervisor bearer and minting fresh bridge certificates.
func rotateQuarshellGenerationSecrets(current []map[string]any, currentGeneration string, identity agentSandboxCreateIdentity, token string) ([]map[string]any, error) {
	return rotateQuarshellGenerationSecretsWithPolicy(current, currentGeneration, identity, token, nil)
}

func rotateQuarshellGenerationSecretsWithPolicy(current []map[string]any, currentGeneration string, identity agentSandboxCreateIdentity, token string, policyOverride []byte) ([]map[string]any, error) {
	if strings.TrimSpace(token) == "" || token != strings.TrimSpace(token) {
		return nil, fmt.Errorf("replacement Quarshell sandbox token is required")
	}
	if generationSuffix(currentGeneration) == "" || currentGeneration == identity.Generation {
		return nil, fmt.Errorf("current Quarshell runtime generation is invalid")
	}
	var policy []byte
	var workloadPolicy []byte
	var guestTLS guestTLSMaterial
	var tlsParts int
	var proxyEnv []string
	for _, secret := range current {
		metadata, _ := secret["metadata"].(map[string]any)
		name := stringValue(metadata["name"])
		data, _ := secret["data"].(map[string]any)
		switch name {
		case generationSecretName("os-sandbox", identity.SandboxID, currentGeneration):
			value, err := decodeSecretData(data, quarshellRuntimePolicyKey)
			if err != nil {
				return nil, err
			}
			workloadPolicy = value
		case generationSecretName("os-supervisor", identity.SandboxID, currentGeneration):
			value, err := decodeSecretData(data, quarshellRuntimePolicyKey)
			if err != nil {
				return nil, err
			}
			policy = value
			for key, target := range map[string]*[]byte{
				quarshellGatewayTLSCAKey: &guestTLS.CA, quarshellGatewayTLSCertKey: &guestTLS.Cert, quarshellGatewayTLSKeyKey: &guestTLS.Key,
			} {
				encoded, exists := data[key]
				if !exists {
					continue
				}
				text, ok := encoded.(string)
				if !ok {
					return nil, fmt.Errorf("stored guest TLS key %q is malformed", key)
				}
				decoded, err := base64.StdEncoding.DecodeString(text)
				if err != nil || len(decoded) == 0 {
					return nil, fmt.Errorf("stored guest TLS key %q is malformed", key)
				}
				*target = decoded
				tlsParts++
			}
		case generationSecretName(proxyEnvSecretPrefix, identity.SandboxID, currentGeneration):
			for key, raw := range data {
				encoded, ok := raw.(string)
				if !ok {
					return nil, fmt.Errorf("stored provider environment value %q is malformed", key)
				}
				value, err := base64.StdEncoding.DecodeString(encoded)
				if err != nil {
					return nil, fmt.Errorf("stored provider environment value %q is malformed", key)
				}
				proxyEnv = append(proxyEnv, key+"="+string(value))
			}
		default:
			return nil, fmt.Errorf("stored Quarshell generation contains an unexpected Secret")
		}
	}
	if len(policy) == 0 || len(workloadPolicy) == 0 || string(policy) != string(workloadPolicy) {
		return nil, fmt.Errorf("stored workload and supervisor policies do not match")
	}
	if policyOverride != nil {
		if len(policyOverride) == 0 || len(policyOverride) > maxRuntimePolicyBytes {
			return nil, fmt.Errorf("replacement Quarshell policy must contain between 1 byte and %d bytes", maxRuntimePolicyBytes)
		}
		policy = append([]byte(nil), policyOverride...)
	}
	if tlsParts != 0 && tlsParts != 3 {
		return nil, fmt.Errorf("stored guest TLS generation must contain CA, certificate, and key")
	}
	proxyEnv = append(proxyEnv, "QUARSHELL_SANDBOX_TOKEN="+token)
	var guest *guestTLSMaterial
	if tlsParts == 3 {
		guest = &guestTLS
	}
	return buildQuarshellGenerationSecretsWithGuestTLS(identity, policy, proxyEnv, guest)
}

func attachGuestTLSCredentials(secrets []map[string]any, identity agentSandboxCreateIdentity, material guestTLSMaterial) error {
	if len(material.CA) == 0 || len(material.Cert) == 0 || len(material.Key) == 0 {
		return fmt.Errorf("guest TLS CA, certificate, and key contents are all required")
	}
	name := generationSecretName("os-supervisor", identity.SandboxID, identity.Generation)
	for _, secret := range secrets {
		metadata, _ := secret["metadata"].(map[string]any)
		if metadata["name"] != name {
			continue
		}
		data, _ := secret["data"].(map[string]any)
		for key, value := range map[string][]byte{
			quarshellGatewayTLSCAKey: material.CA, quarshellGatewayTLSCertKey: material.Cert, quarshellGatewayTLSKeyKey: material.Key,
		} {
			if _, exists := data[key]; exists {
				return fmt.Errorf("quarshell supervisor Secret already contains gateway TLS key %q", key)
			}
			data[key] = base64.StdEncoding.EncodeToString(value)
		}
		return nil
	}
	return fmt.Errorf("quarshell supervisor Secret is required for guest TLS credentials")
}

func validateQuarshellGenerationSecrets(secrets []map[string]any, identity agentSandboxCreateIdentity) error {
	if err := validateQuarshellSecretIdentity(identity); err != nil {
		return err
	}
	if len(secrets) != 3 {
		return fmt.Errorf("quarshell runtime generation must create exactly three separated Secrets")
	}
	type expectedSecret struct {
		name      string
		ownerName string
		ownerUID  string
		ownerAPI  string
		required  map[string]struct{}
		allowed   map[string]struct{}
		role      string
	}
	workloadName := generationSecretName("os-sandbox", identity.SandboxID, identity.Generation)
	supervisorName := generationSecretName("os-supervisor", identity.SandboxID, identity.Generation)
	proxyEnvironmentName := generationSecretName(proxyEnvSecretPrefix, identity.SandboxID, identity.Generation)
	expected := map[string]expectedSecret{
		workloadName: {
			name: workloadName, ownerName: identity.SandboxName, ownerUID: identity.SandboxUID, ownerAPI: identity.APIVersion, role: "workload",
			required: secretKeySet(quarshellRuntimePolicyKey, bridgeTargetClientCAFile, bridgeTargetServerCert, bridgeTargetServerKey,
				bridgeControlClientCAFile, bridgeControlClientCert, bridgeControlClientKey),
		},
		supervisorName: {
			name: supervisorName, ownerName: identity.SandboxName, ownerUID: identity.SandboxUID, ownerAPI: identity.APIVersion, role: "supervisor",
			required: secretKeySet(quarshellRuntimePolicyKey, quarshellProxyTokenKey, bridgeTargetServerCAFile, bridgeTargetClientCert,
				bridgeTargetClientKey, bridgeControlServerCAFile, bridgeControlServerCert, bridgeControlServerKey),
			allowed: secretKeySet(quarshellRuntimePolicyKey, quarshellProxyTokenKey, bridgeTargetServerCAFile, bridgeTargetClientCert,
				bridgeTargetClientKey, bridgeControlServerCAFile, bridgeControlServerCert, bridgeControlServerKey,
				quarshellGatewayTLSCAKey, quarshellGatewayTLSCertKey, quarshellGatewayTLSKeyKey),
		},
		proxyEnvironmentName: {
			name: proxyEnvironmentName, ownerName: identity.SandboxName, ownerUID: identity.SandboxUID, ownerAPI: identity.APIVersion, role: "supervisor",
			required: map[string]struct{}{},
		},
	}
	for name, entry := range expected {
		if name == proxyEnvironmentName {
			continue
		}
		if entry.allowed == nil {
			entry.allowed = entry.required
			expected[name] = entry
		}
	}
	seen := make(map[string]struct{}, len(secrets))
	for _, secret := range secrets {
		if secret == nil || secret["apiVersion"] != "v1" || secret["kind"] != "Secret" || secret["immutable"] != true || secret["type"] != "Opaque" {
			return fmt.Errorf("quarshell runtime Secret must be immutable v1 Opaque")
		}
		metadata, _ := secret["metadata"].(map[string]any)
		name, _ := metadata["name"].(string)
		want, ok := expected[name]
		if !ok {
			return fmt.Errorf("quarshell runtime Secret name does not match the active generation")
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("duplicate Quarshell runtime Secret %q", name)
		}
		seen[name] = struct{}{}
		if metadata["namespace"] != identity.Namespace {
			return fmt.Errorf("quarshell runtime Secret namespace does not match the sandbox")
		}
		labels, _ := metadata["labels"].(map[string]any)
		if labels[labelSandboxID] != identity.SandboxID || labels[labelManagedBy] != managedByValue || labels["openshell.ai/component"] != "quarshell-"+want.role+"-runtime-secret" {
			return fmt.Errorf("quarshell runtime Secret labels do not match its owner role")
		}
		data, ok := secret["data"].(map[string]any)
		if !ok {
			return fmt.Errorf("quarshell runtime Secret data is missing")
		}
		for key := range want.required {
			if _, exists := data[key]; !exists {
				return fmt.Errorf("quarshell runtime Secret %q is missing required key %q", name, key)
			}
		}
		if name == supervisorName {
			tlsKeys := 0
			for _, key := range []string{quarshellGatewayTLSCAKey, quarshellGatewayTLSCertKey, quarshellGatewayTLSKeyKey} {
				if _, exists := data[key]; exists {
					tlsKeys++
				}
			}
			if tlsKeys != 0 && tlsKeys != 3 {
				return fmt.Errorf("quarshell supervisor Secret must contain all guest TLS files or none")
			}
		}
		totalBytes := 0
		for key, value := range data {
			if name != proxyEnvironmentName {
				if _, allowed := want.allowed[key]; !allowed {
					return fmt.Errorf("quarshell runtime Secret %q contains an unexpected key", name)
				}
			}
			encoded, ok := value.(string)
			if !ok {
				return fmt.Errorf("quarshell runtime Secret key %q must be base64-encoded data", key)
			}
			decoded, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil || len(decoded) == 0 {
				return fmt.Errorf("quarshell runtime Secret key %q must contain non-empty base64 data", key)
			}
			totalBytes += len(decoded)
			if name == proxyEnvironmentName {
				if !kubernetesEnvKey.MatchString(key) || isQuarshellProxyManagedEnvironmentKey(key) {
					return fmt.Errorf("quarshell proxy environment Secret contains a managed or invalid environment key")
				}
			}
		}
		if totalBytes > maxKubernetesSecretBytes {
			return fmt.Errorf("quarshell runtime Secret exceeds the Kubernetes %d byte limit", maxKubernetesSecretBytes)
		}
		owners, _ := metadata["ownerReferences"].([]any)
		if len(owners) != 1 {
			return fmt.Errorf("quarshell runtime Secret must have exactly one Agent Sandbox owner")
		}
		owner, _ := owners[0].(map[string]any)
		if owner["apiVersion"] != want.ownerAPI || owner["kind"] != "Sandbox" || owner["name"] != want.ownerName || owner["uid"] != want.ownerUID || owner["blockOwnerDeletion"] != false || owner["controller"] == true {
			return fmt.Errorf("quarshell runtime Secret owner must match the Agent Sandbox UID")
		}
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("quarshell workload, supervisor, and proxy environment Secrets must be separate")
	}
	return nil
}

func secretKeySet(keys ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		set[key] = struct{}{}
	}
	return set
}

func isQuarshellProxyManagedEnvironmentKey(key string) bool {
	switch key {
	case "QUARSHELL_SANDBOX_TOKEN", "QUARSHELL_SANDBOX_TOKEN_FILE", "QUARSHELL_GATEWAY_URL", "QUARSHELL_GATEWAY_GRPC_ENDPOINT", "QUARSHELL_SANDBOX", "QUARSHELL_LOG_DIR", "QUARSHELL_POLICY", "QUARSHELL_GUEST_TLS_CA", "QUARSHELL_GUEST_TLS_CERT", "QUARSHELL_GUEST_TLS_KEY",
		"QUARSHELL_SSH_SOCKET",
		"QUARSHELL_SUPERVISOR_CONTROL_SOCKET", "QUARSHELL_SUPERVISOR_CONTROL_ENDPOINT", "QUARSHELL_SUPERVISOR_CONTROL_LISTEN", "QUARSHELL_SUPERVISOR_CONTROL_CLIENT_CA", "QUARSHELL_SUPERVISOR_CONTROL_CLIENT_CERT", "QUARSHELL_SUPERVISOR_CONTROL_CLIENT_KEY", "QUARSHELL_SUPERVISOR_CONTROL_PRIVATE_DIR", "QUARSHELL_SUPERVISOR_CONTROL_SERVER_CA", "QUARSHELL_SUPERVISOR_CONTROL_SERVER_CERT", "QUARSHELL_SUPERVISOR_CONTROL_SERVER_KEY",
		"QUARSHELL_RELAY_TARGET_ENDPOINT", "QUARSHELL_RELAY_TARGET_LISTEN", "QUARSHELL_RELAY_TARGET_CLIENT_CA", "QUARSHELL_RELAY_TARGET_SERVER_CERT", "QUARSHELL_RELAY_TARGET_SERVER_KEY", "QUARSHELL_RELAY_TARGET_SSH_SOCKET", "QUARSHELL_RELAY_TARGET_SERVER_CA", "QUARSHELL_RELAY_TARGET_CLIENT_CERT", "QUARSHELL_RELAY_TARGET_CLIENT_KEY":
		return true
	default:
		return false
	}
}

func validateQuarshellSecretPayload(name string, files map[string][]byte) error {
	size := 0
	for _, value := range files {
		size += len(value)
		if size > maxKubernetesSecretBytes {
			return fmt.Errorf("quarshell %s Secret exceeds the Kubernetes %d byte limit", name, maxKubernetesSecretBytes)
		}
	}
	return nil
}

func validateQuarshellSecretIdentity(identity agentSandboxCreateIdentity) error {
	for field, value := range map[string]string{
		"namespace": identity.Namespace, "sandbox ID": identity.SandboxID,
		"runtime generation": identity.Generation, "sandbox name": identity.SandboxName,
		"sandbox UID": identity.SandboxUID, "Agent Sandbox API version": identity.APIVersion,
		"workload Pod name": identity.WorkloadName, "supervisor Pod name": identity.SupervisorName,
	} {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return fmt.Errorf("quarshell runtime Secret %s is required and must not contain surrounding whitespace", field)
		}
	}
	if err := requireDNS1123Label("Quarshell runtime Secret namespace", identity.Namespace); err != nil {
		return err
	}
	if len(identity.SandboxID) > 63 || len(validation.IsValidLabelValue(identity.SandboxID)) != 0 {
		return fmt.Errorf("quarshell runtime Secret sandbox ID must fit a Kubernetes label value")
	}
	if generationSuffix(identity.Generation) == "" {
		return fmt.Errorf("quarshell runtime Secret generation must contain an ASCII letter or digit")
	}
	if identity.APIVersion != sandboxAPIGroup+"/"+sandboxAPIVersionV1Beta1 && identity.APIVersion != sandboxAPIGroup+"/"+sandboxAPIVersionV1Alpha1 {
		return fmt.Errorf("quarshell runtime Secret Agent Sandbox apiVersion is unsupported")
	}
	for field, value := range map[string]string{
		"Quarshell runtime Secret sandbox name":        identity.SandboxName,
		"Quarshell runtime Secret workload Pod name":   identity.WorkloadName,
		"Quarshell runtime Secret supervisor Pod name": identity.SupervisorName,
	} {
		if err := requireDNS1123Subdomain(field, value); err != nil {
			return err
		}
	}
	return nil
}

func splitQuarshellProxyEnvironment(entries []string) (string, map[string][]byte, error) {
	var token string
	credentials := make(map[string][]byte, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !kubernetesEnvKey.MatchString(key) || strings.ContainsRune(value, '\x00') {
			return "", nil, fmt.Errorf("quarshell proxy environment contains an invalid entry")
		}
		if _, duplicate := seen[key]; duplicate {
			return "", nil, fmt.Errorf("quarshell proxy environment contains a duplicate key")
		}
		seen[key] = struct{}{}
		switch key {
		case "QUARSHELL_SANDBOX_TOKEN":
			if strings.TrimSpace(value) == "" {
				return "", nil, fmt.Errorf("quarshell sandbox token is empty")
			}
			token = value
		case "QUARSHELL_SANDBOX_TOKEN_FILE":
			return "", nil, fmt.Errorf("quarshell sandbox token file path is managed by the Kubernetes runtime")
		case "QUARSHELL_GATEWAY_URL", "QUARSHELL_SANDBOX", "QUARSHELL_LOG_DIR":
			// These are non-secret fields rendered directly into the trusted
			// companion Pod environment.
		default:
			if isQuarshellProxyManagedEnvironmentKey(key) {
				return "", nil, fmt.Errorf("quarshell proxy environment cannot override a managed runtime setting")
			}
			credentials[key] = []byte(value)
		}
	}
	if token == "" {
		return "", nil, fmt.Errorf("quarshell sandbox token is required for the proxy companion")
	}
	return token, credentials, nil
}

func quarshellRuntimeSecret(name, sandboxID, namespace, ownerRole, ownerName, ownerUID, ownerAPIVersion string, files map[string][]byte) map[string]any {
	data := make(map[string]any, len(files))
	for key, value := range files {
		data[key] = base64.StdEncoding.EncodeToString(value)
	}
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"type":       "Opaque",
		"immutable":  true,
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
			"labels": map[string]any{
				labelManagedBy:           managedByValue,
				"openshell.ai/component": "quarshell-" + ownerRole + "-runtime-secret",
				labelSandboxID:           sandboxID,
			},
			"ownerReferences": []any{map[string]any{
				"apiVersion":         ownerAPIVersion,
				"kind":               "Sandbox",
				"name":               ownerName,
				"uid":                ownerUID,
				"blockOwnerDeletion": false,
			}},
		},
		"data": data,
	}
}
