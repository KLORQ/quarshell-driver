package docker_test

import (
	"encoding/json"

	"github.com/KLORQ/quarshell-driver/driver"
)

// sandboxFixtureSpec keeps the existing Docker/Podman lifecycle fixture inputs
// local to tests while the public flat driver API is removed.
type sandboxFixtureSpec struct {
	Name                     string
	Image                    string
	Workspace                string
	GatewayID                string
	Command                  []string
	TTY                      bool
	MainProcessRestartPolicy string
	Env                      []string
	IKnow                    bool
	ProxyBin                 string
	PolicyPath               string
	ProxyPort                int
	ProxyEnv                 []string
	InitBin                  string
	NoHarden                 bool
	SupervisorBin            string
	DisplayMode              string
	DisplayPort              int
	DisplayPassword          string
	Labels                   map[string]string
	ExtraHosts               []string
	GatewayURL               string
	PersistVolume            bool
	EnableSSH                bool
	SSHBin                   string
	GPU                      bool
	GPUCount                 int
	CDIDevices               []string
	CPU                      float64
	MemoryBytes              int64
	PidsLimit                int64
	ProxyImage               string
	PublishPorts             []driver.PortPublish
	DriverConfigJSON         string
}

func backendRequestFromSpec(spec sandboxFixtureSpec) driver.BackendCreateRequest {
	request := driver.BackendCreateRequest{
		Name: spec.Name,
		Workspace: driver.WorkspaceTarget{
			Kind: driver.WorkspaceHostPath, HostPath: spec.Workspace, Persist: spec.PersistVolume,
		},
		Workload: driver.WorkloadSpec{
			Image: spec.Image, Command: spec.Command, TTY: spec.TTY,
			RestartPolicy: spec.MainProcessRestartPolicy, Environment: spec.Env,
		},
		Resources: driver.ResourceLimits{
			CPUCores: spec.CPU, MemoryBytes: spec.MemoryBytes, PIDs: spec.PidsLimit,
			GPU: spec.GPU, GPUCount: spec.GPUCount, CDIDevices: spec.CDIDevices,
		},
		Network: driver.NetworkSpec{
			GatewayID: spec.GatewayID, GatewayURL: spec.GatewayURL,
			ExtraHosts: spec.ExtraHosts, PortPublish: spec.PublishPorts,
		},
		Runtime: driver.RuntimeAssets{
			InitBinary: spec.InitBin, SupervisorBinary: spec.SupervisorBin, NoHarden: spec.NoHarden,
			EnableSSH: spec.EnableSSH, SSHBinary: spec.SSHBin,
			DisplayMode: spec.DisplayMode, DisplayPort: spec.DisplayPort, DisplayPassword: spec.DisplayPassword,
		},
		Labels: spec.Labels, AllowUnsafeMount: spec.IKnow,
	}
	if spec.ProxyBin != "" || spec.ProxyImage != "" || spec.PolicyPath != "" || spec.ProxyPort != 0 || len(spec.ProxyEnv) > 0 {
		request.Network.Proxy = &driver.ProxySpec{
			Binary: spec.ProxyBin, Image: spec.ProxyImage, PolicyPath: spec.PolicyPath,
			Port: spec.ProxyPort, Environment: spec.ProxyEnv,
		}
	}
	if spec.DriverConfigJSON != "" {
		var config map[string]any
		if json.Unmarshal([]byte(spec.DriverConfigJSON), &config) == nil {
			request.Config = config
		}
	}
	return request
}
