package docker

import (
	"fmt"
	"strings"

	"github.com/distribution/reference"
	"github.com/docker/cli/cli/config"
	"github.com/docker/cli/cli/config/credentials"
	"github.com/moby/moby/api/pkg/authconfig"
	registrytypes "github.com/moby/moby/api/types/registry"
)

// registryAuth resolves the caller's Docker CLI credentials for one registry
// and returns the daemon-scoped X-Registry-Auth value. Credentials are read
// only for the pull request and never enter sandbox configuration or logs.
func dockerRegistryAuth(imageRef string) (string, error) {
	return dockerRegistryAuthFromDir(imageRef, config.Dir())
}

func dockerRegistryAuthFromDir(imageRef, configDir string) (string, error) {
	named, err := reference.ParseNormalizedNamed(strings.TrimSpace(imageRef))
	if err != nil {
		return "", fmt.Errorf("docker registry credentials: invalid image reference")
	}
	registry := reference.Domain(named)
	key := dockerAuthKey(registry)

	cfg, err := config.Load(configDir)
	if err != nil {
		return "", fmt.Errorf("docker registry credential config is unavailable")
	}
	if !cfg.ContainsAuth() {
		cfg.CredentialsStore = credentials.DetectDefaultStore(cfg.CredentialsStore)
	}
	credential, err := cfg.GetAuthConfig(key)
	if err != nil {
		// Credential-helper errors can include helper output; return a stable
		// message that cannot disclose credentials or helper diagnostics.
		return "", fmt.Errorf("docker registry credentials unavailable for %s", registry)
	}
	if credential.Username == "" && credential.Password == "" && credential.Auth == "" && credential.IdentityToken == "" && credential.RegistryToken == "" {
		return "", nil
	}
	encoded, err := authconfig.Encode(registrytypes.AuthConfig{
		Username:      credential.Username,
		Password:      credential.Password,
		Auth:          credential.Auth,
		ServerAddress: key,
		IdentityToken: credential.IdentityToken,
		RegistryToken: credential.RegistryToken,
	})
	if err != nil {
		return "", fmt.Errorf("docker registry credentials could not be encoded for %s", registry)
	}
	return encoded, nil
}

// DockerRegistryAuthForPodman provides the shared Docker CLI credential
// fallback for Podman when no Podman authfile contains this registry.
func DockerRegistryAuthForPodman(imageRef string) (string, error) {
	return dockerRegistryAuth(imageRef)
}

func dockerAuthKey(registry string) string {
	if registry == "docker.io" {
		return "https://index.docker.io/v1/"
	}
	return registry
}
