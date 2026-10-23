package docker

import (
	"context"
	"io"

	core "github.com/KLORQ/quarshell-core"
	"github.com/KLORQ/quarshell-driver/driver"
)

var _ driver.RuntimeProcessWaiter = (*Driver)(nil)
var _ driver.RuntimeProcessLogs = (*Driver)(nil)
var _ driver.RuntimeProcessTerminal = (*Driver)(nil)

func (d *Driver) WaitProcess(ctx context.Context, id core.ID) (int, error) {
	return d.WaitMainProcess(ctx, id)
}

func (d *Driver) OpenProcessLogs(ctx context.Context, id core.ID) (io.ReadCloser, error) {
	return d.OpenMainProcessLogs(ctx, id)
}

func (d *Driver) OpenProcessTerminal(ctx context.Context, id core.ID, columns, rows uint32) (io.ReadWriteCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return d.OpenMainProcessTTY(ctx, id, columns, rows)
}

func (d *Driver) ResizeProcessTerminal(ctx context.Context, id core.ID, columns, rows uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return d.ResizeMainProcessTTY(ctx, id, columns, rows)
}
