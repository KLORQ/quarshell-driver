// Package all blank-imports the container and Kubernetes backends so they
// register with the driver factory registry. Platform backends in separate Go
// modules are imported by their composition root.
//
//	import _ "github.com/KLORQ/quarshell-driver/driver/all"
package all

import (
	_ "github.com/KLORQ/quarshell-driver/internal/docker"
	_ "github.com/KLORQ/quarshell-driver/internal/kubernetes"
	_ "github.com/KLORQ/quarshell-driver/podman"
)
