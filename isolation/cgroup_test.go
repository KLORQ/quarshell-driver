package isolation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeCgroupRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "cgroup.controllers"), []byte("cpuset cpu io memory pids\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return root
}

func TestCgroupCreateLimitsAndAddProcess(t *testing.T) {
	root := fakeCgroupRoot(t)
	cgroup, err := NewCgroup(root, "quarshell-test", CgroupLimits{
		PidsMax:         64,
		MemoryMaxBytes:  512 << 20,
		CPUQuotaMicros:  50000,
		CPUPeriodMicros: 100000,
	})
	if err != nil {
		t.Fatalf("NewCgroup: %v", err)
	}
	if err := cgroup.Create(); err != nil {
		t.Fatalf("Create: %v", err)
	}
	checks := map[string]string{
		"pids.max":   "64\n",
		"memory.max": "536870912\n",
		"cpu.max":    "50000 100000\n",
	}
	for file, want := range checks {
		data, err := os.ReadFile(filepath.Join(cgroup.Path(), file))
		if err != nil {
			t.Fatalf("ReadFile %s: %v", file, err)
		}
		if string(data) != want {
			t.Fatalf("%s = %q, want %q", file, data, want)
		}
	}
	if err := os.WriteFile(filepath.Join(cgroup.Path(), "cgroup.procs"), nil, 0o644); err != nil {
		t.Fatalf("WriteFile procs: %v", err)
	}
	if err := cgroup.AddProcess(1234); err != nil {
		t.Fatalf("AddProcess: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(cgroup.Path(), "cgroup.procs"))
	if err != nil {
		t.Fatalf("ReadFile procs: %v", err)
	}
	if strings.TrimSpace(string(data)) != "1234" {
		t.Fatalf("cgroup.procs = %q", data)
	}
}

func TestCgroupRemoveEmpty(t *testing.T) {
	root := fakeCgroupRoot(t)
	cgroup, err := NewCgroup(root, "empty", CgroupLimits{})
	if err != nil {
		t.Fatalf("NewCgroup: %v", err)
	}
	if err := cgroup.Create(); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := cgroup.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(cgroup.Path()); !os.IsNotExist(err) {
		t.Fatalf("cgroup still exists")
	}
	if err := cgroup.Remove(); err != nil {
		t.Fatalf("second Remove: %v", err)
	}
}

func TestCgroupRejectsInvalidRequests(t *testing.T) {
	root := fakeCgroupRoot(t)
	for name, request := range map[string]struct {
		root   string
		name   string
		limits CgroupLimits
	}{
		"relative root": {root: "cgroup", name: "x"},
		"bad name":      {root: root, name: "../escape"},
		"empty name":    {root: root, name: ""},
		"negative":      {root: root, name: "x", limits: CgroupLimits{PidsMax: -1}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewCgroup(request.root, request.name, request.limits); err == nil {
				t.Fatalf("invalid cgroup accepted")
			}
		})
	}
	if _, err := NewCgroup(filepath.Join(root, "missing"), "x", CgroupLimits{}); err != nil {
		t.Fatalf("NewCgroup: %v", err)
	}
	cgroup, _ := NewCgroup(filepath.Join(root, "missing"), "x", CgroupLimits{})
	if err := cgroup.Create(); err == nil {
		t.Fatalf("Create accepted a missing cgroup v2 root")
	}
}

func TestSanitizeCgroupName(t *testing.T) {
	cases := map[string]string{
		"":             "sandbox",
		"ws/sb":        "ws_sb",
		"a b":          "a_b",
		"valid.name-1": "valid.name-1",
	}
	for input, want := range cases {
		if got := sanitizeCgroupName(input); got != want {
			t.Fatalf("sanitizeCgroupName(%q) = %q, want %q", input, got, want)
		}
	}
}
