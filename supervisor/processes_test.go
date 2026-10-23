package supervisor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeStat(t *testing.T, root string, pid, ppid int, ticks uint64) {
	t.Helper()
	directory := filepath.Join(root, fmt.Sprintf("%d", pid))
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	fields := []string{"S", fmt.Sprintf("%d", ppid), "1", "1", "0", "-1", "0"}
	for len(fields) < 19 {
		fields = append(fields, "0")
	}
	fields = append(fields, fmt.Sprintf("%d", ticks))
	line := fmt.Sprintf("%d (proc%d) %s\n", pid, pid, strings.Join(fields, " "))
	if err := os.WriteFile(filepath.Join(directory, "stat"), []byte(line), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestProcessScannerDescendants(t *testing.T) {
	root := t.TempDir()
	writeStat(t, root, 1, 0, 10)
	writeStat(t, root, 2, 1, 20)
	writeStat(t, root, 3, 2, 30)
	writeStat(t, root, 4, 0, 40)
	if err := os.MkdirAll(filepath.Join(root, "not-a-pid"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "5"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "5", "stat"), []byte("malformed"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	boot := time.Unix(1_700_000_000, 0)
	scanner := ProcessScanner{ProcRoot: root, BootTime: func() (time.Time, error) { return boot, nil }, ClockTicks: 100}
	processes, err := scanner.Descendants(context.Background(), 1)
	if err != nil {
		t.Fatalf("Descendants: %v", err)
	}
	if len(processes) != 2 || processes[0].PID != 2 || processes[1].PID != 3 {
		t.Fatalf("processes = %+v", processes)
	}
	if want := boot.Add(200 * time.Millisecond); !processes[0].StartTime.Equal(want) {
		t.Fatalf("start time = %s, want %s", processes[0].StartTime, want)
	}
	unrelated, err := scanner.Descendants(context.Background(), 4)
	if err != nil || len(unrelated) != 0 {
		t.Fatalf("unrelated = %+v, %v", unrelated, err)
	}
}

func TestProcessScannerBoundsAndErrors(t *testing.T) {
	root := t.TempDir()
	writeStat(t, root, 1, 0, 1)
	writeStat(t, root, 2, 1, 2)
	scanner := ProcessScanner{ProcRoot: root, BootTime: func() (time.Time, error) { return time.Unix(0, 0), nil }, ClockTicks: 100, MaxProcesses: 1}
	if _, err := scanner.Descendants(context.Background(), 1); err == nil {
		t.Fatalf("bound was not enforced")
	}
	if _, err := (ProcessScanner{ProcRoot: root}).Descendants(context.Background(), 0); err == nil {
		t.Fatalf("zero root pid accepted")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (ProcessScanner{ProcRoot: root, BootTime: func() (time.Time, error) { return time.Unix(0, 0), nil }}).Descendants(canceled, 1); err == nil {
		t.Fatalf("canceled scan succeeded")
	}
}
