package isolation

import "time"

// LinuxOptions configures the Linux isolation implementation. Zero values are
// replaced with safe defaults by NewLinux.
type LinuxOptions struct {
	// PreparedTTL bounds how long a prepared launch may wait before Launch.
	PreparedTTL time.Duration
	// Self overrides the executable re-executed for the restricted child.
	// Empty means os.Executable().
	Self string
	// CgroupRoot overrides the cgroup v2 mount. Empty means /sys/fs/cgroup.
	CgroupRoot string
	// Clock is injectable for tests.
	Clock func() time.Time
}
