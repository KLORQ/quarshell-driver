// Package isolation owns the Linux launcher, broker, identity verification, and
// syscall/FD boundary. This file holds the platform-independent admission rules;
// the syscall implementation lives in linux-specific files.
package isolation

import (
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/KLORQ/quarshell-core/domain"
	spi "github.com/KLORQ/quarshell-core/isolation/port"
)

const (
	// MaxCommandArguments bounds the argv passed to one workload.
	MaxCommandArguments = 4096
	// MaxEnvironmentEntries bounds the explicit environment map.
	MaxEnvironmentEntries = 4096
	// MaxArgumentBytes bounds one argv element.
	MaxArgumentBytes = 1 << 20
	// MaxEnvironmentKeyBytes bounds one environment key.
	MaxEnvironmentKeyBytes = 1024
	// MaxEnvironmentValueBytes bounds one environment value.
	MaxEnvironmentValueBytes = 1 << 20
	// MaxFileDescriptors bounds explicitly inherited descriptors.
	MaxFileDescriptors = 256
	// MaxDescriptorFD bounds the target descriptor number.
	MaxDescriptorFD = 1023
	// MaxSupplementaryGroups bounds the explicit group list.
	MaxSupplementaryGroups = 64
)

var deniedEnvironmentKeys = map[string]struct{}{
	"LD_PRELOAD":      {},
	"LD_AUDIT":        {},
	"LD_LIBRARY_PATH": {},
	"LD_DEBUG":        {},
	"BASH_ENV":        {},
	"ENV":             {},
	"GCONV_PATH":      {},
}

// ValidateLaunchSpec rejects untrusted or ambiguous launch data and returns a
// deep copy safe to hand to the launcher. It does not apply restrictions; it
// only proves that the request is well formed and auditable.
func ValidateLaunchSpec(spec spi.LaunchSpec) (spi.LaunchSpec, error) {
	if !spec.Instance.Valid() {
		return spec, domain.Invalidf("isolation.admit", "instance reference is invalid")
	}
	if len(spec.Command) == 0 {
		return spec, domain.Invalidf("isolation.admit", "command is required")
	}
	if len(spec.Command) > MaxCommandArguments {
		return spec, domain.Invalidf("isolation.admit", "command exceeds %d arguments", MaxCommandArguments)
	}
	if err := validateAbsoluteCleanPath("workdir", spec.Workdir); err != nil {
		return spec, err
	}
	if spec.UID == 0 {
		return spec, domain.Invalidf("isolation.admit", "workload must not run as root")
	}
	if spec.GID == 0 {
		return spec, domain.Invalidf("isolation.admit", "workload group must not be root")
	}
	if strings.TrimSpace(spec.PolicyHash) == "" {
		return spec, domain.Invalidf("isolation.admit", "policy hash is required")
	}
	if err := validateGroups(spec.Groups); err != nil {
		return spec, err
	}
	if err := validateDescriptors(spec.Descriptors); err != nil {
		return spec, err
	}
	if err := validateRequirements(spec.Requirements); err != nil {
		return spec, err
	}
	command := make([]string, len(spec.Command))
	for index, argument := range spec.Command {
		if err := validateString("command argument", argument, MaxArgumentBytes); err != nil {
			return spec, err
		}
		if index == 0 && !strings.HasPrefix(argument, "/") {
			return spec, domain.Invalidf("isolation.admit", "command executable must be absolute")
		}
		command[index] = argument
	}
	environment, err := sanitizeEnvironment(spec.Environment)
	if err != nil {
		return spec, err
	}
	spec.Command = command
	spec.Environment = environment
	spec.Groups = append([]uint32(nil), spec.Groups...)
	spec.Descriptors = append([]spi.Descriptor(nil), spec.Descriptors...)
	spec.Requirements.Filesystem.Read = append([]string(nil), spec.Requirements.Filesystem.Read...)
	spec.Requirements.Filesystem.Write = append([]string(nil), spec.Requirements.Filesystem.Write...)
	spec.Requirements.Filesystem.Exec = append([]string(nil), spec.Requirements.Filesystem.Exec...)
	spec.Requirements.Network.Endpoints = append([]string(nil), spec.Requirements.Network.Endpoints...)
	spec.Requirements.Process.Capabilities = append([]string(nil), spec.Requirements.Process.Capabilities...)
	return spec, nil
}

func validateRequirements(requirements spi.SecurityRequirements) error {
	for _, field := range []struct {
		name  string
		paths []string
	}{
		{"filesystem.read", requirements.Filesystem.Read},
		{"filesystem.write", requirements.Filesystem.Write},
		{"filesystem.exec", requirements.Filesystem.Exec},
	} {
		for _, value := range field.paths {
			if err := validateAbsoluteCleanPath(field.name, value); err != nil {
				return err
			}
		}
	}
	for _, endpoint := range requirements.Network.Endpoints {
		if err := validateString("network endpoint", endpoint, MaxEnvironmentValueBytes); err != nil {
			return err
		}
	}
	for _, capability := range requirements.Process.Capabilities {
		if strings.TrimSpace(capability) == "" {
			return domain.Invalidf("isolation.admit", "process capability must not be empty")
		}
	}
	switch requirements.Process.SeccompProfile {
	case "", "baseline":
	default:
		return domain.Invalidf("isolation.admit", "unsupported seccomp profile")
	}
	if requirements.Process.PidsLimit < 0 || requirements.Process.MemoryBytes < 0 ||
		requirements.Process.CPUQuotaMicros < 0 || requirements.Process.CPUPeriodMicros < 0 {
		return domain.Invalidf("isolation.admit", "resource limits must not be negative")
	}
	return nil
}

func validateGroups(groups []uint32) error {
	if len(groups) > MaxSupplementaryGroups {
		return domain.Invalidf("isolation.admit", "too many supplementary groups")
	}
	seen := make(map[uint32]struct{}, len(groups))
	for _, group := range groups {
		if _, duplicate := seen[group]; duplicate {
			return domain.Invalidf("isolation.admit", "duplicate supplementary group %d", group)
		}
		seen[group] = struct{}{}
	}
	return nil
}

func validateDescriptors(descriptors []spi.Descriptor) error {
	if len(descriptors) > MaxFileDescriptors {
		return domain.Invalidf("isolation.admit", "too many descriptors")
	}
	seen := make(map[int]struct{}, len(descriptors))
	for _, descriptor := range descriptors {
		if descriptor.FD < 3 || descriptor.FD > MaxDescriptorFD {
			return domain.Invalidf("isolation.admit", "descriptor %d is outside the allowed range", descriptor.FD)
		}
		if err := validateAbsoluteCleanPath("descriptor source", descriptor.Source); err != nil {
			return err
		}
		if _, duplicate := seen[descriptor.FD]; duplicate {
			return domain.Invalidf("isolation.admit", "duplicate descriptor %d", descriptor.FD)
		}
		seen[descriptor.FD] = struct{}{}
	}
	return nil
}

func sanitizeEnvironment(environment map[string]string) (map[string]string, error) {
	if len(environment) > MaxEnvironmentEntries {
		return nil, domain.Invalidf("isolation.admit", "environment exceeds %d entries", MaxEnvironmentEntries)
	}
	result := make(map[string]string, len(environment))
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := environment[key]
		if key == "" {
			return nil, domain.Invalidf("isolation.admit", "environment key must not be empty")
		}
		if strings.ContainsAny(key, "=\x00") || !utf8.ValidString(key) {
			return nil, domain.Invalidf("isolation.admit", "environment key is invalid")
		}
		if len(key) > MaxEnvironmentKeyBytes {
			return nil, domain.Invalidf("isolation.admit", "environment key exceeds %d bytes", MaxEnvironmentKeyBytes)
		}
		if _, denied := deniedEnvironmentKeys[strings.ToUpper(key)]; denied {
			return nil, domain.Invalidf("isolation.admit", "environment key %q is denied", key)
		}
		if err := validateString("environment value", value, MaxEnvironmentValueBytes); err != nil {
			return nil, err
		}
		result[key] = value
	}
	return result, nil
}

func validateAbsoluteCleanPath(field, value string) error {
	if value == "" {
		return domain.Invalidf("isolation.admit", "%s is required", field)
	}
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return domain.Invalidf("isolation.admit", "%s is invalid", field)
	}
	if !strings.HasPrefix(value, "/") {
		return domain.Invalidf("isolation.admit", "%s must be absolute", field)
	}
	if path.Clean(value) != value {
		return domain.Invalidf("isolation.admit", "%s must be clean", field)
	}
	return nil
}

func validateString(field, value string, max int) error {
	if strings.ContainsRune(value, 0) || !utf8.ValidString(value) {
		return domain.Invalidf("isolation.admit", "%s is invalid", field)
	}
	if len(value) > max {
		return domain.Invalidf("isolation.admit", "%s exceeds %d bytes", field, max)
	}
	return nil
}
