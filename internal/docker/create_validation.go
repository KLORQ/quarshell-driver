package docker

import (
	"context"
	"fmt"

	"github.com/KLORQ/quarshell-core/resourceadmission"
	enginemount "github.com/moby/moby/api/types/mount"
)

type validatedCreate struct {
	cdiDevices         []string
	hasCDIDevices      bool
	mounts             []enginemount.Mount
	binds              []string
	resourceIdentities map[string]string
	configUsed         bool
}

func (d *Driver) validateCreate(ctx context.Context, spec sandboxCreateSpec) (validatedCreate, error) {
	if d == nil || d.cli == nil {
		return validatedCreate{}, fmt.Errorf("docker driver: client not initialized")
	}
	if err := ctx.Err(); err != nil {
		return validatedCreate{}, err
	}
	if err := d.validateAppArmor(ctx); err != nil {
		return validatedCreate{}, err
	}
	if err := ValidateGPURequest(spec); err != nil {
		return validatedCreate{}, fmt.Errorf("docker gpu: %w", err)
	}
	configUsed, err := validateDriverConfigAllowed(spec.DriverConfigJSON, d.runtimeConfig.AllowDriverConfig)
	if err != nil {
		return validatedCreate{}, err
	}
	cdiDevices, hasCDIDevices, mounts, binds, err := parseDockerSandboxDriverConfig(spec.DriverConfigJSON, d.enableBindMounts)
	if err != nil {
		return validatedCreate{}, err
	}
	policy := resourceadmission.Default()
	if configured := d.runtimeConfig.ResourceAdmission; configured != nil {
		policy = *configured
	}
	identities, err := d.validateExternalDriverMounts(ctx, spec, mounts, policy)
	if err != nil {
		return validatedCreate{}, err
	}
	if err := ctx.Err(); err != nil {
		return validatedCreate{}, err
	}
	return validatedCreate{
		cdiDevices: cdiDevices, hasCDIDevices: hasCDIDevices, mounts: mounts,
		binds: binds, resourceIdentities: identities, configUsed: configUsed,
	}, nil
}
