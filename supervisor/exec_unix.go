//go:build unix

package supervisor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/KLORQ/quarshell-core/domain"
	"github.com/creack/pty"
)

type ptySession struct {
	cmd  *exec.Cmd
	file *os.File

	mu     sync.Mutex
	waited bool
	status ExitStatus
}

func startExec(ctx context.Context, request ExecRequest) (ExecSession, error) {
	if err := validateExecRequest(request); err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, request.Command[0], request.Command[1:]...)
	if request.Workdir != "" {
		command.Dir = request.Workdir
	}
	if request.Environment != nil {
		keys := make([]string, 0, len(request.Environment))
		for key := range request.Environment {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		environment := make([]string, 0, len(keys))
		for _, key := range keys {
			environment = append(environment, key+"="+request.Environment[key])
		}
		command.Env = environment
	}
	file, err := pty.Start(command)
	if err != nil {
		return nil, domain.Wrap(err, "supervisor.exec", "start pty session")
	}
	session := &ptySession{cmd: command, file: file}
	if request.Rows > 0 || request.Cols > 0 {
		_ = session.Resize(request.Rows, request.Cols)
	}
	return session, nil
}

func validateExecRequest(request ExecRequest) error {
	if len(request.Command) == 0 {
		return domain.Invalidf("supervisor.exec", "command is required")
	}
	for _, argument := range request.Command {
		if strings.ContainsRune(argument, 0) {
			return domain.Invalidf("supervisor.exec", "command contains NUL")
		}
	}
	if request.Workdir != "" {
		if !filepath.IsAbs(request.Workdir) || filepath.Clean(request.Workdir) != request.Workdir {
			return domain.Invalidf("supervisor.exec", "workdir must be absolute and clean")
		}
	}
	for key, value := range request.Environment {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			return domain.Invalidf("supervisor.exec", "environment entry is invalid")
		}
	}
	return nil
}

func (s *ptySession) PID() int { return s.cmd.Process.Pid }

func (s *ptySession) Read(buffer []byte) (int, error)  { return s.file.Read(buffer) }
func (s *ptySession) Write(buffer []byte) (int, error) { return s.file.Write(buffer) }

func (s *ptySession) Resize(rows, cols uint16) error {
	if rows == 0 || cols == 0 {
		return nil
	}
	return pty.Setsize(s.file, &pty.Winsize{Rows: rows, Cols: cols})
}

func (s *ptySession) Signal(name string) error {
	signal, err := signalByName(name)
	if err != nil {
		return err
	}
	if err := syscall.Kill(-s.cmd.Process.Pid, signal); err != nil {
		return domain.Wrap(err, "supervisor.exec", "signal process group")
	}
	return nil
}

func (s *ptySession) Wait() (ExitStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.waited {
		return s.status, nil
	}
	err := s.cmd.Wait()
	s.waited = true
	s.status = exitStatus(err)
	if err != nil && !isExitError(err) {
		return s.status, domain.Wrap(err, "supervisor.exec", "wait process")
	}
	return s.status, nil
}

func (s *ptySession) Close() error {
	_ = s.file.Close()
	s.mu.Lock()
	waited := s.waited
	s.mu.Unlock()
	if !waited {
		_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
		_, _ = s.Wait()
	}
	return nil
}

func exitStatus(err error) ExitStatus {
	status := ExitStatus{Exited: true, At: time.Now()}
	if err == nil {
		return status
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		if wait, ok := exitError.Sys().(syscall.WaitStatus); ok {
			if wait.Signaled() {
				status.Exited = false
				status.Signal = int(wait.Signal())
				status.Code = 128 + int(wait.Signal())
				return status
			}
			status.Code = wait.ExitStatus()
			return status
		}
		status.Code = exitError.ExitCode()
	}
	return status
}

func isExitError(err error) bool {
	var exitError *exec.ExitError
	return errors.As(err, &exitError)
}

func signalByName(name string) (syscall.Signal, error) {
	switch strings.ToUpper(strings.TrimPrefix(name, "SIG")) {
	case "TERM":
		return syscall.SIGTERM, nil
	case "KILL":
		return syscall.SIGKILL, nil
	case "INT":
		return syscall.SIGINT, nil
	case "HUP":
		return syscall.SIGHUP, nil
	case "QUIT":
		return syscall.SIGQUIT, nil
	case "USR1":
		return syscall.SIGUSR1, nil
	case "USR2":
		return syscall.SIGUSR2, nil
	case "STOP":
		return syscall.SIGSTOP, nil
	case "CONT":
		return syscall.SIGCONT, nil
	default:
		return 0, domain.Invalidf("supervisor.exec", "unsupported signal %q", name)
	}
}
