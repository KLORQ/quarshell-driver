package kubernetes

import "github.com/KLORQ/quarshell-driver/driver"

func init() {
	if err := driver.RegisterBackendWithConfigAndInfoAndValidator("kubernetes", (&Driver{}).backendInfo(), openBackendWithConfig, func(values map[string]any) error {
		config := make(map[string]any, len(values)+1)
		for key, value := range values {
			config[key] = value
		}
		if _, exists := config[internalGatewayIDConfigKey]; !exists {
			config[internalGatewayIDConfigKey] = "preflight"
		}
		_, err := BackendConfigFromMap(config)
		return err
	}); err != nil {
		panic(err)
	}
}
