package kubernetes

import (
	"context"
	"testing"

	"github.com/KLORQ/quarshell-driver/driver"
)

func TestBackendCapabilitiesAdvertiseRuntimeExtensions(t *testing.T) {
	info, err := (&Driver{}).Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != "kubernetes" || !info.Has(driver.CapabilityTokenAwareStart) || !info.Has(driver.CapabilityPolicyUpdate) || !info.Has(driver.CapabilityCredentialRefresh) || !info.Has(driver.CapabilityProxyReadiness) || !info.Has(driver.CapabilityPlatformEvents) {
		t.Fatalf("Kubernetes backend info=%+v; want token, policy, credential, proxy readiness, and platform event capabilities", info)
	}
}
