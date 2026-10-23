//go:build linux

package isolation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/KLORQ/quarshell-core/domain"
	spi "github.com/KLORQ/quarshell-core/isolation/port"
	ll "github.com/landlock-lsm/go-landlock/landlock"
	"golang.org/x/sys/unix"
)

const (
	childEnvironment      = "QUARSHELL_ISOLATION_CHILD"
	childSpecFD           = 3
	maxChildSpecBytes     = 16 << 20
	startTimeTolerance    = 3 * time.Second
	processStopDeadline   = 5 * time.Second
	landlockCreateVersion = 1 << 0
)

// Linux is the privileged Linux isolation implementation. It re-executes the
// current binary as a restricted child; the child applies Landlock, drops
// capabilities and identity, and then execs the workload.
type Linux struct {
	ttl        time.Duration
	self       string
	now        func() time.Time
	cgroupRoot string

	mu      sync.Mutex
	procs   map[int]*exec.Cmd
	cgroups map[int]*Cgroup
}

var _ spi.Isolation = (*Linux)(nil)

// NewLinux returns a Linux isolation implementation.
func NewLinux(options LinuxOptions) *Linux {
	ttl := options.PreparedTTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	now := options.Clock
	if now == nil {
		now = time.Now
	}
	return &Linux{ttl: ttl, self: options.Self, now: now, cgroupRoot: options.CgroupRoot, procs: map[int]*exec.Cmd{}, cgroups: map[int]*Cgroup{}}
}

type preparedLaunch struct {
	spec      spi.LaunchSpec
	expiresAt time.Time
}

func (p *preparedLaunch) Instance() domain.InstanceRef { return p.spec.Instance }
func (p *preparedLaunch) ExpiresAt() time.Time         { return p.expiresAt }

// Probe reports the controls this kernel actually exposes. It never claims a
// capability that was not observed.
func (l *Linux) Probe(ctx context.Context, instance domain.InstanceRef, requirements spi.SecurityRequirements) (spi.SecurityEvidence, error) {
	if err := ctx.Err(); err != nil {
		return spi.SecurityEvidence{}, domain.Wrap(err, "isolation.probe", "probe canceled")
	}
	abi, abiErr := landlockABI()
	capabilities := []domain.Capability{domain.CapExec}
	items := make([]spi.EvidenceItem, 0, 8)
	if abiErr == nil {
		capabilities = append(capabilities, domain.CapLandlock)
		items = append(items, spi.EvidenceItem{Name: "landlock", Source: "syscall", Details: map[string]string{
			"abi": strconv.Itoa(abi),
		}})
		if abi >= 4 {
			capabilities = append(capabilities, domain.CapNetworkMediation)
		}
	} else {
		items = append(items, spi.EvidenceItem{Name: "landlock", Source: "syscall", Details: map[string]string{
			"available": "false",
		}})
	}
	if seccompNotificationAvailable() {
		capabilities = append(capabilities, domain.CapSeccompNotification)
		items = append(items, spi.EvidenceItem{Name: "seccomp.user_notification", Source: "procfs"})
	}
	if userNamespacesAvailable() {
		capabilities = append(capabilities, domain.CapUserNamespaces)
		items = append(items, spi.EvidenceItem{Name: "user_namespaces", Source: "procfs"})
	}
	if cgroupsV2() {
		capabilities = append(capabilities, domain.CapResourceLimits)
		items = append(items, spi.EvidenceItem{Name: "cgroup.v2", Source: "procfs"})
	}
	for _, path := range append(append(append([]string{}, requirements.Filesystem.Read...), requirements.Filesystem.Write...), requirements.Filesystem.Exec...) {
		details := map[string]string{"path": path}
		if _, err := os.Stat(path); err == nil {
			details["available"] = "true"
		} else {
			details["available"] = "false"
		}
		items = append(items, spi.EvidenceItem{Name: "filesystem.path", Source: "stat", Details: details})
	}
	return spi.SecurityEvidence{
		Instance:     instance,
		Kernel:       kernelRelease(),
		Capabilities: domain.NewCapabilitySet(capabilities...),
		Items:        items,
		VerifiedAt:   l.now(),
	}, nil
}

// Prepare validates the launch request and returns a time-bounded handle.
func (l *Linux) Prepare(ctx context.Context, spec spi.LaunchSpec) (spi.PreparedLaunch, error) {
	if err := ctx.Err(); err != nil {
		return nil, domain.Wrap(err, "isolation.prepare", "prepare canceled")
	}
	sanitized, err := ValidateLaunchSpec(spec)
	if err != nil {
		return nil, err
	}
	return &preparedLaunch{spec: sanitized, expiresAt: l.now().Add(l.ttl)}, nil
}

// Launch starts the restricted child and returns a PID plus wall-clock start
// time. The child cannot be constructed from user JSON.
func (l *Linux) Launch(ctx context.Context, prepared spi.PreparedLaunch) (spi.ProcessRef, error) {
	if err := ctx.Err(); err != nil {
		return spi.ProcessRef{}, domain.Wrap(err, "isolation.launch", "launch canceled")
	}
	launch, ok := prepared.(*preparedLaunch)
	if !ok || launch == nil {
		return spi.ProcessRef{}, domain.Invalidf("isolation.launch", "unknown prepared launch")
	}
	if !l.now().Before(launch.expiresAt) {
		return spi.ProcessRef{}, domain.New(domain.CodeConflict, "isolation.launch", "prepared launch expired")
	}
	self := l.self
	if self == "" {
		executable, err := os.Executable()
		if err != nil {
			return spi.ProcessRef{}, domain.Wrap(err, "isolation.launch", "resolve executable")
		}
		self = executable
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return spi.ProcessRef{}, domain.Wrap(err, "isolation.launch", "create spec socket")
	}
	childEnd := os.NewFile(uintptr(fds[0]), "launch-spec-child")
	parentEnd := os.NewFile(uintptr(fds[1]), "launch-spec-parent")
	descriptorFiles, err := openDescriptorSources(launch.spec.Descriptors)
	if err != nil {
		childEnd.Close()
		parentEnd.Close()
		return spi.ProcessRef{}, err
	}
	command := exec.Command(self)
	command.Dir = launch.spec.Workdir
	command.Env = []string{childEnvironment + "=1"}
	command.ExtraFiles = append([]*os.File{childEnd}, descriptorFiles...)
	attributes := &syscall.SysProcAttr{
		Setpgid:    true,
		Pdeathsig:  unix.SIGKILL,
		Cloneflags: unix.CLONE_NEWNS | unix.CLONE_NEWUTS | unix.CLONE_NEWIPC | unix.CLONE_NEWPID,
	}
	command.SysProcAttr = attributes
	if err := command.Start(); err != nil {
		childEnd.Close()
		parentEnd.Close()
		closeFiles(descriptorFiles)
		return spi.ProcessRef{}, domain.Wrap(err, "isolation.launch", "start restricted child")
	}
	_ = childEnd.Close()
	closeFiles(descriptorFiles)
	startedAt := l.now()
	written := make(chan error, 1)
	go func() {
		written <- writeLaunchSpec(parentEnd, launch.spec)
	}()
	select {
	case err := <-written:
		if err != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
			return spi.ProcessRef{}, err
		}
	case <-time.After(processStopDeadline):
		_ = command.Process.Kill()
		_ = command.Wait()
		return spi.ProcessRef{}, domain.New(domain.CodeUnavailable, "isolation.launch", "child did not accept launch spec")
	}
	pid := command.Process.Pid
	cgroup, err := l.attachCgroup(pid, launch.spec)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return spi.ProcessRef{}, err
	}
	l.mu.Lock()
	l.procs[pid] = command
	if cgroup != nil {
		l.cgroups[pid] = cgroup
	}
	l.mu.Unlock()
	go l.reap(pid, command)
	return spi.ProcessRef{Instance: launch.spec.Instance, PID: pid, StartTime: startedAt}, nil
}

// Stop verifies the kernel start time before signaling so a reused PID cannot
// target an unrelated process.
func (l *Linux) Stop(ctx context.Context, ref spi.ProcessRef) error {
	if err := ctx.Err(); err != nil {
		return domain.Wrap(err, "isolation.stop", "stop canceled")
	}
	if ref.PID <= 0 {
		return domain.Invalidf("isolation.stop", "invalid pid")
	}
	started, err := processStartTime(ref.PID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return domain.Wrap(err, "isolation.stop", "inspect process start time")
	}
	if delta := started.Sub(ref.StartTime); delta > startTimeTolerance || delta < -startTimeTolerance {
		return domain.New(domain.CodeConflict, "isolation.stop", "process identity changed")
	}
	if err := unix.Kill(-ref.PID, unix.SIGKILL); err != nil && !errors.Is(err, unix.ESRCH) {
		return domain.Wrap(err, "isolation.stop", "kill process group")
	}
	l.mu.Lock()
	command := l.procs[ref.PID]
	l.mu.Unlock()
	if command == nil {
		return nil
	}
	_ = command.Process.Kill()
	deadline := time.Now().Add(processStopDeadline)
	for {
		l.mu.Lock()
		_, exists := l.procs[ref.PID]
		l.mu.Unlock()
		if !exists {
			return nil
		}
		if time.Now().After(deadline) {
			return domain.New(domain.CodeUnavailable, "isolation.stop", "process did not exit")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (l *Linux) reap(pid int, command *exec.Cmd) {
	_ = command.Wait()
	l.mu.Lock()
	cgroup := l.cgroups[pid]
	delete(l.cgroups, pid)
	delete(l.procs, pid)
	l.mu.Unlock()
	if cgroup != nil {
		_ = cgroup.Remove()
	}
}

func (l *Linux) attachCgroup(pid int, spec spi.LaunchSpec) (*Cgroup, error) {
	process := spec.Requirements.Process
	if process.PidsLimit == 0 && process.MemoryBytes == 0 && process.CPUQuotaMicros == 0 {
		return nil, nil
	}
	name := "quarshell-" + sanitizeCgroupName(string(spec.Instance.Sandbox)) + "-" + strconv.Itoa(pid)
	cgroup, err := NewCgroup(l.cgroupRoot, name, CgroupLimits{
		PidsMax:         process.PidsLimit,
		MemoryMaxBytes:  process.MemoryBytes,
		CPUQuotaMicros:  process.CPUQuotaMicros,
		CPUPeriodMicros: process.CPUPeriodMicros,
	})
	if err != nil {
		return nil, err
	}
	if err := cgroup.Create(); err != nil {
		return nil, err
	}
	if err := cgroup.AddProcess(pid); err != nil {
		_ = cgroup.Remove()
		return nil, err
	}
	return cgroup, nil
}

func writeLaunchSpec(writer *os.File, spec spi.LaunchSpec) error {
	defer writer.Close()
	encoded, err := json.Marshal(spec)
	if err != nil {
		return domain.Wrap(err, "isolation.launch", "encode launch spec")
	}
	if len(encoded) > maxChildSpecBytes {
		return domain.Invalidf("isolation.launch", "launch spec exceeds %d bytes", maxChildSpecBytes)
	}
	if _, err := writer.Write(encoded); err != nil {
		return domain.Wrap(err, "isolation.launch", "write launch spec")
	}
	return nil
}

func openDescriptorSources(descriptors []spi.Descriptor) ([]*os.File, error) {
	files := make([]*os.File, 0, len(descriptors))
	for _, descriptor := range descriptors {
		flags := os.O_RDONLY
		if descriptor.Writable {
			flags = os.O_RDWR
		}
		file, err := os.OpenFile(descriptor.Source, flags, 0)
		if err != nil {
			closeFiles(files)
			return nil, domain.Wrap(err, "isolation.launch", "open descriptor source")
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			_ = file.Close()
			closeFiles(files)
			return nil, domain.Invalidf("isolation.launch", "descriptor source must be a regular file")
		}
		files = append(files, file)
	}
	return files, nil
}

func closeFiles(files []*os.File) {
	for _, file := range files {
		_ = file.Close()
	}
}

const (
	descriptorSourceBase = 4
	descriptorTempBase   = MaxDescriptorFD + 1
)

func installDescriptors(descriptors []spi.Descriptor) error {
	if len(descriptors) == 0 {
		return nil
	}
	temps := make([]int, len(descriptors))
	for index := range descriptors {
		sourceFD := descriptorSourceBase + index
		tempFD, err := unix.FcntlInt(uintptr(sourceFD), unix.F_DUPFD_CLOEXEC, descriptorTempBase)
		if err != nil {
			for _, opened := range temps[:index] {
				_ = unix.Close(opened)
			}
			return fmt.Errorf("isolation child: duplicate descriptor %d: %w", sourceFD, err)
		}
		temps[index] = tempFD
	}
	for index := range descriptors {
		_ = unix.Close(descriptorSourceBase + index)
	}
	for index, descriptor := range descriptors {
		if err := unix.Dup3(temps[index], descriptor.FD, 0); err != nil {
			for _, opened := range temps[index:] {
				_ = unix.Close(opened)
			}
			return fmt.Errorf("isolation child: install descriptor %d: %w", descriptor.FD, err)
		}
	}
	for _, temp := range temps {
		_ = unix.Close(temp)
	}
	return nil
}

// RunChildIfRequested is called at process startup. It returns true only when
// the process was re-executed as the restricted child.
func RunChildIfRequested() (bool, error) {
	if os.Getenv(childEnvironment) != "1" {
		return false, nil
	}
	return true, runChild()
}

func runChild() error {
	specFile := os.NewFile(childSpecFD, "launch-spec")
	if specFile == nil {
		return fmt.Errorf("isolation child: launch spec descriptor is missing")
	}
	peer, err := unix.GetsockoptUcred(childSpecFD, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		_ = specFile.Close()
		return fmt.Errorf("isolation child: peer credentials: %w", err)
	}
	if peer.Uid != uint32(os.Geteuid()) || peer.Gid != uint32(os.Getegid()) {
		_ = specFile.Close()
		return fmt.Errorf("isolation child: unauthenticated supervisor")
	}
	body, err := io.ReadAll(io.LimitReader(specFile, maxChildSpecBytes+1))
	_ = specFile.Close()
	if err != nil {
		return fmt.Errorf("isolation child: read launch spec: %w", err)
	}
	if len(body) > maxChildSpecBytes {
		return fmt.Errorf("isolation child: launch spec exceeds %d bytes", maxChildSpecBytes)
	}
	var spec spi.LaunchSpec
	if err := json.Unmarshal(body, &spec); err != nil {
		return fmt.Errorf("isolation child: decode launch spec: %w", err)
	}
	spec, err = ValidateLaunchSpec(spec)
	if err != nil {
		return err
	}
	if err := installDescriptors(spec.Descriptors); err != nil {
		return err
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("isolation child: no_new_privs: %w", err)
	}
	dropBoundingCapabilities()
	if err := applyLandlock(spec.Requirements); err != nil {
		return err
	}
	if err := applyIdentity(spec); err != nil {
		return err
	}
	if spec.Requirements.Process.SeccompProfile == "baseline" {
		if err := installBaselineSeccomp(); err != nil {
			return err
		}
	}
	environment := make([]string, 0, len(spec.Environment))
	keys := make([]string, 0, len(spec.Environment))
	for key := range spec.Environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		environment = append(environment, key+"="+spec.Environment[key])
	}
	return unix.Exec(spec.Command[0], spec.Command, environment)
}

func applyIdentity(spec spi.LaunchSpec) error {
	if os.Geteuid() == 0 {
		groups := make([]int, len(spec.Groups))
		for index, group := range spec.Groups {
			if uint64(group) > uint64(math.MaxInt) {
				return domain.Invalidf("isolation.child", "supplementary group is out of range")
			}
			groups[index] = int(group)
		}
		if err := syscall.Setgroups(groups); err != nil {
			return fmt.Errorf("isolation child: setgroups: %w", err)
		}
		if err := syscall.Setgid(int(spec.GID)); err != nil {
			return fmt.Errorf("isolation child: setgid: %w", err)
		}
		if err := syscall.Setuid(int(spec.UID)); err != nil {
			return fmt.Errorf("isolation child: setuid: %w", err)
		}
	} else if os.Geteuid() != int(spec.UID) || os.Getegid() != int(spec.GID) {
		return fmt.Errorf("isolation child: cannot switch identity without root")
	}
	if os.Geteuid() != int(spec.UID) || os.Getegid() != int(spec.GID) {
		return fmt.Errorf("isolation child: identity verification failed")
	}
	return nil
}

func dropBoundingCapabilities() {
	for capability := 0; capability < 64; capability++ {
		_ = unix.Prctl(unix.PR_CAPBSET_DROP, uintptr(capability), 0, 0, 0)
	}
}

func applyLandlock(requirements spi.SecurityRequirements) error {
	reads := uniquePaths(append(append([]string{}, requirements.Filesystem.Read...), requirements.Filesystem.Exec...))
	writes := uniquePaths(requirements.Filesystem.Write)
	strict := len(reads) > 0 || len(writes) > 0
	if !strict {
		reads = []string{"/usr", "/bin", "/lib", "/lib64", "/etc", "/proc"}
		writes = []string{"/tmp", "/dev/null"}
	}
	roDirs, roFiles, err := collectPaths(reads, strict)
	if err != nil {
		return err
	}
	rwDirs, rwFiles, err := collectPaths(writes, strict)
	if err != nil {
		return err
	}
	rules := make([]ll.Rule, 0, 4)
	if len(roDirs) > 0 {
		rules = append(rules, ll.RODirs(roDirs...))
	}
	if len(roFiles) > 0 {
		rules = append(rules, ll.ROFiles(roFiles...))
	}
	if len(rwDirs) > 0 {
		rules = append(rules, ll.RWDirs(rwDirs...))
	}
	if len(rwFiles) > 0 {
		rules = append(rules, ll.RWFiles(rwFiles...))
	}
	if len(rules) == 0 {
		return fmt.Errorf("isolation child: no filesystem rules")
	}
	if err := ll.V6.BestEffort().RestrictPaths(rules...); err != nil {
		return fmt.Errorf("isolation child: landlock paths: %w", err)
	}
	if ports := endpointPorts(requirements.Network.Endpoints); len(ports) > 0 {
		netRules := make([]ll.Rule, 0, len(ports))
		for _, port := range ports {
			netRules = append(netRules, ll.ConnectTCP(port))
		}
		if err := ll.V6.BestEffort().RestrictNet(netRules...); err != nil {
			return fmt.Errorf("isolation child: landlock network: %w", err)
		}
	}
	if err := ll.V6.BestEffort().RestrictScoped(); err != nil {
		return fmt.Errorf("isolation child: landlock scoped: %w", err)
	}
	return nil
}

func collectPaths(paths []string, strict bool) (dirs, files []string, err error) {
	for _, path := range paths {
		info, statErr := os.Stat(path)
		if statErr != nil {
			if !strict && os.IsNotExist(statErr) {
				continue
			}
			return nil, nil, fmt.Errorf("isolation child: stat %s: %w", path, statErr)
		}
		if info.IsDir() {
			dirs = append(dirs, path)
		} else {
			files = append(files, path)
		}
	}
	return dirs, files, nil
}

func uniquePaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		result = append(result, path)
	}
	return result
}

func endpointPorts(endpoints []string) []uint16 {
	seen := map[uint16]struct{}{}
	result := make([]uint16, 0, len(endpoints))
	for _, endpoint := range endpoints {
		_, portText, err := net.SplitHostPort(endpoint)
		if err != nil {
			continue
		}
		port, err := strconv.ParseUint(portText, 10, 16)
		if err != nil {
			continue
		}
		value := uint16(port)
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func landlockABI() (int, error) {
	result, _, errno := unix.Syscall(
		unix.SYS_LANDLOCK_CREATE_RULESET,
		0,
		0,
		landlockCreateVersion,
	)
	if errno != 0 {
		return 0, errno
	}
	return int(result), nil
}

func kernelRelease() string {
	var name unix.Utsname
	if err := unix.Uname(&name); err != nil {
		return ""
	}
	return unix.ByteSliceToString(name.Release[:])
}

func seccompNotificationAvailable() bool {
	data, err := os.ReadFile("/proc/sys/kernel/seccomp/actions_avail")
	if err != nil {
		return false
	}
	for _, action := range strings.Fields(string(data)) {
		if action == "user_notif" {
			return true
		}
	}
	return false
}

func userNamespacesAvailable() bool {
	data, err := os.ReadFile("/proc/sys/user/max_user_namespaces")
	if err == nil {
		value, parseErr := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
		return parseErr == nil && value > 0
	}
	data, err = os.ReadFile("/proc/sys/kernel/unprivileged_userns_clone")
	return err == nil && strings.TrimSpace(string(data)) == "1"
}

func cgroupsV2() bool {
	_, err := os.Stat("/sys/fs/cgroup/cgroup.controllers")
	return err == nil
}

func processStartTime(pid int) (time.Time, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return time.Time{}, err
	}
	close := bytes.LastIndexByte(data, ')')
	if close < 0 || close+2 >= len(data) {
		return time.Time{}, fmt.Errorf("malformed process stat")
	}
	fields := strings.Fields(string(data[close+1:]))
	if len(fields) < 20 {
		return time.Time{}, fmt.Errorf("malformed process stat")
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("malformed process start time")
	}
	boot, err := bootTime()
	if err != nil {
		return time.Time{}, err
	}
	return boot.Add(time.Duration(ticks) * (time.Second / 100)), nil
}

func bootTime() (time.Time, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "btime ") {
			continue
		}
		seconds, parseErr := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "btime ")), 10, 64)
		if parseErr != nil {
			return time.Time{}, fmt.Errorf("malformed btime")
		}
		return time.Unix(seconds, 0), nil
	}
	return time.Time{}, fmt.Errorf("missing btime")
}
