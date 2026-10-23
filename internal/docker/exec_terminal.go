package docker

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	core "github.com/KLORQ/quarshell-core"
	"github.com/KLORQ/quarshell-driver/driver"
	"github.com/KLORQ/quarshell-driver/internal/mounts"
	"github.com/moby/moby/client"
)

func (d *Driver) OpenExecTerminal(ctx context.Context, id core.ID, request driver.ExecTerminalRequest) (driver.ExecTerminalSession, error) {
	if ctx == nil {
		return nil, fmt.Errorf("docker terminal exec: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(request.Argv) == 0 || strings.TrimSpace(request.Argv[0]) == "" {
		return nil, fmt.Errorf("docker terminal exec: command is required")
	}
	if request.Columns > 65535 || request.Rows > 65535 {
		return nil, fmt.Errorf("docker terminal exec: dimensions must not exceed 65535")
	}
	columns, rows := request.Columns, request.Rows
	if columns == 0 {
		columns = 80
	}
	if rows == 0 {
		rows = 24
	}
	info, err := d.cli.ContainerInspect(ctx, string(id), client.ContainerInspectOptions{})
	if err != nil {
		return nil, fmt.Errorf("docker terminal exec inspect container: %w", err)
	}
	if info.Container.Config == nil {
		return nil, fmt.Errorf("docker terminal exec: container config is unavailable")
	}
	argv := append([]string(nil), request.Argv...)
	workdir := strings.TrimSpace(request.WorkDir)
	if workdir == "" {
		workdir = mounts.WorkdirInContainer
	}
	if info.Container.Config.Labels[labelInit] == "1" {
		argv = append([]string{"/quarshell/quarshell-init", "--"}, argv...)
	}
	if info.Container.Config.Labels[labelRole] == roleProxy {
		workdir = "/"
	}
	env := ensureTTYEnv(mergeEnv(info.Container.Config.Env, request.Env))
	consoleSize := client.ConsoleSize{Height: uint(rows), Width: uint(columns)}
	execID, err := d.cli.ExecCreate(ctx, string(id), client.ExecCreateOptions{
		Cmd: argv, Env: env, AttachStdin: true, AttachStdout: true, AttachStderr: true,
		TTY: true, WorkingDir: workdir, ConsoleSize: consoleSize,
	})
	if err != nil {
		return nil, fmt.Errorf("docker terminal exec create: %w", err)
	}
	attached, err := d.cli.ExecAttach(ctx, execID.ID, client.ExecAttachOptions{TTY: true, ConsoleSize: consoleSize})
	if err != nil {
		return nil, fmt.Errorf("docker terminal exec attach: %w", err)
	}
	session := &dockerExecTerminal{cli: d.cli, execID: execID.ID, attached: attached, done: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			_ = session.Close()
		case <-session.done:
		}
	}()
	return session, nil
}

type dockerExecTerminal struct {
	cli      *client.Client
	execID   string
	attached client.ExecAttachResult
	done     chan struct{}
	once     sync.Once
}

func (s *dockerExecTerminal) Read(p []byte) (int, error)  { return s.attached.Reader.Read(p) }
func (s *dockerExecTerminal) Write(p []byte) (int, error) { return s.attached.Conn.Write(p) }

func (s *dockerExecTerminal) Resize(ctx context.Context, columns, rows uint32) error {
	if ctx == nil {
		return fmt.Errorf("docker terminal resize context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if columns == 0 || rows == 0 || columns > 65535 || rows > 65535 {
		return fmt.Errorf("docker terminal dimensions must be between 1 and 65535")
	}
	_, err := s.cli.ExecResize(ctx, s.execID, client.ExecResizeOptions{Width: uint(columns), Height: uint(rows)})
	if err != nil {
		return fmt.Errorf("docker terminal resize: %w", err)
	}
	return nil
}

func (s *dockerExecTerminal) CloseStdin() error {
	_, err := s.attached.Conn.Write([]byte{4})
	return err
}

func (s *dockerExecTerminal) Wait(ctx context.Context) (int, error) {
	if ctx == nil {
		return 0, fmt.Errorf("docker terminal wait context is nil")
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		result, err := s.cli.ExecInspect(ctx, s.execID, client.ExecInspectOptions{})
		if err != nil {
			return 0, fmt.Errorf("docker terminal exec inspect: %w", err)
		}
		if !result.Running {
			return result.ExitCode, nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *dockerExecTerminal) Close() error {
	s.once.Do(func() {
		close(s.done)
		s.attached.Close()
	})
	return nil
}

var _ driver.ExecTerminal = (*Driver)(nil)
var _ driver.ExecTerminalSession = (*dockerExecTerminal)(nil)
var _ driver.ExecTerminalInputCloser = (*dockerExecTerminal)(nil)
var _ io.ReadWriteCloser = (*dockerExecTerminal)(nil)
