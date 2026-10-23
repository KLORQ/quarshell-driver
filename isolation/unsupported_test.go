//go:build !linux

package isolation

import (
	"context"
	"errors"
	"testing"

	"github.com/KLORQ/quarshell-core/domain"
	spi "github.com/KLORQ/quarshell-core/isolation/port"
)

func TestLinuxIsUnsupportedOffLinux(t *testing.T) {
	backend := NewLinux(LinuxOptions{})
	if _, err := backend.Probe(context.Background(), domain.InstanceRef{}, spi.SecurityRequirements{}); !errors.Is(err, domain.ErrUnsupported) {
		t.Fatalf("Probe error = %v, want unsupported", err)
	}
	if _, err := backend.Prepare(context.Background(), spi.LaunchSpec{}); !errors.Is(err, domain.ErrUnsupported) {
		t.Fatalf("Prepare error = %v, want unsupported", err)
	}
	if _, err := backend.Launch(context.Background(), nil); !errors.Is(err, domain.ErrUnsupported) {
		t.Fatalf("Launch error = %v, want unsupported", err)
	}
	if err := backend.Stop(context.Background(), spi.ProcessRef{}); !errors.Is(err, domain.ErrUnsupported) {
		t.Fatalf("Stop error = %v, want unsupported", err)
	}
	if handled, err := RunChildIfRequested(); handled || err != nil {
		t.Fatalf("RunChildIfRequested = %v, %v", handled, err)
	}
}
