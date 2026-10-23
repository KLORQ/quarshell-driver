package podman_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/KLORQ/quarshell-core/resourceadmission"
	podman "github.com/KLORQ/quarshell-driver/podman"
	"github.com/KLORQ/quarshell-driver/tests/internal/testenv"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

func TestPodmanLegacyRejectsUnsupportedNetworkBeforeWorkloadStart(t *testing.T) {
	if os.Getenv("QUARSHELL_PODMAN_LEGACY_NEGATIVE") != "1" {
		t.Skip("set QUARSHELL_PODMAN_LEGACY_NEGATIVE=1 to run the Podman 5.8.7 negative compatibility lane")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	service, endpoint, workspace := testenv.RunPodmanDaemon(ctx, t)
	t.Setenv("DOCKER_HOST", endpoint)
	code, versionStream, err := service.Exec(ctx, []string{"podman", "--version"})
	if err != nil {
		t.Fatalf("inspect legacy Podman version: %v", err)
	}
	var versionOutput bytes.Buffer
	if _, err := stdcopy.StdCopy(&versionOutput, io.Discard, versionStream); err != nil {
		t.Fatalf("read legacy Podman version: %v", err)
	}
	versionBytes := versionOutput.Bytes()
	if code != 0 || strings.TrimSpace(string(versionBytes)) != "podman version 5.8.7" {
		t.Fatalf("legacy Podman version = %q (exit %d), err=%v; want exact 5.8.7", versionBytes, code, err)
	}

	engineClient, err := client.New(client.WithHost(endpoint), client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("create legacy Podman API client: %v", err)
	}
	defer engineClient.Close()
	proxyBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve executable proxy fixture: %v", err)
	}
	policyPath := workspace + "/legacy-negative-policy.yaml"
	if err := os.WriteFile(policyPath, []byte("version: 1\nnetwork_policies: {}\n"), 0o600); err != nil {
		t.Fatalf("write policy fixture: %v", err)
	}
	networkPrefix := "quarshell-podman-legacy-negative"
	name := "legacy-negative-" + time.Now().Format("150405000000000")
	engine, err := podman.NewWithConfig(podman.Config{
		ImagePullPolicy: "missing", NetworkName: networkPrefix, UsernsMode: "host",
		ResourceAdmission: &resourceadmission.Config{Enabled: false},
	})
	if err != nil {
		t.Fatalf("create Podman driver: %v", err)
	}
	defer engine.Close()
	handle, createErr := engine.Create(ctx, backendRequestFromSpec(sandboxFixtureSpec{
		Name: name, Image: testenv.AlpineImage, Workspace: workspace,
		Command: []string{"sh", "-c", "sleep 30"}, NoHarden: true,
		ProxyBin: proxyBinary, ProxyImage: testenv.AlpineImage, PolicyPath: policyPath,
	}))
	if createErr == nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		if deleteErr := engine.Delete(cleanupCtx, handle.ID); deleteErr != nil {
			t.Errorf("clean up unexpected legacy workload: %v", deleteErr)
		}
		t.Fatal("Podman 5.8.7 unexpectedly created a proxy-backed workload without blackhole host-gateway isolation")
	}
	if !strings.Contains(strings.ToLower(createErr.Error()), "host-gateway isolation requires podman 6.0") {
		t.Fatalf("legacy Podman create error = %v; want explicit fail-closed blackhole capability error", createErr)
	}

	containerName := "quarshell-" + name
	containers, err := engineClient.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		t.Fatalf("list containers after isolation rejection: %v", err)
	}
	for _, container := range containers.Items {
		for _, listedName := range container.Names {
			if strings.TrimPrefix(listedName, "/") == containerName {
				t.Fatalf("workload container %q exists after isolation rejection", containerName)
			}
		}
	}
	networkName := networkPrefix + "-" + name
	networks, err := engineClient.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		t.Fatalf("list networks after isolation rejection: %v", err)
	}
	for _, network := range networks.Items {
		if network.Name == networkName {
			t.Fatalf("network %q exists after unsupported blackhole create", networkName)
		}
	}
	t.Logf("Podman 5.8.7 rejected proxy-backed workload before network or workload creation: %v", createErr)
}
