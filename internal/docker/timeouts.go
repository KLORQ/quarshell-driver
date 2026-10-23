package docker

import "time"

// Operational defaults owned by this package.
const (
	readinessTimeout      = 5 * time.Second
	readinessPollInterval = 50 * time.Millisecond
	caPollInterval        = 100 * time.Millisecond
	ttyResizeInterval     = 20 * time.Millisecond
)
