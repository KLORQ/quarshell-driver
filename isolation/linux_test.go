//go:build linux

package isolation

import "testing"

func TestEndpointPorts(t *testing.T) {
	ports := endpointPorts([]string{
		"api.example.com:443",
		"[2001:db8::1]:8443",
		"api.example.com:443",
		"no-port",
		"bad:port",
		"service:0",
	})
	if len(ports) != 3 {
		t.Fatalf("ports = %v", ports)
	}
	if ports[0] != 443 || ports[1] != 8443 || ports[2] != 0 {
		t.Fatalf("ports = %v", ports)
	}
}

func TestUniquePaths(t *testing.T) {
	values := uniquePaths([]string{"/usr", "", "/usr", "/bin", "/bin"})
	if len(values) != 2 || values[0] != "/usr" || values[1] != "/bin" {
		t.Fatalf("uniquePaths = %v", values)
	}
}

func TestLandlockProbeIsClassified(t *testing.T) {
	abi, err := landlockABI()
	if err == nil && abi < 1 {
		t.Fatalf("landlock ABI = %d", abi)
	}
}
