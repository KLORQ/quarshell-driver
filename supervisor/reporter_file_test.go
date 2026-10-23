package supervisor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KLORQ/quarshell-core/domain"
)

func TestFileReporterAppendsAndSyncs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "events.jsonl")
	reporter, err := NewFileReporter(path)
	if err != nil {
		t.Fatalf("NewFileReporter: %v", err)
	}
	instance := domain.InstanceRef{
		SandboxRef: domain.SandboxRef{Workspace: "ws", Sandbox: "sb"},
		Instance:   "inst",
		Generation: 1,
	}
	events := []Event{
		{Instance: instance, State: StateAdmitted, Generation: 1},
		{Instance: instance, State: StateStopped, Generation: 1, Exit: &ExitStatus{Code: 0, Exited: true}},
	}
	for _, event := range events {
		if err := reporter.Report(context.Background(), event); err != nil {
			t.Fatalf("Report: %v", err)
		}
	}
	if err := reporter.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != len(events) {
		t.Fatalf("lines = %d", len(lines))
	}
	for index, line := range lines {
		var decoded Event
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if decoded.State != events[index].State || decoded.Instance != instance {
			t.Fatalf("event[%d] = %+v", index, decoded)
		}
	}
	if err := reporter.Report(context.Background(), Event{}); err == nil {
		t.Fatalf("report after close succeeded")
	}
}

func TestFileReporterRejectsBadInput(t *testing.T) {
	if _, err := NewFileReporter(""); err == nil {
		t.Fatalf("empty path accepted")
	}
	reporter, err := NewFileReporter(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatalf("NewFileReporter: %v", err)
	}
	defer reporter.Close()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := reporter.Report(canceled, Event{}); err == nil {
		t.Fatalf("canceled report succeeded")
	}
}
