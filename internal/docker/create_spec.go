package docker

import "github.com/KLORQ/quarshell-driver/driver"

// sandboxCreateSpec is the Docker package's internal create input. It contains
// only Docker implementation data and is not part of orchestration's API.
type sandboxCreateSpec struct {
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
