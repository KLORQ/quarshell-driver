package docker

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	core "github.com/KLORQ/quarshell-core"
	"github.com/KLORQ/quarshell-driver/driver"
	mobyevents "github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"
)

var _ driver.PlatformEventStream = (*Driver)(nil)

// WatchPlatformEvents adapts Docker-compatible Engine container events to the
// optional driver stream. It filters by the immutable engine container ID and
// omits raw actor attributes, which may contain arbitrary labels or values.
func (d *Driver) WatchPlatformEvents(ctx context.Context, request driver.PlatformEventWatchRequest) (<-chan driver.PlatformEventResult, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%s platform events: context is nil", d.ID())
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.SandboxID == "" {
		return nil, fmt.Errorf("%s platform events: sandbox ID is required", d.ID())
	}
	if d == nil || d.cli == nil {
		return nil, fmt.Errorf("%s platform events: Engine client is unavailable", d.ID())
	}

	filters := make(client.Filters).Add("type", string(mobyevents.ContainerEventType)).Add("container", string(request.SandboxID))
	stream := d.cli.Events(ctx, client.EventsListOptions{Filters: filters})
	results := make(chan driver.PlatformEventResult)
	go d.forwardPlatformEvents(ctx, request, stream, results)
	return results, nil
}

func (d *Driver) forwardPlatformEvents(ctx context.Context, request driver.PlatformEventWatchRequest, stream client.EventsResult, results chan<- driver.PlatformEventResult) {
	defer close(results)
	for {
		select {
		case <-ctx.Done():
			return
		case message, ok := <-stream.Messages:
			if !ok {
				return
			}
			if message.Actor.ID != string(request.SandboxID) {
				continue
			}
			result := driver.PlatformEventResult{Event: platformEvent(string(d.ID()), request.SandboxID, message)}
			select {
			case results <- result:
			case <-ctx.Done():
				return
			}
		case err, ok := <-stream.Err:
			if !ok || err == nil || err == io.EOF || ctx.Err() != nil {
				return
			}
			select {
			case results <- driver.PlatformEventResult{Err: fmt.Errorf("%s platform event stream: %w", d.ID(), err)}:
			case <-ctx.Done():
			}
			return
		}
	}
}

func platformEvent(source string, sandboxID core.ID, message mobyevents.Message) driver.PlatformEvent {
	when := time.Time{}
	if message.TimeNano != 0 {
		when = time.Unix(0, message.TimeNano).UTC()
	} else if message.Time != 0 {
		when = time.Unix(message.Time, 0).UTC()
	}
	action := string(message.Action)
	if separator := strings.IndexByte(action, ':'); separator >= 0 {
		// Engine actions such as "exec_create: <command>" embed command text.
		action = action[:separator]
	}
	return driver.PlatformEvent{
		SandboxID: sandboxID,
		Time:      when,
		Source:    source,
		Type:      string(message.Type),
		Reason:    action,
		Message:   string(message.Type) + " " + action,
		Terminal:  message.Type == mobyevents.ContainerEventType && (action == string(mobyevents.ActionDie) || action == string(mobyevents.ActionDestroy) || action == string(mobyevents.ActionRemove)),
	}
}
