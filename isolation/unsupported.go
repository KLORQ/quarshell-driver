//go:build !linux

package isolation

import (
	"context"

	"github.com/KLORQ/quarshell-core/domain"
	spi "github.com/KLORQ/quarshell-core/isolation/port"
)

// Linux is unavailable off Linux. The type still exists so callers can compile
// and receive a classified unsupported error instead of a build failure.
type Linux struct{}

var _ spi.Isolation = (*Linux)(nil)

// NewLinux returns an unsupported isolation implementation.
func NewLinux(LinuxOptions) *Linux { return &Linux{} }

// Probe reports that the Linux isolation backend is unavailable.
func (l *Linux) Probe(context.Context, domain.InstanceRef, spi.SecurityRequirements) (spi.SecurityEvidence, error) {
	return spi.SecurityEvidence{}, domain.Wrap(domain.ErrUnsupported, "isolation.probe", "linux isolation is unavailable on this platform")
}

// Prepare reports that the Linux isolation backend is unavailable.
func (l *Linux) Prepare(context.Context, spi.LaunchSpec) (spi.PreparedLaunch, error) {
	return nil, domain.Wrap(domain.ErrUnsupported, "isolation.prepare", "linux isolation is unavailable on this platform")
}

// Launch reports that the Linux isolation backend is unavailable.
func (l *Linux) Launch(context.Context, spi.PreparedLaunch) (spi.ProcessRef, error) {
	return spi.ProcessRef{}, domain.Wrap(domain.ErrUnsupported, "isolation.launch", "linux isolation is unavailable on this platform")
}

// Stop reports that the Linux isolation backend is unavailable.
func (l *Linux) Stop(context.Context, spi.ProcessRef) error {
	return domain.Wrap(domain.ErrUnsupported, "isolation.stop", "linux isolation is unavailable on this platform")
}

// RunChildIfRequested is a no-op off Linux.
func RunChildIfRequested() (bool, error) { return false, nil }
