// SPDX-FileCopyrightText: Copyright (c) 2026 quarshell
// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"os"
	"strconv"
	"strings"

	"github.com/KLORQ/quarshell-core/defaults"
)

const (
	envProxyImage       = "QUARSHELL_PROXY_IMAGE"
	envSandboxPidsLimit = "QUARSHELL_SANDBOX_PIDS_LIMIT"
)

// resolveProxyImage returns the OCI image for the egress sidecar.
// Prefer sandboxCreateSpec.ProxyImage, then QUARSHELL_PROXY_IMAGE, then defaults.ImageProxy.
// Never reuse the fat agent/sandbox image unless the operator points ProxyImage at it.
func resolveProxyImage(explicit string) string {
	if s := strings.TrimSpace(explicit); s != "" {
		return s
	}
	if s := strings.TrimSpace(os.Getenv(envProxyImage)); s != "" {
		return s
	}
	return defaults.ImageProxy
}

// resolvePidsLimit returns the Docker PidsLimit pointer.
// PIDs limit 0 → env QUARSHELL_SANDBOX_PIDS_LIMIT or OpenShell-aligned 2048.
// PIDs limit -1 or env 0 → unlimited (nil).
// PIDs limit > 0 → that value.
func resolvePidsLimit(specLimit int64) *int64 {
	if specLimit < 0 {
		return nil
	}
	if specLimit > 0 {
		v := specLimit
		return &v
	}
	raw := strings.TrimSpace(os.Getenv(envSandboxPidsLimit))
	if raw == "" {
		v := defaults.SandboxPidsLimit
		return &v
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		v := defaults.SandboxPidsLimit
		return &v
	}
	if n == 0 {
		return nil
	}
	return &n
}
