package isolation

import (
	"strings"
	"testing"

	"github.com/KLORQ/quarshell-core/domain"
	spi "github.com/KLORQ/quarshell-core/isolation/port"
)

func validLaunchSpec() spi.LaunchSpec {
	return spi.LaunchSpec{
		Instance: domain.InstanceRef{
			SandboxRef: domain.SandboxRef{Workspace: "ws", Sandbox: "sb"},
			Instance:   "inst",
			Generation: 1,
		},
		Command:     []string{"/usr/bin/env", "true"},
		Environment: map[string]string{"PATH": "/usr/bin"},
		Workdir:     "/workspace",
		UID:         1000,
		GID:         1000,
		Groups:      []uint32{1000, 1001},
		Descriptors: []spi.Descriptor{{FD: 3, Source: "/var/log/app.log"}},
		PolicyHash:  strings.Repeat("a", 64),
		Requirements: spi.SecurityRequirements{
			Filesystem: spi.FilesystemPolicy{Read: []string{"/usr"}, Write: []string{"/workspace"}, Exec: []string{"/usr/bin"}},
			Network:    spi.NetworkPolicy{Mode: "allowlist", Endpoints: []string{"api.example.com:443"}},
			Process:    spi.ProcessPolicy{NoNewPrivs: true, Capabilities: []string{"net_bind_service"}},
		},
	}
}

func TestValidateLaunchSpecAcceptsValidSpec(t *testing.T) {
	spec, err := ValidateLaunchSpec(validLaunchSpec())
	if err != nil {
		t.Fatalf("ValidateLaunchSpec: %v", err)
	}
	if spec.Command[0] != "/usr/bin/env" || spec.Environment["PATH"] != "/usr/bin" {
		t.Fatalf("valid spec changed: %+v", spec)
	}
}

func TestValidateLaunchSpecRejectsInvalidSpec(t *testing.T) {
	cases := map[string]func(*spi.LaunchSpec){
		"missing instance":           func(s *spi.LaunchSpec) { s.Instance = domain.InstanceRef{} },
		"empty command":              func(s *spi.LaunchSpec) { s.Command = nil },
		"relative executable":        func(s *spi.LaunchSpec) { s.Command[0] = "env" },
		"nul argument":               func(s *spi.LaunchSpec) { s.Command[1] = "a\x00b" },
		"relative workdir":           func(s *spi.LaunchSpec) { s.Workdir = "workspace" },
		"unclean workdir":            func(s *spi.LaunchSpec) { s.Workdir = "/workspace/../etc" },
		"root uid":                   func(s *spi.LaunchSpec) { s.UID = 0 },
		"root gid":                   func(s *spi.LaunchSpec) { s.GID = 0 },
		"duplicate group":            func(s *spi.LaunchSpec) { s.Groups = []uint32{1000, 1000} },
		"descriptor collision":       func(s *spi.LaunchSpec) { s.Descriptors[0].FD = 1 },
		"duplicate descriptor":       func(s *spi.LaunchSpec) { s.Descriptors = append(s.Descriptors, spi.Descriptor{FD: 3, Source: "dup"}) },
		"empty descriptor source":    func(s *spi.LaunchSpec) { s.Descriptors[0].Source = "" },
		"relative descriptor source": func(s *spi.LaunchSpec) { s.Descriptors[0].Source = "log" },
		"descriptor fd too large":    func(s *spi.LaunchSpec) { s.Descriptors[0].FD = 2048 },
		"empty policy hash":          func(s *spi.LaunchSpec) { s.PolicyHash = "" },
		"denied ld_preload":          func(s *spi.LaunchSpec) { s.Environment["LD_PRELOAD"] = "/tmp/x.so" },
		"denied bash_env":            func(s *spi.LaunchSpec) { s.Environment["BASH_ENV"] = "/tmp/x" },
		"environment key equals":     func(s *spi.LaunchSpec) { s.Environment["A=B"] = "x" },
		"nul environment value":      func(s *spi.LaunchSpec) { s.Environment["PATH"] = "/usr\x00/bin" },
		"relative read grant":        func(s *spi.LaunchSpec) { s.Requirements.Filesystem.Read = []string{"usr"} },
		"unclean write grant":        func(s *spi.LaunchSpec) { s.Requirements.Filesystem.Write = []string{"/tmp/../etc"} },
		"empty capability":           func(s *spi.LaunchSpec) { s.Requirements.Process.Capabilities = []string{" "} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			spec := validLaunchSpec()
			mutate(&spec)
			if _, err := ValidateLaunchSpec(spec); err == nil {
				t.Fatalf("invalid spec accepted")
			}
		})
	}
}

func TestValidateLaunchSpecReturnsDeepCopy(t *testing.T) {
	spec := validLaunchSpec()
	sanitized, err := ValidateLaunchSpec(spec)
	if err != nil {
		t.Fatalf("ValidateLaunchSpec: %v", err)
	}
	sanitized.Command[0] = "/bin/sh"
	sanitized.Groups[0] = 1
	sanitized.Descriptors[0].FD = 4
	sanitized.Environment["PATH"] = "/bin"
	sanitized.Requirements.Filesystem.Read[0] = "/etc"
	sanitized.Requirements.Network.Endpoints[0] = "evil.example.com:443"
	if spec.Command[0] != "/usr/bin/env" || spec.Groups[0] != 1000 || spec.Descriptors[0].FD != 3 ||
		spec.Environment["PATH"] != "/usr/bin" || spec.Requirements.Filesystem.Read[0] != "/usr" ||
		spec.Requirements.Network.Endpoints[0] != "api.example.com:443" {
		t.Fatalf("admission result aliases the input: %+v", spec)
	}
}
