package docker

import "github.com/KLORQ/quarshell-driver/driver"

func (d *Driver) backendInfo() driver.BackendInfo {
	backendID := "docker"
	if d != nil && d.runtimeConfig.BackendID != "" {
		backendID = d.runtimeConfig.BackendID
	}
	resources := &driver.ResourceCapabilities{
		CPU:    &driver.CPUResourceCapabilities{LimitSupported: true},
		Memory: &driver.MemoryResourceCapabilities{LimitSupported: true},
	}
	if backendID == "podman" {
		resources.GPU = &driver.GPUResourceCapabilities{DefaultSelectionSupported: true, CountSelectionSupported: true}
	}
	return driver.BackendInfo{
		ID:                  driver.BackendID(backendID),
		Version:             driver.BackendVersion,
		ConfigSchemaVersion: driver.ConfigSchemaVersionV1,
		Resources:           resources,
		Capabilities: map[driver.Capability]struct{}{
			driver.CapabilityInteractiveExec: {},
			driver.CapabilityExecTerminal:    {},
			driver.CapabilityPTY:             {},
			driver.CapabilityLogAccess:       {},
			driver.CapabilityFileTransfer:    {},
			driver.CapabilityProcessWait:     {},
			driver.CapabilityProcessLogs:     {},
			driver.CapabilityProcessTerminal: {},
			driver.CapabilityPlatformEvents:  {},
		},
	}
}

// StaticBackendInfo returns configuration-independent public metadata for a
// registered Docker-compatible backend without initializing its runtime.
func StaticBackendInfo(id string) driver.BackendInfo {
	return (&Driver{runtimeConfig: RuntimeConfig{BackendID: id}}).backendInfo()
}
