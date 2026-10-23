package supervisor

import (
	"context"
	"sync"

	"github.com/KLORQ/quarshell-core/domain"
)

// BoundedReporter buffers a bounded number of events when the durable sink is
// unavailable. It never grows without limit and never silently drops an event.
type BoundedReporter struct {
	mu      sync.Mutex
	sink    Reporter
	max     int
	pending []Event
}

// NewBoundedReporter wraps a sink with a pending-event bound.
func NewBoundedReporter(sink Reporter, maxPending int) (*BoundedReporter, error) {
	if sink == nil {
		return nil, domain.Invalidf("supervisor.reporter", "sink is required")
	}
	if maxPending <= 0 {
		maxPending = 64
	}
	return &BoundedReporter{sink: sink, max: maxPending}, nil
}

// Report sends the event to the sink or buffers it. A buffered event returns an
// unavailable error so the caller knows durability is pending.
func (r *BoundedReporter) Report(ctx context.Context, event Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pending) == 0 {
		if err := r.sink.Report(ctx, event); err == nil {
			return nil
		}
	}
	if len(r.pending) >= r.max {
		return domain.New(domain.CodeUnavailable, "supervisor.report", "pending event buffer is full")
	}
	r.pending = append(r.pending, event)
	return domain.New(domain.CodeUnavailable, "supervisor.report", "event buffered after sink failure")
}

// Flush drains pending events in order. A failure leaves the remaining events
// buffered.
func (r *BoundedReporter) Flush(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for len(r.pending) > 0 {
		if err := r.sink.Report(ctx, r.pending[0]); err != nil {
			return err
		}
		r.pending = r.pending[1:]
	}
	return nil
}

// Pending returns the number of buffered events.
func (r *BoundedReporter) Pending() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pending)
}
