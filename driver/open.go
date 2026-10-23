package driver

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

var (
	mu                      sync.RWMutex
	backendConfigOpeners    = map[string]func(map[string]any) (Backend, error){}
	backendConfigValidators = map[string]func(map[string]any) error{}
	backendHostPreflighters = map[string]BackendHostToolPreflight{}
	backendInfos            = map[string]BackendInfo{}
)

// BackendConfigValidator validates backend-owned deployment values without
// constructing a Backend or probing external state.
type BackendConfigValidator func(map[string]any) error

// BackendHostToolPreflight checks required local executables without starting
// the backend or creating runtime state. Gateway calls it only for explicit
// config preflight commands.
type BackendHostToolPreflight func(context.Context, map[string]any) ([]string, error)

// RegisterBackendHostToolPreflight attaches a host-tool probe to a registered
// backend. It must not connect to services or mutate host/runtime state.
func RegisterBackendHostToolPreflight(kind string, preflight BackendHostToolPreflight) error {
	name := normalizeKind(kind)
	if preflight == nil {
		return fmt.Errorf("driver: backend host-tool preflight is required")
	}
	mu.Lock()
	defer mu.Unlock()
	if backendConfigOpeners[name] == nil {
		return fmt.Errorf("driver: backend %q must be registered before its host-tool preflight", name)
	}
	if backendHostPreflighters[name] != nil {
		return fmt.Errorf("driver: backend %q already has a host-tool preflight", name)
	}
	backendHostPreflighters[name] = preflight
	return nil
}

// PreflightRegisteredBackendHostTools runs a registered local host probe.
// The boolean reports whether the backend provides a probe.
func PreflightRegisteredBackendHostTools(ctx context.Context, kind string, config map[string]any) (bool, []string, error) {
	name := normalizeKind(kind)
	mu.RLock()
	preflight := backendHostPreflighters[name]
	mu.RUnlock()
	if preflight == nil {
		return false, nil, nil
	}
	reports, err := preflight(ctx, cloneBackendConfig(config))
	return true, reports, err
}

// RegisterBackendWithConfig binds a native Backend factory. The returned
// backend ID must match the normalized kind supplied at registration.
func RegisterBackendWithConfig(kind string, open func(map[string]any) (Backend, error)) error {
	return RegisterBackendWithConfigAndInfo(kind, BackendInfo{}, open)
}

// RegisterBackendWithConfigAndInfo binds a native Backend factory and its
// immutable, configuration-independent public capability snapshot. The
// metadata is available without constructing the Backend or initializing its
// runtime, so control-plane discovery preserves lazy startup.
func RegisterBackendWithConfigAndInfo(kind string, info BackendInfo, open func(map[string]any) (Backend, error)) error {
	return registerBackend(kind, info, open, nil)
}

// RegisterBackendWithConfigAndInfoAndValidator binds a backend with a
// side-effect-free validator for its deployment configuration. Validators
// must not probe runtimes, connect to services, or mutate host state.
func RegisterBackendWithConfigAndInfoAndValidator(kind string, info BackendInfo, open func(map[string]any) (Backend, error), validate BackendConfigValidator) error {
	if validate == nil {
		return fmt.Errorf("driver: backend config validator is required")
	}
	return registerBackend(kind, info, open, validate)
}

func registerBackend(kind string, info BackendInfo, open func(map[string]any) (Backend, error), validate BackendConfigValidator) error {
	name := strings.TrimSpace(kind)
	if name == "" || open == nil {
		return fmt.Errorf("driver: backend kind and factory are required")
	}
	name = normalizeKind(name)
	if info.ID != "" && info.ID != BackendID(name) {
		return fmt.Errorf("driver: backend metadata id %q does not match kind %q", info.ID, name)
	}
	mu.Lock()
	defer mu.Unlock()
	if _, exists := backendConfigOpeners[name]; exists {
		return fmt.Errorf("driver: backend kind %q is already registered", name)
	}
	backendConfigOpeners[name] = open
	if validate != nil {
		backendConfigValidators[name] = validate
	}
	if info.ID != "" {
		backendInfos[name] = info.Clone()
	}
	return nil
}

// ValidateRegisteredBackendConfig validates config without constructing the
// backend. The boolean reports whether a side-effect-free validator exists.
func ValidateRegisteredBackendConfig(kind string, config map[string]any) (bool, error) {
	name := normalizeKind(kind)
	mu.RLock()
	validate := backendConfigValidators[name]
	mu.RUnlock()
	if validate == nil {
		return false, nil
	}
	return true, validate(cloneBackendConfig(config))
}

// RegisteredBackendInfo returns the immutable metadata registered for kind.
// It never constructs a Backend or initializes a runtime.
func RegisteredBackendInfo(kind string) (BackendInfo, bool) {
	mu.RLock()
	info, ok := backendInfos[normalizeKind(kind)]
	mu.RUnlock()
	if !ok {
		return BackendInfo{}, false
	}
	return info.Clone(), true
}

// OpenRegisteredBackendWithConfig opens a registered Backend factory.
// registered is true even when factory construction fails, so the caller can
// distinguish a broken backend from an absent registration.
func OpenRegisteredBackendWithConfig(kind string, config map[string]any) (backend Backend, registered bool, err error) {
	name := normalizeKind(kind)
	mu.RLock()
	factory := backendConfigOpeners[name]
	mu.RUnlock()
	if factory == nil {
		return nil, false, nil
	}
	backend, err = factory(cloneBackendConfig(config))
	if err != nil {
		return nil, true, fmt.Errorf("driver: open backend %q: %w", name, err)
	}
	if backend == nil {
		return nil, true, fmt.Errorf("driver: backend factory %q returned nil", name)
	}
	if got := backend.ID(); got != BackendID(name) {
		return nil, true, fmt.Errorf("driver: backend factory %q returned mismatched id %q", name, got)
	}
	return backend, true, nil
}

// HasRegisteredBackend reports whether kind has a native Backend factory.
func HasRegisteredBackend(kind string) bool {
	mu.RLock()
	defer mu.RUnlock()
	return backendConfigOpeners[normalizeKind(kind)] != nil
}

func cloneBackendConfig(config map[string]any) map[string]any {
	if config == nil {
		return nil
	}
	cloned := make(map[string]any, len(config))
	for key, value := range config {
		cloned[key] = cloneBackendConfigValue(value)
	}
	return cloned
}

func cloneBackendConfigValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneBackendConfig(typed)
	case []any:
		cloned := make([]any, len(typed))
		for i, item := range typed {
			cloned[i] = cloneBackendConfigValue(item)
		}
		return cloned
	case []string:
		return append([]string(nil), typed...)
	case []byte:
		return append([]byte(nil), typed...)
	default:
		return value
	}
}

func normalizeKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "podman":
		return "podman"
	case "vm", "microvm":
		return "vm"
	case "kubernetes", "k8s":
		return "kubernetes"
	case "", "docker":
		return "docker"
	default:
		return strings.ToLower(strings.TrimSpace(kind))
	}
}

// CanonicalBackendID maps supported aliases to their product backend identity.
func CanonicalBackendID(kind string) BackendID { return BackendID(normalizeKind(kind)) }
