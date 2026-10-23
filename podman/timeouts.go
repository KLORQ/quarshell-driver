package podman

import "time"

// Operational defaults owned by this package.
const (
	daemonProbeTimeout = 400 * time.Millisecond
)
