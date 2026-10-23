package isolation

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/KLORQ/quarshell-core/domain"
)

const defaultCPUPeriodMicros = 100_000

var cgroupNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// CgroupLimits are cgroup v2 resource limits. Zero means "leave unset".
type CgroupLimits struct {
	PidsMax         int64
	MemoryMaxBytes  int64
	CPUQuotaMicros  int64
	CPUPeriodMicros int64
}

// Cgroup is one cgroup v2 directory.
type Cgroup struct {
	root   string
	name   string
	limits CgroupLimits
}

// NewCgroup validates a cgroup request without touching the filesystem.
func NewCgroup(root, name string, limits CgroupLimits) (*Cgroup, error) {
	if root == "" {
		root = "/sys/fs/cgroup"
	}
	if !filepath.IsAbs(root) {
		return nil, domain.Invalidf("isolation.cgroup", "cgroup root must be absolute")
	}
	if !cgroupNamePattern.MatchString(name) || name == "." || name == ".." {
		return nil, domain.Invalidf("isolation.cgroup", "invalid cgroup name")
	}
	if limits.PidsMax < 0 || limits.MemoryMaxBytes < 0 || limits.CPUQuotaMicros < 0 || limits.CPUPeriodMicros < 0 {
		return nil, domain.Invalidf("isolation.cgroup", "cgroup limits must not be negative")
	}
	if limits.CPUQuotaMicros > 0 && limits.CPUPeriodMicros == 0 {
		limits.CPUPeriodMicros = defaultCPUPeriodMicros
	}
	return &Cgroup{root: root, name: name, limits: limits}, nil
}

// Path returns the cgroup directory path.
func (c *Cgroup) Path() string {
	return filepath.Join(c.root, c.name)
}

// Create makes the cgroup and writes the configured limits.
func (c *Cgroup) Create() error {
	if _, err := os.Stat(filepath.Join(c.root, "cgroup.controllers")); err != nil {
		return domain.Wrap(err, "isolation.cgroup", "cgroup v2 is unavailable")
	}
	path := c.Path()
	if err := os.Mkdir(path, 0o755); err != nil {
		return domain.Wrap(err, "isolation.cgroup", "create cgroup")
	}
	if err := c.writeLimits(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func (c *Cgroup) writeLimits() error {
	if c.limits.PidsMax > 0 {
		if err := writeCgroupFile(c.Path(), "pids.max", strconv.FormatInt(c.limits.PidsMax, 10)); err != nil {
			return err
		}
	}
	if c.limits.MemoryMaxBytes > 0 {
		if err := writeCgroupFile(c.Path(), "memory.max", strconv.FormatInt(c.limits.MemoryMaxBytes, 10)); err != nil {
			return err
		}
	}
	if c.limits.CPUQuotaMicros > 0 {
		period := c.limits.CPUPeriodMicros
		if period == 0 {
			period = defaultCPUPeriodMicros
		}
		value := fmt.Sprintf("%d %d", c.limits.CPUQuotaMicros, period)
		if err := writeCgroupFile(c.Path(), "cpu.max", value); err != nil {
			return err
		}
	}
	return nil
}

// AddProcess moves a process into the cgroup.
func (c *Cgroup) AddProcess(pid int) error {
	if pid <= 0 {
		return domain.Invalidf("isolation.cgroup", "pid must be positive")
	}
	if err := writeCgroupFile(c.Path(), "cgroup.procs", strconv.Itoa(pid)); err != nil {
		return err
	}
	return nil
}

// Remove removes an empty cgroup directory.
func (c *Cgroup) Remove() error {
	if err := os.Remove(c.Path()); err != nil && !os.IsNotExist(err) {
		return domain.Wrap(err, "isolation.cgroup", "remove cgroup")
	}
	return nil
}

func writeCgroupFile(directory, name, value string) error {
	file, err := os.OpenFile(filepath.Join(directory, name), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return domain.Wrap(err, "isolation.cgroup", "open "+name)
	}
	defer file.Close()
	if _, err := file.WriteString(strings.TrimSpace(value) + "\n"); err != nil {
		return domain.Wrap(err, "isolation.cgroup", "write "+name)
	}
	return nil
}

func sanitizeCgroupName(value string) string {
	if value == "" {
		return "sandbox"
	}
	var builder strings.Builder
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '_' || char == '-' || char == '.' {
			builder.WriteRune(char)
		} else {
			builder.WriteByte('_')
		}
		if builder.Len() >= 128 {
			break
		}
	}
	if builder.Len() == 0 {
		return "sandbox"
	}
	return builder.String()
}
