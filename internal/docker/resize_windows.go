//go:build windows

package docker

import (
	"context"
	"time"

	"github.com/moby/moby/client"
)

// resizeExecTTY retries an initial resize; Windows has no SIGWINCH.
func resizeExecTTY(ctx context.Context, cli *client.Client, execID string, inFd int) {
	for i := 0; i < 10; i++ {
		resizeExecOnce(ctx, cli, execID, inFd)
		time.Sleep(ttyResizeInterval)
	}
}
