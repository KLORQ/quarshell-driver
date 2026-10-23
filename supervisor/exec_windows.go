//go:build windows

package supervisor

import (
	"context"

	"github.com/KLORQ/quarshell-core/domain"
)

func startExec(context.Context, ExecRequest) (ExecSession, error) {
	return nil, domain.Wrap(domain.ErrUnsupported, "supervisor.exec", "pty sessions are unavailable on windows")
}
