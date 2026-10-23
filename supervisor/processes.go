package supervisor

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KLORQ/quarshell-core/domain"
)

const (
	// DefaultMaxProcesses bounds one descendant scan.
	DefaultMaxProcesses = 10_000
	defaultClockTicks   = 100
)

// Process is one observed host process. StartTime is derived from the boot time
// and the kernel start ticks so PID reuse is detectable.
type Process struct {
	PID       int
	PPID      int
	StartTime time.Time
}

// ProcessScanner is a procfs polling baseline for descendant discovery.
//
// Polling cannot observe processes that exit between scans. The event-source
// spike must replace or supplement it with a kernel event source (pidfd/proc
// connector or eBPF) before claiming descendant coverage.
type ProcessScanner struct {
	ProcRoot     string
	BootTime     func() (time.Time, error)
	ClockTicks   int64
	MaxProcesses int
}

// Descendants returns all currently visible descendants of rootPID.
func (s ProcessScanner) Descendants(ctx context.Context, rootPID int) ([]Process, error) {
	if rootPID <= 0 {
		return nil, domain.Invalidf("supervisor.processes", "root pid must be positive")
	}
	root := s.ProcRoot
	if root == "" {
		root = "/proc"
	}
	clockTicks := s.ClockTicks
	if clockTicks <= 0 {
		clockTicks = defaultClockTicks
	}
	maxProcesses := s.MaxProcesses
	if maxProcesses <= 0 {
		maxProcesses = DefaultMaxProcesses
	}
	bootTime := s.BootTime
	if bootTime == nil {
		bootTime = readBootTime
	}
	boot, err := bootTime()
	if err != nil {
		return nil, domain.Wrap(err, "supervisor.processes", "read boot time")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, domain.Wrap(err, "supervisor.processes", "scan proc root")
	}
	processes := make(map[int]Process, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, domain.Wrap(err, "supervisor.processes", "scan canceled")
		}
		if !entry.IsDir() {
			continue
		}
		pid, ok := parsePID(entry.Name())
		if !ok {
			continue
		}
		if len(processes) >= maxProcesses {
			return nil, domain.New(domain.CodeUnavailable, "supervisor.processes", "process scan exceeded the bound")
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name(), "stat"))
		if err != nil {
			continue
		}
		process, err := parseProcessStat(pid, data, boot, clockTicks)
		if err != nil {
			continue
		}
		processes[pid] = process
	}
	children := make(map[int][]int, len(processes))
	for pid, process := range processes {
		children[process.PPID] = append(children[process.PPID], pid)
	}
	visited := map[int]struct{}{rootPID: {}}
	queue := []int{rootPID}
	result := make([]Process, 0, len(processes))
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		childIDs := children[current]
		sort.Ints(childIDs)
		for _, child := range childIDs {
			if _, seen := visited[child]; seen {
				continue
			}
			visited[child] = struct{}{}
			result = append(result, processes[child])
			queue = append(queue, child)
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].PID < result[right].PID })
	return result, nil
}

func parsePID(name string) (int, bool) {
	if name == "" {
		return 0, false
	}
	for _, char := range name {
		if char < '0' || char > '9' {
			return 0, false
		}
	}
	pid, err := strconv.Atoi(name)
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

func parseProcessStat(pid int, data []byte, boot time.Time, clockTicks int64) (Process, error) {
	closeIndex := bytes.LastIndexByte(data, ')')
	if closeIndex < 0 || closeIndex+2 >= len(data) {
		return Process{}, domain.Invalidf("supervisor.processes", "malformed process stat")
	}
	fields := strings.Fields(string(data[closeIndex+1:]))
	if len(fields) < 20 {
		return Process{}, domain.Invalidf("supervisor.processes", "malformed process stat")
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return Process{}, domain.Invalidf("supervisor.processes", "malformed ppid")
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return Process{}, domain.Invalidf("supervisor.processes", "malformed start time")
	}
	return Process{
		PID:       pid,
		PPID:      ppid,
		StartTime: boot.Add(time.Duration(ticks) * (time.Second / time.Duration(clockTicks))),
	}, nil
}

func readBootTime() (time.Time, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "btime ") {
			continue
		}
		seconds, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "btime ")), 10, 64)
		if err != nil {
			return time.Time{}, err
		}
		return time.Unix(seconds, 0), nil
	}
	return time.Time{}, domain.Invalidf("supervisor.processes", "missing btime")
}
