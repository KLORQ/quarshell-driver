package supervisor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/KLORQ/quarshell-core/domain"
)

// FileReporter appends one JSON object per line and fsyncs each event. It is a
// durable local sink for a single supervisor process; rotation, compaction and
// cross-node reconciliation belong to the deployment layer.
type FileReporter struct {
	mu   sync.Mutex
	file *os.File
}

// NewFileReporter opens or creates an append-only event log with mode 0600.
func NewFileReporter(path string) (*FileReporter, error) {
	if path == "" {
		return nil, domain.Invalidf("supervisor.reporter", "path is required")
	}
	if directory := filepath.Dir(path); directory != "." {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, domain.Wrap(err, "supervisor.reporter", "create event directory")
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, domain.Wrap(err, "supervisor.reporter", "open event log")
	}
	return &FileReporter{file: file}, nil
}

// Report appends and fsyncs one event.
func (r *FileReporter) Report(ctx context.Context, event Event) error {
	if err := ctx.Err(); err != nil {
		return domain.Wrap(err, "supervisor.reporter", "report canceled")
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		return domain.Wrap(err, "supervisor.reporter", "encode event")
	}
	encoded = append(encoded, '\n')
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return domain.New(domain.CodeUnavailable, "supervisor.reporter", "event log is closed")
	}
	if _, err := r.file.Write(encoded); err != nil {
		return domain.Wrap(err, "supervisor.reporter", "append event")
	}
	if err := r.file.Sync(); err != nil {
		return domain.Wrap(err, "supervisor.reporter", "sync event")
	}
	return nil
}

// Close closes the event log.
func (r *FileReporter) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}
