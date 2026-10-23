package supervisor

import "context"

// ExecRequest starts one interactive or non-interactive exec session.
type ExecRequest struct {
	Command     []string
	Environment map[string]string
	Workdir     string
	Rows        uint16
	Cols        uint16
}

// ExecSession is a PTY-backed process session. Write/Read carry terminal bytes.
type ExecSession interface {
	PID() int
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	Resize(rows, cols uint16) error
	Signal(signal string) error
	Wait() (ExitStatus, error)
	Close() error
}

// StartExec starts a PTY session. Platforms without PTY support return a
// classified unsupported error.
func StartExec(ctx context.Context, request ExecRequest) (ExecSession, error) {
	return startExec(ctx, request)
}
