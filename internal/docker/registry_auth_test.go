package docker

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moby/moby/api/pkg/authconfig"
)

func TestDockerRegistryAuthUsesOnlyMatchingRegistry(t *testing.T) {
	configDir := t.TempDir()
	secret := base64.StdEncoding.EncodeToString([]byte("robot:registry-secret"))
	body := []byte(`{"auths":{"registry.example.com":{"auth":"` + secret + `"}}}`)
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	encoded, err := dockerRegistryAuthFromDir("registry.example.com/team/image:latest", configDir)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := authconfig.Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Username != "robot" || decoded.Password != "registry-secret" || decoded.ServerAddress != "registry.example.com" {
		t.Fatalf("unexpected registry auth: user=%q server=%q", decoded.Username, decoded.ServerAddress)
	}
	missing, err := dockerRegistryAuthFromDir("other.example.com/team/image:latest", configDir)
	if err != nil || missing != "" {
		t.Fatalf("unconfigured registry auth=%q error=%v", missing, err)
	}
}

func TestDockerRegistryAuthErrorsDoNotExposeCredentialMaterial(t *testing.T) {
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"secret-sentinel":`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := dockerRegistryAuthFromDir("registry.example.com/team/image:latest", configDir)
	if err == nil {
		t.Fatal("expected malformed credential error")
	}
	if strings.Contains(err.Error(), "secret-sentinel") {
		t.Fatalf("credential material leaked in error: %v", err)
	}
}
