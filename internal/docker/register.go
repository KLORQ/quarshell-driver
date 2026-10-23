package docker

import (
	"fmt"

	"github.com/KLORQ/quarshell-driver/driver"
	"github.com/moby/moby/client"
)

func init() {
	if err := driver.RegisterBackendWithConfigAndInfoAndValidator("docker", StaticBackendInfo("docker"), func(values map[string]any) (driver.Backend, error) {
		return openConfiguredDriver(values)
	}, func(values map[string]any) error {
		_, _, err := ConfigFromMap(values)
		return err
	}); err != nil {
		panic(err)
	}
}

func openConfiguredDriver(values map[string]any) (*Driver, error) {
	cfg, socket, err := ConfigFromMap(values)
	if err != nil {
		return nil, err
	}
	var cli *client.Client
	if socket == "" {
		cli, err = client.New(client.FromEnv)
	} else {
		cli, err = client.New(client.FromEnv, client.WithHost(socket))
	}
	if err != nil {
		return nil, fmt.Errorf("docker driver: %w", err)
	}
	return NewFromClientWithRuntimeConfig(cli, cfg), nil
}
