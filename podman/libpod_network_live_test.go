package podman

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestPodmanNativeBlackholeRouteCreateInspectE2E(t *testing.T) {
	if os.Getenv("CAUTEM_PODMAN_NATIVE_NETWORK_E2E") != "1" {
		t.Skip("set CAUTEM_PODMAN_NATIVE_NETWORK_E2E=1 to probe native Podman blackhole route support")
	}
	if os.Getenv("CAUTEM_TESTCONTAINERS") != "1" {
		t.Fatal("CAUTEM_PODMAN_NATIVE_NETWORK_E2E=1 requires CAUTEM_TESTCONTAINERS=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	image := os.Getenv("CAUTEM_TEST_PODMAN_IMAGE")
	if image == "" {
		t.Fatal("CAUTEM_TEST_PODMAN_IMAGE must point to the Podman 6.1.4 fixture")
	}
	service, err := testcontainers.Run(ctx, image,
		testcontainers.WithExposedPorts("18500/tcp"),
		testcontainers.WithEnv(map[string]string{"STORAGE_DRIVER": "vfs"}),
		testcontainers.WithHostConfigModifier(func(config *container.HostConfig) { config.Privileged = true }),
		testcontainers.WithCmd("sh", "-lc", "printf 'root:100000:65536\\n' > /etc/subuid; printf 'root:100000:65536\\n' > /etc/subgid; exec podman system service --time=0 tcp:0.0.0.0:18500"),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("18500/tcp")),
	)
	if err != nil {
		t.Fatalf("start Podman network probe service: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		if err := service.Terminate(cleanupCtx); err != nil {
			t.Errorf("terminate Podman network probe service: %v", err)
		}
	})
	code, versionOutput, err := service.Exec(ctx, []string{"podman", "--version"})
	if err != nil {
		t.Fatalf("inspect Podman network probe version: %v", err)
	}
	versionBytes, err := io.ReadAll(versionOutput)
	if err != nil || code != 0 || strings.TrimSpace(string(versionBytes)) != "podman version 6.1.4" {
		t.Fatalf("Podman network probe version = %q (exit %d), err=%v; want exact 6.1.4", versionBytes, code, err)
	}
	port, err := service.MappedPort(ctx, "18500/tcp")
	if err != nil {
		t.Fatalf("resolve Podman API port: %v", err)
	}
	host, err := service.Host(ctx)
	if err != nil {
		t.Fatalf("resolve Podman API host: %v", err)
	}
	endpoint := fmt.Sprintf("tcp://%s:%s", host, port.Port())
	create, verify := nativeNetworkCallbacks(endpoint)
	name := fmt.Sprintf("cautem-native-blackhole-%d", time.Now().UnixNano())
	if err := create(ctx, name, true, map[string]string{"cautem.test.owner": name}); err != nil {
		t.Fatalf("create native internal network with blackhole gateway route: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if err := deleteNativeNetwork(cleanupCtx, endpoint, name); err != nil {
			t.Errorf("remove native Podman network %q: %v", name, err)
		}
	})
	if err := verify(ctx, name, true); err != nil {
		t.Fatalf("native Podman inspect did not retain required blackhole gateway route: %v", err)
	}
}

func deleteNativeNetwork(ctx context.Context, host, name string) error {
	endpoint, err := nativeEndpoint(host)
	if err != nil {
		return err
	}
	endpoint.Path = path.Join(endpoint.Path, "/v5.0.0/libpod/networks", url.PathEscape(name))
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint.String(), nil)
	if err != nil {
		return err
	}
	httpClient, err := nativeHTTPClient(host, endpoint)
	if err != nil {
		return err
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("send delete request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("delete returned %s", response.Status)
	}
	return nil
}
