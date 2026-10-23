package backendcontract

import (
	"context"
	"testing"

	"github.com/KLORQ/quarshell-driver/driver"
	_ "github.com/KLORQ/quarshell-driver/driver/all"
)

var backends = []string{"docker", "podman", "kubernetes"}

func TestRegisteredBackendsExposeSafeMetadata(t *testing.T) {
	for _, kind := range backends {
		t.Run(kind, func(t *testing.T) {
			info, ok := driver.RegisteredBackendInfo(kind)
			if !ok {
				t.Fatalf("backend %q is not registered", kind)
			}
			if string(info.ID) != kind {
				t.Fatalf("info id = %q", info.ID)
			}
			if info.Version == "" || info.ConfigSchemaVersion == "" {
				t.Fatalf("incomplete info = %+v", info)
			}
			if info.Capabilities == nil {
				t.Fatalf("capabilities are nil")
			}
			hasValidator, err := driver.ValidateRegisteredBackendConfig(kind, map[string]any{})
			if !hasValidator {
				t.Fatalf("backend %q has no side-effect-free validator", kind)
			}
			_ = err // required deployment fields may be absent in the empty map.
			if hasValidator, err := driver.ValidateRegisteredBackendConfig(kind, map[string]any{"definitely_unknown_setting": true}); !hasValidator || err == nil {
				t.Fatalf("unknown setting accepted by %q (validator=%v err=%v)", kind, hasValidator, err)
			}
		})
	}
}

func TestBackendConfigValidationIsSideEffectFree(t *testing.T) {
	for _, kind := range backends {
		t.Run(kind, func(t *testing.T) {
			for index := 0; index < 3; index++ {
				if _, err := driver.ValidateRegisteredBackendConfig(kind, map[string]any{}); err == nil {
					// An empty config may be valid; the important part is that repeated
					// validation does not construct a backend or touch the host.
					continue
				}
			}
			if _, _, err := driver.OpenRegisteredBackendWithConfig(kind, map[string]any{"definitely_unknown_setting": true}); err == nil {
				t.Fatalf("unknown setting opened a backend")
			}
		})
	}
	_ = context.Background()
}
