// Package supervisor owns sessions, configuration admission, and process
// orchestration. It depends on the isolation SPI, never on a concrete backend.
package supervisor

import (
	"context"
	"sync"
	"time"

	"github.com/KLORQ/quarshell-core/domain"
	spi "github.com/KLORQ/quarshell-core/isolation/port"
	"github.com/KLORQ/quarshell-driver/isolation"
)

// State is the durable session state.
type State string

const (
	StateRequested State = "requested"
	StateAdmitted  State = "admitted"
	StateRunning   State = "running"
	StateStopping  State = "stopping"
	StateStopped   State = "stopped"
	StateFailed    State = "failed"
)

// ExitStatus is the final process result.
type ExitStatus struct {
	Code   int
	Signal int
	Exited bool
	At     time.Time
}

// RestartMode selects when a session may be restarted.
type RestartMode string

const (
	RestartNever     RestartMode = "never"
	RestartOnFailure RestartMode = "on_failure"
	RestartAlways    RestartMode = "always"
)

// RestartPolicy bounds automatic restarts. MaxAttempts counts restarts already
// performed; zero allows DefaultRestartAttempts.
type RestartPolicy struct {
	Mode        RestartMode
	MaxAttempts int
}

const DefaultRestartAttempts = 3

// Allows reports whether another restart is permitted.
func (p RestartPolicy) Allows(exit *ExitStatus, attempts int) bool {
	maxAttempts := p.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = DefaultRestartAttempts
	}
	if attempts >= maxAttempts {
		return false
	}
	switch p.Mode {
	case RestartAlways:
		return true
	case RestartOnFailure:
		return exit == nil || !exit.Exited || exit.Code != 0 || exit.Signal != 0
	default:
		return false
	}
}

// Event is one durable session report.
type Event struct {
	Instance   domain.InstanceRef
	State      State
	Generation domain.Generation
	At         time.Time
	Reason     string
	Exit       *ExitStatus
}

// Reporter persists session events. A bounded implementation is provided by
// BoundedReporter; callers own retry and durability.
type Reporter interface {
	Report(context.Context, Event) error
}

// Admission validates a launch request before it reaches the isolation layer.
type Admission struct {
	// Deadline bounds the admission work itself.
	Deadline time.Duration
	// Now is injectable for tests.
	Now func() time.Time
}

// Admit returns a sanitized launch spec or a classified error.
func (a Admission) Admit(ctx context.Context, spec spi.LaunchSpec) (spi.LaunchSpec, error) {
	if err := ctx.Err(); err != nil {
		return spec, domain.Wrap(err, "supervisor.admit", "admission canceled")
	}
	started := a.now()
	sanitized, err := isolation.ValidateLaunchSpec(spec)
	if err != nil {
		return spec, err
	}
	if a.Deadline > 0 && a.now().Sub(started) > a.Deadline {
		return spec, domain.New(domain.CodeDeadlineExceeded, "supervisor.admit", "admission deadline exceeded")
	}
	return sanitized, nil
}

func (a Admission) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// Session is one sandbox instance lifecycle. It is safe for one caller at a
// time; concurrent transitions return a conflict rather than racing.
type Session struct {
	mu sync.Mutex

	instance  domain.InstanceRef
	isolation spi.Isolation
	reporter  Reporter
	now       func() time.Time

	state     State
	prepared  spi.PreparedLaunch
	process   spi.ProcessRef
	exit      *ExitStatus
	spec      spi.LaunchSpec
	attempts  int
	restart   RestartPolicy
	lastError string
}

// NewSession creates a requested session.
func NewSession(instance domain.InstanceRef, backend spi.Isolation, reporter Reporter) (*Session, error) {
	if !instance.Valid() {
		return nil, domain.Invalidf("supervisor.session", "instance reference is invalid")
	}
	if backend == nil {
		return nil, domain.Invalidf("supervisor.session", "isolation backend is required")
	}
	if reporter == nil {
		return nil, domain.Invalidf("supervisor.session", "reporter is required")
	}
	return &Session{
		instance:  instance,
		isolation: backend,
		reporter:  reporter,
		now:       time.Now,
		state:     StateRequested,
	}, nil
}

// State returns the current state.
func (s *Session) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Process returns the last launched process reference.
func (s *Session) Process() spi.ProcessRef {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.process
}

// Admit validates the spec and prepares a launch handle.
func (s *Session) Admit(ctx context.Context, spec spi.LaunchSpec) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateRequested {
		return s.conflict("admit", s.state)
	}
	sanitized, err := (Admission{}).Admit(ctx, spec)
	if err != nil {
		s.lastError = domain.SafeMessage(err)
		return err
	}
	prepared, err := s.isolation.Prepare(ctx, sanitized)
	if err != nil {
		s.lastError = domain.SafeMessage(err)
		return err
	}
	s.prepared = prepared
	s.spec = sanitized
	s.state = StateAdmitted
	return s.report(ctx, "")
}

// Start launches the prepared process and moves to running.
func (s *Session) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateAdmitted || s.prepared == nil {
		return s.conflict("start", s.state)
	}
	process, err := s.isolation.Launch(ctx, s.prepared)
	if err != nil {
		s.state = StateFailed
		s.lastError = domain.SafeMessage(err)
		_ = s.report(ctx, s.lastError)
		return err
	}
	s.process = process
	s.state = StateRunning
	return s.report(ctx, "")
}

// Exit records the final process status. It is idempotent only in the sense
// that a second call conflicts; the first durable report wins.
func (s *Session) Exit(ctx context.Context, status ExitStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateRunning {
		return s.conflict("exit", s.state)
	}
	if status.At.IsZero() {
		status.At = s.now()
	}
	s.exit = &status
	s.state = StateStopped
	return s.report(ctx, "process exited")
}

// Fail records a terminal failure.
func (s *Session) Fail(ctx context.Context, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == StateStopped || s.state == StateFailed {
		return s.conflict("fail", s.state)
	}
	s.lastError = reason
	s.state = StateFailed
	return s.report(ctx, reason)
}

// Stop terminates a running process and reports the stop.
func (s *Session) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateRunning {
		return s.conflict("stop", s.state)
	}
	s.state = StateStopping
	_ = s.report(ctx, "stopping")
	if err := s.isolation.Stop(ctx, s.process); err != nil {
		s.state = StateFailed
		s.lastError = domain.SafeMessage(err)
		_ = s.report(ctx, s.lastError)
		return err
	}
	s.state = StateStopped
	return s.report(ctx, "stopped")
}

// SetRestartPolicy configures automatic restarts for this session.
func (s *Session) SetRestartPolicy(policy RestartPolicy) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.restart = policy
}

// Restart re-prepares the last admitted spec after a terminal state. The
// caller starts the new attempt with Start; attempts are counted per restart.
func (s *Session) Restart(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateStopped && s.state != StateFailed {
		return s.conflict("restart", s.state)
	}
	if len(s.spec.Command) == 0 {
		return s.conflict("restart", s.state)
	}
	if !s.restart.Allows(s.exit, s.attempts) {
		return domain.New(domain.CodeConflict, "supervisor.restart", "restart policy denies another attempt")
	}
	prepared, err := s.isolation.Prepare(ctx, s.spec)
	if err != nil {
		s.state = StateFailed
		s.lastError = domain.SafeMessage(err)
		_ = s.report(ctx, s.lastError)
		return err
	}
	s.prepared = prepared
	s.process = spi.ProcessRef{}
	s.exit = nil
	s.state = StateAdmitted
	s.attempts++
	return s.report(ctx, "restart prepared")
}

func (s *Session) conflict(operation string, state State) error {
	return domain.New(domain.CodeConflict, "supervisor."+operation, "session is not ready for this operation (state="+string(state)+")")
}

func (s *Session) report(ctx context.Context, reason string) error {
	event := Event{
		Instance:   s.instance,
		State:      s.state,
		Generation: s.instance.Generation,
		At:         s.now(),
		Reason:     reason,
		Exit:       s.exit,
	}
	return s.reporter.Report(ctx, event)
}
