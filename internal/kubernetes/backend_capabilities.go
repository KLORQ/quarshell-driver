package kubernetes

import "github.com/KLORQ/quarshell-driver/driver"

func (*Driver) backendInfo() driver.BackendInfo {
	return driver.BackendInfo{
		ID:                  "kubernetes",
		Version:             driver.BackendVersion,
		ConfigSchemaVersion: driver.ConfigSchemaVersionV1,
		Resources: &driver.ResourceCapabilities{
			CPU:    &driver.CPUResourceCapabilities{LimitSupported: true},
			Memory: &driver.MemoryResourceCapabilities{LimitSupported: true},
			GPU:    &driver.GPUResourceCapabilities{DefaultSelectionSupported: true, CountSelectionSupported: true},
		},
		Capabilities: map[driver.Capability]struct{}{
			driver.CapabilityTokenAwareStart:   {},
			driver.CapabilityPolicyUpdate:      {},
			driver.CapabilityCredentialRefresh: {},
			driver.CapabilityProxyReadiness:    {},
			driver.CapabilityPlatformEvents:    {},
		},
	}
}
