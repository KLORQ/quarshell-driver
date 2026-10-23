package supervisor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/KLORQ/quarshell-core/domain"
	spi "github.com/KLORQ/quarshell-core/isolation/port"
)

type fakePrepared struct {
	instance domain.InstanceRef
	expires  time.Time
}

func (f fakePrepared) Instance() domain.InstanceRef { return f.instance }
func (f fakePrepared) ExpiresAt() time.Time         { return f.expires }

type fakeIsolation struct {
	prepareErr error
	launchErr  error
	stopErr    error

	prepared int
	launched int
	stopped  int
}

func (f *fakeIsolation) Probe(context.Context, domain.InstanceRef, spi.SecurityRequirements) (spi.SecurityEvidence, error) {
	return spi.SecurityEvidence{}, nil
}

func (f *fakeIsolation) Prepare(_ context.Context, spec spi.LaunchSpec) (spi.PreparedLaunch, error) {
	f.prepared++
	if f.prepareErr != nil {
		return nil, f.prepareErr
	}
	return fakePrepared{instance: spec.Instance, expires: time.Now().Add(time.Minute)}, nil
}

func (f *fakeIsolation) Launch(context.Context, spi.PreparedLaunch) (spi.ProcessRef, error) {
	f.launched++
	if f.launchErr != nil {
		return spi.ProcessRef{}, f.launchErr
	}
	return spi.ProcessRef{PID: 42, StartTime: time.Now()}, nil
}

func (f *fakeIsolation) Stop(context.Context, spi.ProcessRef) error {
	f.stopped++
	return f.stopErr
}

type fakeReporter struct {
	mu     sync.Mutex
	events []Event
	err    error
}

func (f *fakeReporter) Report(_ context.Context, event Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
	return f.err
}

func (f *fakeReporter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

func (f *fakeReporter) last() Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.events[len(f.events)-1]
}

func supervisorSpec() spi.LaunchSpec {
	return spi.LaunchSpec{
		Instance: domain.InstanceRef{
			SandboxRef: domain.SandboxRef{Workspace: "ws", Sandbox: "sb"},
			Instance:   "inst",
			Generation: 1,
		},
		Command:     []string{"/usr/bin/env", "true"},
		Environment: map[string]string{"PATH": "/usr/bin"},
		Workdir:     "/workspace",
		UID:         1000,
		GID:         1000,
		PolicyHash:  "abcdef0123456789",
	}
}

func TestRestartPolicyAllows(t *testing.T) {
	success := &ExitStatus{Code: 0, Exited: true}
	failure := &ExitStatus{Code: 1, Exited: true}
	cases := []struct {
		name     string
		policy   RestartPolicy
		exit     *ExitStatus
		attempts int
		want     bool
	}{
		{"never", RestartPolicy{Mode: RestartNever}, failure, 0, false},
		{"on failure", RestartPolicy{Mode: RestartOnFailure}, failure, 0, true},
		{"on success", RestartPolicy{Mode: RestartOnFailure}, success, 0, false},
		{"missing exit", RestartPolicy{Mode: RestartOnFailure}, nil, 0, true},
		{"always", RestartPolicy{Mode: RestartAlways}, success, 0, true},
		{"max attempts", RestartPolicy{Mode: RestartAlways, MaxAttempts: 2}, success, 2, false},
		{"default attempts", RestartPolicy{Mode: RestartAlways}, success, DefaultRestartAttempts, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := test.policy.Allows(test.exit, test.attempts); got != test.want {
				t.Fatalf("Allows = %v, want %v", got, test.want)
			}
		})
	}
}

func TestSessionRestart(t *testing.T) {
	backend := &fakeIsolation{}
	reporter := &fakeReporter{}
	session, err := NewSession(supervisorSpec().Instance, backend, reporter)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	session.SetRestartPolicy(RestartPolicy{Mode: RestartAlways, MaxAttempts: 1})
	if err := session.Admit(context.Background(), supervisorSpec()); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if err := session.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := session.Exit(context.Background(), ExitStatus{Code: 1, Exited: true}); err != nil {
		t.Fatalf("Exit: %v", err)
	}
	if err := session.Restart(context.Background()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if session.State() != StateAdmitted || backend.prepared != 2 {
		t.Fatalf("state=%q prepared=%d", session.State(), backend.prepared)
	}
	if err := session.Start(context.Background()); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if err := session.Exit(context.Background(), ExitStatus{Code: 1, Exited: true}); err != nil {
		t.Fatalf("second Exit: %v", err)
	}
	if err := session.Restart(context.Background()); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("restart past max = %v", err)
	}
}

func TestSessionRestartRejectsRunningSession(t *testing.T) {
	session, err := NewSession(supervisorSpec().Instance, &fakeIsolation{}, &fakeReporter{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	session.SetRestartPolicy(RestartPolicy{Mode: RestartAlways})
	if err := session.Admit(context.Background(), supervisorSpec()); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if err := session.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := session.Restart(context.Background()); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("restart while running = %v", err)
	}
}

func TestSessionLifecycle(t *testing.T) {
	backend := &fakeIsolation{}
	reporter := &fakeReporter{}
	session, err := NewSession(supervisorSpec().Instance, backend, reporter)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if session.State() != StateRequested {
		t.Fatalf("state = %q", session.State())
	}
	if err := session.Admit(context.Background(), supervisorSpec()); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if backend.prepared != 1 || session.State() != StateAdmitted {
		t.Fatalf("prepared=%d state=%q", backend.prepared, session.State())
	}
	if err := session.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if backend.launched != 1 || session.State() != StateRunning {
		t.Fatalf("launched=%d state=%q", backend.launched, session.State())
	}
	exit := ExitStatus{Code: 0, Exited: true}
	if err := session.Exit(context.Background(), exit); err != nil {
		t.Fatalf("Exit: %v", err)
	}
	if session.State() != StateStopped {
		t.Fatalf("state = %q", session.State())
	}
	last := reporter.last()
	if last.State != StateStopped || last.Exit == nil || last.Exit.Code != 0 {
		t.Fatalf("last event = %+v", last)
	}
	if reporter.count() != 3 {
		t.Fatalf("events = %d", reporter.count())
	}
}

func TestSessionRejectsInvalidTransitions(t *testing.T) {
	session, err := NewSession(supervisorSpec().Instance, &fakeIsolation{}, &fakeReporter{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := session.Start(context.Background()); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("Start before Admit = %v", err)
	}
	if err := session.Exit(context.Background(), ExitStatus{}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("Exit before Start = %v", err)
	}
	if err := session.Stop(context.Background()); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("Stop before Start = %v", err)
	}
	if err := session.Admit(context.Background(), supervisorSpec()); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if err := session.Admit(context.Background(), supervisorSpec()); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second Admit = %v", err)
	}
	if err := session.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := session.Start(context.Background()); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second Start = %v", err)
	}
}

func TestSessionLaunchFailureIsReported(t *testing.T) {
	backend := &fakeIsolation{launchErr: errors.New("boom")}
	reporter := &fakeReporter{}
	session, err := NewSession(supervisorSpec().Instance, backend, reporter)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := session.Admit(context.Background(), supervisorSpec()); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if err := session.Start(context.Background()); err == nil {
		t.Fatalf("Start accepted a launch failure")
	}
	if session.State() != StateFailed {
		t.Fatalf("state = %q", session.State())
	}
	if reporter.last().State != StateFailed {
		t.Fatalf("last event = %+v", reporter.last())
	}
}

func TestSessionStop(t *testing.T) {
	backend := &fakeIsolation{}
	reporter := &fakeReporter{}
	session, err := NewSession(supervisorSpec().Instance, backend, reporter)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := session.Admit(context.Background(), supervisorSpec()); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if err := session.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := session.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if backend.stopped != 1 || session.State() != StateStopped {
		t.Fatalf("stopped=%d state=%q", backend.stopped, session.State())
	}
}

func TestAdmissionRejectsInvalidSpecAndDeadline(t *testing.T) {
	if _, err := (Admission{}).Admit(context.Background(), spi.LaunchSpec{}); err == nil {
		t.Fatalf("invalid spec admitted")
	}
	clock := &incrementingClock{step: 2 * time.Second}
	if _, err := (Admission{Deadline: time.Second, Now: clock.now}).Admit(context.Background(), supervisorSpec()); !errors.Is(err, domain.ErrDeadlineExceeded) {
		t.Fatalf("deadline error = %v", err)
	}
}

type incrementingClock struct {
	mu   sync.Mutex
	next time.Time
	step time.Duration
}

func (c *incrementingClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.next = c.next.Add(c.step)
	return c.next
}

type flakySink struct {
	fail bool
	seen []Event
}

func (f *flakySink) Report(_ context.Context, event Event) error {
	if f.fail {
		return errors.New("sink unavailable")
	}
	f.seen = append(f.seen, event)
	return nil
}

func TestBoundedReporterBuffersAndFlushes(t *testing.T) {
	sink := &flakySink{fail: true}
	reporter, err := NewBoundedReporter(sink, 2)
	if err != nil {
		t.Fatalf("NewBoundedReporter: %v", err)
	}
	if err := reporter.Report(context.Background(), Event{State: StateAdmitted}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("buffered report = %v", err)
	}
	if reporter.Pending() != 1 {
		t.Fatalf("pending = %d", reporter.Pending())
	}
	if err := reporter.Report(context.Background(), Event{State: StateRunning}); err == nil {
		t.Fatalf("second buffered report returned nil")
	}
	if err := reporter.Report(context.Background(), Event{State: StateStopped}); err == nil {
		t.Fatalf("overflow report returned nil")
	}
	if reporter.Pending() != 2 {
		t.Fatalf("pending after overflow = %d", reporter.Pending())
	}
	sink.fail = false
	if err := reporter.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if reporter.Pending() != 0 || len(sink.seen) != 2 {
		t.Fatalf("pending=%d seen=%d", reporter.Pending(), len(sink.seen))
	}
}
