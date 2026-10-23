package podman

import (
	"github.com/KLORQ/quarshell-driver/driver"
	"github.com/KLORQ/quarshell-driver/internal/docker"
)

func init() {
	if err := driver.RegisterBackendWithConfigAndInfoAndValidator("podman", docker.StaticBackendInfo("podman"), func(values map[string]any) (driver.Backend, error) {
		cfg, err := ConfigFromMap(values)
		if err != nil {
			return nil, err
		}
		return NewWithConfig(cfg)
	}, func(values map[string]any) error {
		_, err := ConfigFromMap(values)
		return err
	}); err != nil {
		panic(err)
	}
}
