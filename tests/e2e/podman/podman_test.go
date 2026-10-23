package podman_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KLORQ/quarshell-core"
	"github.com/KLORQ/quarshell-core/resourceadmission"
	"github.com/KLORQ/quarshell-driver/driver"
	podman "github.com/KLORQ/quarshell-driver/podman"
	"github.com/KLORQ/quarshell-driver/tests/internal/testenv"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// TestPodmanEngineE2E exercises the same Engine API path used by the Docker
// backend, against a real Podman service. Set QUARSHELL_PODMAN_E2E=1 and
// DOCKER_HOST to an isolated Podman compatibility API endpoint to enable it.
func TestPodmanEngineE2E(t *testing.T) {
	if os.Getenv("QUARSHELL_PODMAN_E2E") != "1" {
		t.Skip("set QUARSHELL_PODMAN_E2E=1 to run against a disposable Podman service")
	}
	workspace := strings.TrimSpace(os.Getenv("QUARSHELL_PODMAN_WORKSPACE"))
	if workspace == "" {
		t.Fatal("QUARSHELL_PODMAN_WORKSPACE must name a directory visible to both the test process and Podman service")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	image := strings.TrimSpace(os.Getenv("QUARSHELL_E2E_SANDBOX_IMAGE"))
	if image == "" {
		image = testenv.AlpineImage
	}
	helperDir := strings.TrimSpace(os.Getenv("QUARSHELL_HELPERS_DIR"))
	proxyBin := filepath.Join(helperDir, "quarshell")
	proxyImage := strings.TrimSpace(os.Getenv("QUARSHELL_PROXY_IMAGE"))
	policyPath := filepath.Join(workspace, "podman-driver-deny-policy.yaml")
	if helperDir == "" || proxyImage == "" {
		t.Fatal("QUARSHELL_HELPERS_DIR and QUARSHELL_PROXY_IMAGE are required for policy E2E")
	}
	if err := os.WriteFile(policyPath, []byte("version: 1\nnetwork_policies: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The Podman daemon runs in a disposable Docker container; use a host path
	// shared with that container rather than this test process's temp directory.
	bindSource := filepath.Join(workspace, "driver-config-bind")
	if err := os.MkdirAll(bindSource, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bindSource, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bindSource, "marker"), []byte("bind-ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	engine, err := podman.NewWithConfig(podman.Config{
		ImagePullPolicy:         "missing",
		HealthCheckIntervalSecs: 30,
		UsernsMode:              "host",
		EnableBindMounts:        true,
		AllowDriverConfig:       true,
		// E2E explicitly exercises caller mounts, including a bind source.
		ResourceAdmission: &resourceadmission.Config{Enabled: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	engineClient, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer engineClient.Close()
	partialName := "podman-partial-failure"
	if _, err := engine.Create(ctx, backendRequestFromSpec(sandboxFixtureSpec{
		Name: partialName, Image: image, Workspace: workspace,
		ProxyBin: filepath.Join(workspace, "missing-proxy-binary"), ProxyImage: proxyImage, PolicyPath: policyPath,
	})); err == nil {
		t.Fatal("partial create with missing proxy binary unexpectedly succeeded")
	}
	if _, err := engineClient.NetworkInspect(ctx, "quarshell-net-"+partialName, client.NetworkInspectOptions{}); err == nil {
		t.Fatal("partial Podman proxy create left its isolated network behind")
	}
	if _, err := engineClient.VolumeInspect(ctx, "quarshell-ca-"+partialName, client.VolumeInspectOptions{}); err == nil {
		t.Fatal("partial Podman proxy create left its CA volume behind")
	}
	runID := time.Now().UnixNano()
	name := fmt.Sprintf("podman-e2e-%d", runID)
	volumeName := fmt.Sprintf("quarshell-podman-e2e-cache-%d", runID)
	if _, err := engineClient.VolumeCreate(ctx, client.VolumeCreateOptions{Name: volumeName}); err != nil {
		t.Fatalf("create caller volume fixture: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if _, err := engineClient.VolumeRemove(cleanupCtx, volumeName, client.VolumeRemoveOptions{Force: true}); err != nil {
			t.Errorf("remove E2E named volume: %v", err)
		}
	}()
	driverConfig := fmt.Sprintf(`{"mounts":[{"type":"bind","source":%s,"target":"/openshell-bind","read_only":true,"selinux_label":"shared"},{"type":"volume","source":%s,"target":"/openshell-volume","read_only":false},{"type":"tmpfs","target":"/openshell-tmpfs","read_only":false,"size_bytes":4096,"mode":448}]}`, strconv.Quote(bindSource), strconv.Quote(volumeName))
	h, err := engine.Create(ctx, backendRequestFromSpec(sandboxFixtureSpec{
		Name: name, Image: image, Workspace: workspace,
		Command:  []string{"/bin/sh", "-c", "echo quarshell-podman-e2e-ready; sleep 300"},
		NoHarden: true, PersistVolume: true, CPU: 0.5, MemoryBytes: 256 << 20, PidsLimit: 128,
		ProxyBin: proxyBin, ProxyImage: proxyImage, PolicyPath: policyPath,
		DriverConfigJSON: driverConfig,
	}))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "host-gateway isolation requires podman 6.0") {
			t.Skipf("proxy-backed Podman sandboxes require verified blackhole routes: %v", err)
		}
		t.Fatalf("create sandbox through Podman Engine API: %v", err)
	}
	policyNetwork := fmt.Sprintf("quarshell-podman-policy-e2e-%d", time.Now().UnixNano())
	if _, err := engineClient.NetworkCreate(ctx, policyNetwork, client.NetworkCreateOptions{Driver: "bridge", Internal: true}); err != nil {
		t.Fatalf("create proxy-isolated Podman network: %v", err)
	}
	defer func() {
		_, _ = engineClient.NetworkRemove(context.Background(), policyNetwork, client.NetworkRemoveOptions{})
	}()
	policyNetworkInfo, err := engineClient.NetworkInspect(ctx, policyNetwork, client.NetworkInspectOptions{})
	if err != nil || !policyNetworkInfo.Network.Internal {
		t.Fatalf("Podman proxy policy network inspect internal=%t err=%v; want internal network", policyNetworkInfo.Network.Internal, err)
	}
	deleted := false
	defer func() {
		if deleted {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if err := engine.Delete(cleanupCtx, h.ID); err != nil {
			t.Errorf("delete sandbox: %v", err)
		}
		_, _ = engineClient.VolumeRemove(cleanupCtx, volumeName, client.VolumeRemoveOptions{Force: true})
	}()
	created, err := engineClient.ContainerInspect(ctx, string(h.ID), client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("inspect created sandbox Engine settings: %v", err)
	}
	if created.Container.Config == nil || created.Container.Config.Healthcheck == nil || created.Container.Config.Healthcheck.Interval != 30*time.Second {
		t.Fatalf("Podman healthcheck config=%+v", created.Container.Config)
	}
	if created.Container.HostConfig == nil {
		t.Fatalf("Podman host config effects=%+v", created.Container.HostConfig)
	}
	if created.Container.HostConfig == nil || len(created.Container.HostConfig.Binds) < 3 || created.Container.HostConfig.Tmpfs["/openshell-tmpfs"] == "" {
		t.Fatalf("OpenShell driver_config mounts not applied: %+v", created.Container.HostConfig)
	}
	if !slices.Contains(created.Container.HostConfig.SecurityOpt, "no-new-privileges") {
		t.Fatalf("sandbox security defaults not applied: %+v", created.Container.HostConfig)
	}
	imageInfo, err := engineClient.ImageInspect(ctx, image)
	if err != nil {
		t.Fatalf("inspect pinned image: %v", err)
	}
	if created.Container.Image != imageInfo.ID {
		t.Fatalf("container image ID=%q, inspected image ID=%q; create must pin immutable image identity", created.Container.Image, imageInfo.ID)
	}
	if created.Container.HostConfig.NanoCPUs != 500_000_000 || created.Container.HostConfig.Memory != 256<<20 || created.Container.HostConfig.PidsLimit == nil || *created.Container.HostConfig.PidsLimit != 128 {
		t.Fatalf("Podman override resource limits missing: %+v", created.Container.HostConfig)
	}
	if err := engine.Start(ctx, h.ID); err != nil {
		t.Fatalf("start sandbox: %v", err)
	}
	assertPodmanPlatformExecEvent(t, ctx, engine, h.ID, name)
	probeTarget, closeProbeTarget := startPodmanPolicyProbeTarget(t, ctx, engineClient, image)
	defer closeProbeTarget()
	proxyNetworkName := "quarshell-net-" + name
	proxyNetwork, err := engineClient.NetworkInspect(ctx, proxyNetworkName, client.NetworkInspectOptions{})
	if err != nil || !proxyNetwork.Network.Internal {
		t.Fatalf("actual Podman sandbox network internal=%v err=%v; want Internal=true", proxyNetwork.Network.Internal, err)
	}
	assertPodmanNativeBlackholeGatewayRoute(t, ctx, proxyNetworkName)
	proxyInfo, err := engineClient.ContainerInspect(ctx, "quarshell-proxy-"+name, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("inspect Podman policy proxy sidecar: %v", err)
	}
	hostTarget := ""
	if len(proxyNetwork.Network.IPAM.Config) > 0 && proxyNetwork.Network.IPAM.Config[0].Gateway.IsValid() {
		var closeHostTarget func()
		hostTarget, closeHostTarget = startPodmanHostGatewayProbeTarget(t, ctx, engineClient, image, proxyNetwork.Network.IPAM.Config[0].Gateway.String())
		defer closeHostTarget()
		if result, err := engine.Exec(ctx, h.ID, driver.ExecRequest{Argv: []string{"/usr/local/bin/quarshell-tcp-echo", "probe-tcp", hostTarget}}); err != nil || result.ExitCode != 0 {
			t.Fatalf("Podman sandbox reached host gateway directly at %s: result=%+v err=%v", hostTarget, result, err)
		}
	}
	for _, probe := range [][]string{{"probe-tcp", probeTarget}, {"probe-udp", strings.TrimSuffix(probeTarget, ":17777") + ":17778"}} {
		result, err := engine.Exec(ctx, core.ID(proxyInfo.Container.ID), driver.ExecRequest{Argv: append([]string{"/usr/local/bin/quarshell-tcp-echo"}, probe...)})
		if err != nil || result.ExitCode != 2 {
			t.Fatalf("Podman policy sidecar could not reach control listener via bridge: probe=%v exit=%d err=%v", probe, result.ExitCode, err)
		}
	}
	if hostTarget != "" {
		if _, err := engineClient.ContainerKill(ctx, proxyInfo.Container.ID, client.ContainerKillOptions{Signal: "KILL"}); err != nil {
			t.Fatalf("stop proxy before host-gateway failure probe: %v", err)
		}
		if result, err := engine.Exec(ctx, h.ID, driver.ExecRequest{Argv: []string{"/usr/local/bin/quarshell-tcp-echo", "probe-tcp", hostTarget}}); err != nil || result.ExitCode != 0 {
			t.Fatalf("Podman sandbox reached host gateway after proxy failure at %s: result=%+v err=%v", hostTarget, result, err)
		}
		if err := engine.Start(ctx, h.ID); err != nil {
			t.Fatalf("restart sandbox policy proxy after host-gateway failure probe: %v", err)
		}
	}
	for _, probe := range [][]string{
		{"probe-connect", probeTarget},
		{"probe-tcp", probeTarget},
		{"probe-udp", strings.TrimSuffix(probeTarget, ":17777") + ":17778"},
	} {
		result, err := engine.Exec(ctx, h.ID, driver.ExecRequest{Argv: append([]string{"/usr/local/bin/quarshell-tcp-echo"}, probe...)})
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("network policy probe %v exit=%d err=%v; want denied", probe, result.ExitCode, err)
		}
	}
	info, err := engine.Inspect(ctx, string(h.ID))
	if err != nil {
		t.Fatalf("inspect sandbox: %v", err)
	}
	if !strings.Contains(strings.ToLower(info.Status), "up") && !strings.Contains(strings.ToLower(info.Status), "running") {
		t.Fatalf("sandbox status = %q, want running", info.Status)
	}
	command := `set -eu
test "$(id -u)" = 1000
grep -q '^NoNewPrivs:[[:space:]]*1$' /proc/self/status
test "$(cat /openshell-bind/marker)" = bind-ok
if touch /openshell-bind/write-denied 2>/dev/null; then exit 41; fi
printf persisted > /quarshell/data/persisted
printf volume > /openshell-volume/persisted
test "$(stat -c %a /openshell-tmpfs)" = 700
cap=$(awk '/^CapBnd:/ {print $2}' /proc/self/status)
[ $((0x$cap & 0x2000)) -eq 0 ]`
	result, err := engine.Exec(ctx, h.ID, driver.ExecRequest{Argv: []string{"/bin/sh", "-c", command}})
	if err != nil {
		t.Fatalf("exec in sandbox: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exec exit code = %d, want 0", result.ExitCode)
	}
	if err := engine.Stop(ctx, h.ID); err != nil {
		t.Fatalf("stop sandbox: %v", err)
	}
	if err := engine.Start(ctx, h.ID); err != nil {
		t.Fatalf("restart sandbox: %v", err)
	}
	result, err = engine.Exec(ctx, h.ID, driver.ExecRequest{Argv: []string{"/bin/sh", "-c", `set -eu; test "$(cat /quarshell/data/persisted)" = persisted; test "$(cat /openshell-volume/persisted)" = volume; test "$(stat -c %a /openshell-tmpfs)" = 700`}})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("stop/start persistence and tmpfs reset: result=%+v err=%v", result, err)
	}
	var logs bytes.Buffer
	if err := engine.Logs(ctx, h.ID, false, &logs); err != nil || !strings.Contains(logs.String(), "quarshell-podman-e2e-ready") {
		t.Fatalf("sandbox logs=%q err=%v, expected readiness marker", logs.String(), err)
	}
	if err := engine.Stop(ctx, h.ID); err != nil {
		t.Fatalf("stop restarted sandbox: %v", err)
	}
	if err := engine.Delete(ctx, h.ID); err != nil {
		t.Fatalf("delete sandbox: %v", err)
	}
	deleted = true
	if _, err := engineClient.ContainerInspect(ctx, string(h.ID), client.ContainerInspectOptions{}); err == nil {
		t.Fatalf("container %s still exists after delete", h.ID)
	}
	// Explicit private ID maps use host IDs and are exercised in the rootful
	// lane. The rootless lane uses Podman's allocated maps for auto/no-map.
	if os.Getenv("QUARSHELL_PODMAN_ROOTLESS") != "1" {
		mapped, err := podman.NewWithConfig(podman.Config{
			ImagePullPolicy:         "missing",
			UsernsMode:              "private",
			EnableBindMounts:        true,
			HealthCheckIntervalSecs: 30,
			AllowDriverConfig:       true,
			ResourceAdmission:       &resourceadmission.Config{Enabled: false},
			UIDMap:                  []podman.IDMapping{{ContainerID: 0, HostID: 100000, Size: 65536}},
			GIDMap:                  []podman.IDMapping{{ContainerID: 0, HostID: 100000, Size: 65536}},
		})
		if err != nil {
			t.Fatalf("create mapped Podman driver: %v", err)
		}
		defer mapped.Close()
		mapName := fmt.Sprintf("podman-map-e2e-%d", time.Now().UnixNano())
		mapHandle, err := mapped.Create(ctx, backendRequestFromSpec(sandboxFixtureSpec{Name: mapName, Image: image, Workspace: workspace, Command: []string{"/bin/sh", "-c", "sleep 300"}, NoHarden: true, PidsLimit: 128, DriverConfigJSON: driverConfig}))
		if err != nil {
			t.Fatalf("create sandbox with native Libpod ID maps: %v", err)
		}
		mappedInspect, err := engineClient.ContainerInspect(ctx, string(mapHandle.ID), client.ContainerInspectOptions{})
		if err != nil {
			t.Fatalf("inspect native Libpod sandbox settings: %v", err)
		}
		mappedHost := mappedInspect.Container.HostConfig
		if mappedHost == nil || mappedInspect.Container.Config == nil || mappedInspect.Container.Config.Healthcheck == nil || mappedInspect.Container.Config.Healthcheck.Interval != 30*time.Second {
			t.Fatalf("native Libpod create lost healthcheck settings: config=%+v", mappedInspect.Container.Config)
		}
		if mappedHost.NanoCPUs != int64(2.0*1e9) || mappedHost.Memory != 4_294_967_296 || mappedHost.PidsLimit == nil || *mappedHost.PidsLimit != 128 {
			t.Fatalf("native Libpod create lost CPU/memory/PID settings: %+v", mappedHost)
		}
		if !slices.Contains(mappedHost.SecurityOpt, "no-new-privileges") {
			t.Fatalf("native Libpod create lost no-new-privileges setting: security=%v", mappedHost.SecurityOpt)
		}
		defer func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cleanupCancel()
			if err := mapped.Delete(cleanupCtx, mapHandle.ID); err != nil {
				t.Errorf("delete ID-map sandbox: %v", err)
			}
		}()
		if err := mapped.Start(ctx, mapHandle.ID); err != nil {
			t.Fatalf("start ID-map sandbox: %v", err)
		}
		mapFile, err := os.CreateTemp(workspace, "podman-idmaps-*.txt")
		if err != nil {
			t.Fatalf("create host-side ID map result file: %v", err)
		}
		mapFilePath := mapFile.Name()
		if err := mapFile.Close(); err != nil {
			t.Fatalf("close host-side ID map result file: %v", err)
		}
		defer os.Remove(mapFilePath)
		if err := os.Chmod(mapFilePath, 0o666); err != nil {
			t.Fatalf("make host-side ID map result file writable by mapped UID: %v", err)
		}
		mapFileGuestPath := "/workspace/" + filepath.Base(mapFilePath)
		mapResult, err := mapped.Exec(ctx, mapHandle.ID, driver.ExecRequest{Argv: []string{"/bin/sh", "-c", "cat /proc/self/uid_map /proc/self/gid_map > \"$1\" && test -d /openshell-tmpfs && test -d /openshell-volume && test \"$(cat /openshell-bind/marker)\" = bind-ok && cap=$(awk '/^CapBnd:/ {print $2}' /proc/self/status) && [ $((0x$cap & 0x2000)) -eq 0 ]", "sh", mapFileGuestPath}})
		if err != nil {
			t.Fatalf("read ID maps in sandbox: %v", err)
		}
		if mapResult.ExitCode != 0 {
			t.Fatalf("reading container UID/GID maps failed: exit=%d", mapResult.ExitCode)
		}
		mapContents, err := os.ReadFile(mapFilePath)
		if err != nil {
			t.Fatal(err)
		}
		mapText := string(mapContents)
		if !hasIDMap(mapText, "0", "100000", "65536") || strings.Count(mapText, "65536") < 2 {
			t.Fatalf("container UID/GID maps do not match requested ranges: %q", mapText)
		}
		if err := mapped.Stop(ctx, mapHandle.ID); err != nil {
			t.Fatalf("stop ID-map sandbox: %v", err)
		}
	}

	for _, mode := range []string{"auto", "no-map", "keep-id"} {
		mode := mode
		t.Run("userns-"+mode, func(t *testing.T) {
			usernsDriver, err := podman.NewWithConfig(podman.Config{ImagePullPolicy: "missing", UsernsMode: mode})
			if err != nil {
				t.Fatalf("create userns=%s Podman driver: %v", mode, err)
			}
			defer usernsDriver.Close()
			id := fmt.Sprintf("podman-userns-%s-e2e-%d", mode, time.Now().UnixNano())
			modeHandle, err := usernsDriver.Create(ctx, backendRequestFromSpec(sandboxFixtureSpec{
				Name: id, Image: image, Workspace: workspace,
				Command: []string{"/bin/sh", "-c", "sleep 300"}, NoHarden: true, PidsLimit: 128,
			}))
			if err != nil {
				if mode == "auto" && strings.Contains(strings.ToLower(err.Error()), "not enough unused ids") {
					t.Skipf("rootful Podman fixture has no free auto-userns range; rootless lane covers this mode: %v", err)
				}
				if mode == "no-map" && strings.Contains(strings.ToLower(err.Error()), "only supported in rootless mode") {
					t.Skipf("Podman documents userns=no-map as rootless-only; rootless lane covers this mode: %v", err)
				}
				t.Fatalf("create userns=%s sandbox: %v", mode, err)
			}
			defer func() {
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cleanupCancel()
				_ = usernsDriver.Delete(cleanupCtx, modeHandle.ID)
			}()
			modeInspect, err := engineClient.ContainerInspect(ctx, string(modeHandle.ID), client.ContainerInspectOptions{})
			if err != nil {
				t.Fatalf("inspect userns=%s sandbox: %v", mode, err)
			}
			if modeInspect.Container.HostConfig == nil {
				t.Fatalf("Podman userns=%s container host config missing", mode)
			}
			if err := usernsDriver.Start(ctx, modeHandle.ID); err != nil {
				if mode == "keep-id" && strings.Contains(err.Error(), "mount `mqueue` to `dev/mqueue`: Operation not permitted") {
					t.Skipf("Podman-in-Docker host blocks crun mqueue mount required by keep-id: %v", err)
				}
				if mode == "auto" && (strings.Contains(strings.ToLower(err.Error()), "subuid") || strings.Contains(strings.ToLower(err.Error()), "subgid") || strings.Contains(strings.ToLower(err.Error()), "available id")) {
					t.Skipf("Podman service has no subordinate ID range for userns=auto: %v", err)
				}
				t.Fatalf("start userns=%s sandbox: %v", mode, err)
			}
			probe := `awk 'NR == 1 && $3 > 1 { found=1 } END { exit !found }' /proc/self/uid_map && awk 'NR == 1 && $3 > 1 { found=1 } END { exit !found }' /proc/self/gid_map && test "$(id -u)" = 1000`
			result, err := usernsDriver.Exec(ctx, modeHandle.ID, driver.ExecRequest{Argv: []string{"/bin/sh", "-c", probe}})
			if err != nil || result.ExitCode != 0 {
				t.Fatalf("userns=%s runtime UID/GID mapping probe: result=%+v err=%v", mode, result, err)
			}
			if err := usernsDriver.Stop(ctx, modeHandle.ID); err != nil {
				t.Fatalf("stop userns=%s sandbox: %v", mode, err)
			}
		})
	}
}

func assertPodmanPlatformExecEvent(t *testing.T, ctx context.Context, backend interface {
	WatchPlatformEvents(context.Context, driver.PlatformEventWatchRequest) (<-chan driver.PlatformEventResult, error)
	Exec(context.Context, core.ID, driver.ExecRequest) (driver.ExecResult, error)
}, sandboxID core.ID, sandboxName string) {
	t.Helper()
	eventCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	events, err := backend.WatchPlatformEvents(eventCtx, driver.PlatformEventWatchRequest{SandboxID: sandboxID, SandboxName: sandboxName})
	if err != nil {
		t.Fatalf("watch Podman platform events: %v", err)
	}
	result, err := backend.Exec(ctx, sandboxID, driver.ExecRequest{Argv: []string{"/bin/true"}})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("trigger Podman platform event: result=%+v err=%v", result, err)
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case result, ok := <-events:
			if !ok {
				t.Fatal("Podman platform event stream closed before exec event")
			}
			if result.Err != nil {
				t.Fatalf("Podman platform event stream: %v", result.Err)
			}
			// Podman reports this container event as "exec"; Docker Engine
			// reports the equivalent lifecycle point as "exec_create".
			if result.Event.SandboxID == sandboxID && (result.Event.Reason == "exec_create" || result.Event.Reason == "exec") {
				if result.Event.Source != "podman" || result.Event.Type != "container" || result.Event.Terminal {
					t.Fatalf("Podman exec event = %#v", result.Event)
				}
				return
			}
		case <-deadline.C:
			t.Fatal("timed out waiting for Podman exec platform event")
		case <-ctx.Done():
			t.Fatalf("Podman E2E context ended while waiting for platform event: %v", ctx.Err())
		}
	}
}

func assertPodmanNativeBlackholeGatewayRoute(t *testing.T, ctx context.Context, networkName string) {
	t.Helper()
	rawHost := strings.TrimSpace(os.Getenv("DOCKER_HOST"))
	if rawHost == "" {
		t.Fatal("DOCKER_HOST is required to inspect the native Podman network contract")
	}
	endpoint, err := url.Parse(rawHost)
	if err != nil {
		t.Fatalf("parse Podman API endpoint: %v", err)
	}
	transport := &http.Transport{}
	switch endpoint.Scheme {
	case "tcp":
		endpoint.Scheme = "http"
	case "http", "https":
	case "unix":
		socketPath := endpoint.Path
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		}
		endpoint = &url.URL{Scheme: "http", Host: "podman"}
	default:
		t.Fatalf("unsupported Podman API endpoint scheme %q for native inspect", endpoint.Scheme)
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/v5.0.0/libpod/networks/" + url.PathEscape(networkName) + "/json"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		t.Fatalf("create native Podman inspect request: %v", err)
	}
	httpClient := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	defer httpClient.CloseIdleConnections()
	response, err := httpClient.Do(request)
	if err != nil {
		t.Fatalf("inspect native Podman network %q: %v", networkName, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 8<<10))
		t.Fatalf("native Podman network inspect returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	var details struct {
		Internal bool `json:"internal"`
		Subnets  []struct {
			Subnet  string `json:"subnet"`
			Gateway string `json:"gateway"`
		} `json:"subnets"`
		Routes []struct {
			Destination string `json:"destination"`
			RouteType   string `json:"route_type"`
		} `json:"routes"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&details); err != nil {
		t.Fatalf("decode native Podman network inspect: %v", err)
	}
	if !details.Internal || len(details.Subnets) == 0 {
		t.Fatalf("native Podman network internal=%t subnets=%+v; want internal network with inspectable subnets", details.Internal, details.Subnets)
	}
	for _, subnet := range details.Subnets {
		prefix, prefixErr := netip.ParsePrefix(subnet.Subnet)
		gateway, gatewayErr := netip.ParseAddr(subnet.Gateway)
		if prefixErr != nil || gatewayErr != nil || !prefix.Contains(gateway) {
			t.Fatalf("native Podman subnet=%q gateway=%q is invalid: subnet error=%v gateway error=%v", subnet.Subnet, subnet.Gateway, prefixErr, gatewayErr)
		}
		wantDestination := netip.PrefixFrom(gateway, gateway.BitLen())
		found := false
		for _, route := range details.Routes {
			destination, parseErr := netip.ParsePrefix(route.Destination)
			if parseErr == nil && strings.EqualFold(route.RouteType, "blackhole") && destination == wantDestination {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("native Podman network %q has no blackhole route to gateway %s; routes=%+v", networkName, gateway, details.Routes)
		}
	}
}

func TestPodmanNativeBlackholeGatewayRouteProbe(t *testing.T) {
	var inspectedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inspectedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"internal":true,"subnets":[{"subnet":"172.24.0.0/24","gateway":"172.24.0.1"}],"routes":[{"destination":"172.24.0.1/32","route_type":"blackhole"}]}`)
	}))
	defer server.Close()
	t.Setenv("DOCKER_HOST", server.URL)
	assertPodmanNativeBlackholeGatewayRoute(t, context.Background(), "sandbox-network")
	if inspectedPath != "/v5.0.0/libpod/networks/sandbox-network/json" {
		t.Fatalf("native inspect path = %q", inspectedPath)
	}
}

func startPodmanHostGatewayProbeTarget(t *testing.T, ctx context.Context, engine *client.Client, image, gateway string) (string, func()) {
	t.Helper()
	created, err := engine.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:       "quarshell-podman-host-gateway-probe-" + strconv.FormatInt(time.Now().UnixNano(), 10),
		Config:     &container.Config{Image: image, User: "0", Entrypoint: []string{"/bin/sh"}, Cmd: []string{"-c", "/usr/local/bin/quarshell-tcp-echo serve-tcp 0.0.0.0:17779 & wait"}},
		HostConfig: &container.HostConfig{NetworkMode: "host"},
	})
	if err != nil {
		t.Fatalf("create Podman host-network policy probe: %v", err)
	}
	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = engine.ContainerKill(cleanupCtx, created.ID, client.ContainerKillOptions{Signal: "KILL"})
		_, _ = engine.ContainerRemove(cleanupCtx, created.ID, client.ContainerRemoveOptions{Force: true})
	}
	if _, err := engine.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		cleanup()
		t.Fatalf("start Podman host-network policy probe: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if podmanPolicyProbeContainerExit(ctx, engine, created.ID, "probe-tcp", "127.0.0.1:17779") == 2 {
			return net.JoinHostPort(gateway, "17779"), cleanup
		}
		time.Sleep(50 * time.Millisecond)
	}
	cleanup()
	t.Fatal("Podman host-network policy-probe listener did not become reachable")
	return "", func() {}
}

func startPodmanPolicyProbeTarget(t *testing.T, ctx context.Context, engine *client.Client, image string) (string, func()) {
	t.Helper()
	created, err := engine.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: "quarshell-podman-policy-probe-" + strconv.FormatInt(time.Now().UnixNano(), 10),
		Config: &container.Config{
			Image: image, User: "0", Entrypoint: []string{"/bin/sh"},
			Cmd: []string{"-c", "/usr/local/bin/quarshell-tcp-echo serve-tcp 0.0.0.0:17777 & /usr/local/bin/quarshell-tcp-echo serve-udp 0.0.0.0:17778 & wait"},
		},
		HostConfig: &container.HostConfig{NetworkMode: "bridge"},
	})
	if err != nil {
		t.Fatalf("create reachable Podman policy-probe target on bridge: %v", err)
	}
	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = engine.ContainerKill(cleanupCtx, created.ID, client.ContainerKillOptions{Signal: "KILL"})
		_, _ = engine.ContainerRemove(cleanupCtx, created.ID, client.ContainerRemoveOptions{Force: true})
	}
	if _, err := engine.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		cleanup()
		t.Fatalf("start reachable Podman policy-probe target: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var address string
	for time.Now().Before(deadline) {
		inspect, inspectErr := engine.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
		if inspectErr == nil && inspect.Container.NetworkSettings != nil {
			for _, network := range inspect.Container.NetworkSettings.Networks {
				if network.IPAddress.IsValid() {
					address = net.JoinHostPort(network.IPAddress.String(), "17777")
					break
				}
			}
		}
		if address != "" {
			if podmanPolicyProbeContainerExit(ctx, engine, created.ID, "probe-tcp", "127.0.0.1:17777") == 2 &&
				podmanPolicyProbeContainerExit(ctx, engine, created.ID, "probe-udp", "127.0.0.1:17778") == 2 {
				return address, cleanup
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	cleanup()
	t.Fatal("Podman policy-probe TCP and UDP listeners did not become reachable")
	return "", func() {}
}

func podmanPolicyProbeContainerExit(ctx context.Context, engine *client.Client, containerID, mode, target string) int {
	checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	created, err := engine.ExecCreate(checkCtx, containerID, client.ExecCreateOptions{
		TTY: true, AttachStdout: true, AttachStderr: true,
		Cmd: []string{"/usr/local/bin/quarshell-tcp-echo", mode, target},
	})
	if err != nil {
		return -1
	}
	attach, err := engine.ExecAttach(checkCtx, created.ID, client.ExecAttachOptions{TTY: true})
	if err != nil {
		return -1
	}
	_, _ = io.Copy(io.Discard, attach.Reader)
	attach.Close()
	result, err := engine.ExecInspect(checkCtx, created.ID, client.ExecInspectOptions{})
	if err != nil {
		return -1
	}
	return result.ExitCode
}

func hasIDMap(contents, containerID, hostID, size string) bool {
	for _, line := range strings.Split(contents, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == containerID && fields[1] == hostID && fields[2] == size {
			return true
		}
	}
	return false
}
