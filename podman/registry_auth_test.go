package podman

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moby/moby/api/pkg/authconfig"
)

func TestPodmanRegistryAuthFileSelectsMatchingRegistry(t *testing.T) {
	authFile := filepath.Join(t.TempDir(), "auth.json")
	secret := base64.StdEncoding.EncodeToString([]byte("robot:registry-secret"))
	body := []byte(`{"auths":{"https://registry.example.com/v1":{"auth":"` + secret + `"}}}`)
	if err := os.WriteFile(authFile, body, 0o600); err != nil {
		t.Fatal(err)
	}
	encoded, err := podmanRegistryAuth("registry.example.com/team/image:latest", authFile)
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
}

func TestPodmanRegistryAuthRejectsUnsafeFilesWithoutLeakingSecrets(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	missing := filepath.Join(t.TempDir(), "missing-auth.json")
	if _, err := podmanRegistryAuth("registry.example.com/image:latest", missing); err == nil {
		t.Fatal("expected missing authfile rejection")
	}
	authFile := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(authFile, []byte(`{"auths":{"registry.example.com":{"auth":"secret-sentinel"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := podmanRegistryAuth("registry.example.com/image:latest", authFile)
	if err == nil {
		t.Fatal("expected malformed auth rejection")
	}
	if strings.Contains(err.Error(), "secret-sentinel") {
		t.Fatalf("credential material leaked in error: %v", err)
	}
}

func TestFindPodmanAuthFilePrecedence(t *testing.T) {
	configured := filepath.Join(t.TempDir(), "configured.json")
	environment := filepath.Join(t.TempDir(), "environment.json")
	t.Setenv("REGISTRY_AUTH_FILE", environment)
	if path, err := findPodmanAuthFile(configured); err != nil || path != configured {
		t.Fatalf("configured precedence path=%q error=%v", path, err)
	}
	if path, err := findPodmanAuthFile(""); err != nil || path != environment {
		t.Fatalf("environment precedence path=%q error=%v", path, err)
	}
}
