package docker

import (
	"context"
	"testing"

	"github.com/KLORQ/quarshell-driver/driver"
)

func TestBackendCapabilitiesReportConfiguredBackendAndExtensions(t *testing.T) {
	backend := NewFromClientWithRuntimeConfig(nil, RuntimeConfig{BackendID: "podman"})
	info, err := backend.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != driver.BackendID("podman") {
		t.Fatalf("backend ID = %q; want podman", info.ID)
	}
	for _, capability := range []driver.Capability{
		driver.CapabilityInteractiveExec,
		driver.CapabilityExecTerminal,
		driver.CapabilityPTY,
		driver.CapabilityLogAccess,
		driver.CapabilityFileTransfer,
		driver.CapabilityProcessWait,
		driver.CapabilityProcessLogs,
		driver.CapabilityProcessTerminal,
		driver.CapabilityPlatformEvents,
	} {
		if !info.Has(capability) {
			t.Errorf("backend capabilities do not include %q", capability)
		}
	}
}
