package docker

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	core "github.com/KLORQ/quarshell-core"
	"github.com/moby/moby/client"
)

func lifecycleFixtureDriver(t *testing.T, handler http.HandlerFunc) *Driver {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	cli, err := client.New(client.WithHost("tcp://"+strings.TrimPrefix(server.URL, "http://")), client.WithAPIVersion("1.41"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return NewFromClient(cli)
}

func sandboxInspectFixture(state string) string {
	return "{\"Id\":\"sandbox-id\",\"Name\":\"/quarshell-fixture\",\"Config\":{\"Labels\":{\"quarshell.sandbox\":\"1\",\"quarshell.name\":\"fixture\",\"quarshell.role\":\"sandbox\",\"quarshell.network\":\"quarshell-net-fixture\",\"quarshell.ca_volume\":\"quarshell-ca-fixture\",\"quarshell.ssh_volume\":\"quarshell-ssh-fixture\",\"quarshell.internal/workspace\":\"default\",\"quarshell.internal/driver-config-used\":\"false\",\"quarshell.internal/driver-config-unlabelable\":\"false\",\"quarshell.internal/resource-admission-identities\":\"{}\"}},\"State\":{\"Status\":\"" + state + "\"}}"
}

func proxyInspectFixture(state string) string {
	return "{\"Id\":\"proxy-id\",\"Name\":\"/quarshell-proxy-fixture\",\"Config\":{\"Labels\":{\"quarshell.sandbox\":\"1\",\"quarshell.name\":\"fixture\",\"quarshell.network\":\"quarshell-net-fixture\",\"quarshell.role\":\"proxy\"},\"ExposedPorts\":{\"3128/tcp\":{}}},\"State\":{\"Status\":\"" + state + "\"}}"
}

func TestStopStopsProxySidecarAndWorkload(t *testing.T) {
	stopped := map[string]bool{}
	d := lifecycleFixtureDriver(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/sandbox-id/json"):
			_, _ = io.WriteString(w, sandboxInspectFixture("running"))
		case strings.HasSuffix(r.URL.Path, "/containers/quarshell-proxy-fixture/json"):
			_, _ = io.WriteString(w, proxyInspectFixture("running"))
		case strings.HasSuffix(r.URL.Path, "/containers/proxy-id/json"):
			_, _ = io.WriteString(w, proxyInspectFixture("running"))
		case strings.HasSuffix(r.URL.Path, "/containers/sandbox-id/stop"):
			stopped["sandbox"] = true
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/containers/proxy-id/stop"):
			stopped["proxy"] = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected Engine request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	})

	if err := d.Stop(context.Background(), core.ID("sandbox-id")); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if !stopped["sandbox"] || !stopped["proxy"] {
		t.Fatalf("stopped resources = %v; want workload and proxy sidecar", stopped)
	}
}

func TestStopIsIdempotentForStoppedWorkloadAndProxy(t *testing.T) {
	d := lifecycleFixtureDriver(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/sandbox-id/json"):
			_, _ = io.WriteString(w, sandboxInspectFixture("exited"))
		case strings.HasSuffix(r.URL.Path, "/containers/quarshell-proxy-fixture/json"):
			_, _ = io.WriteString(w, proxyInspectFixture("exited"))
		case strings.HasSuffix(r.URL.Path, "/containers/proxy-id/json"):
			_, _ = io.WriteString(w, proxyInspectFixture("exited"))
		default:
			t.Errorf("unexpected Engine request for stopped resources: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	})
	if err := d.Stop(context.Background(), core.ID("sandbox-id")); err != nil {
		t.Fatalf("repeated Stop() error = %v", err)
	}
}

func TestEnsureProxyRunningRestartsStoppedCompanion(t *testing.T) {
	var proxyStarts int
	var proxyInspects int
	var proxyIDInspects int
	d := lifecycleFixtureDriver(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/quarshell-proxy-fixture/json"):
			proxyInspects++
			_, _ = io.WriteString(w, proxyInspectFixture("exited"))
		case strings.HasSuffix(r.URL.Path, "/containers/proxy-id/json"):
			proxyIDInspects++
			_, _ = io.WriteString(w, proxyInspectFixture("exited"))
		case strings.HasSuffix(r.URL.Path, "/containers/proxy-id/start"):
			proxyStarts++
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected Engine request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	})

	id, port, found, err := d.ensureProxyRunning(context.Background(), "fixture")
	if err != nil {
		t.Fatalf("ensureProxyRunning() error = %v", err)
	}
	if !found || id != "proxy-id" || port != 3128 {
		t.Fatalf("ensureProxyRunning() = (%q, %d, %t); want (proxy-id, 3128, true)", id, port, found)
	}
	if proxyInspects != 1 || proxyIDInspects != 2 || proxyStarts != 1 {
		t.Fatalf("proxy requests: inspect-name=%d inspect-ID=%d start=%d; want 1/2/1", proxyInspects, proxyIDInspects, proxyStarts)
	}
}

func TestStartFailsClosedWhenConfiguredProxyIsMissing(t *testing.T) {
	var workloadStarts int
	d := lifecycleFixtureDriver(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/sandbox-id/json"):
			_, _ = io.WriteString(w, sandboxInspectFixture("exited"))
		case strings.HasSuffix(r.URL.Path, "/containers/quarshell-proxy-fixture/json"):
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, "/containers/sandbox-id/start"):
			workloadStarts++
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected Engine request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	})
	err := d.Start(context.Background(), core.ID("sandbox-id"))
	if err == nil || !strings.Contains(err.Error(), "policy proxy sidecar is missing") {
		t.Fatalf("Start() error = %v; want fail-closed missing proxy error", err)
	}
	if workloadStarts != 0 {
		t.Fatalf("workload started %d times without its configured proxy", workloadStarts)
	}
}

func TestStopStopsOrphanProxyUsingCleanupManifest(t *testing.T) {
	manifestDir := filepath.Join(t.TempDir(), "cleanup")
	var proxyStopped bool
	d := lifecycleFixtureDriver(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/sandbox-id/json"):
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, "/containers/quarshell-proxy-fixture/json"),
			strings.HasSuffix(r.URL.Path, "/containers/proxy-id/json"):
			_, _ = io.WriteString(w, proxyInspectFixture("running"))
		case strings.HasSuffix(r.URL.Path, "/containers/proxy-id/stop"):
			proxyStopped = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected Engine request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	})
	d.runtimeConfig.CleanupManifestDir = manifestDir
	if err := d.saveCleanupManifest(cleanupManifest{Version: cleanupManifestVersion, ContainerID: "sandbox-id", SandboxName: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Stop(context.Background(), core.ID("sandbox-id")); err != nil {
		t.Fatalf("Stop() orphan error = %v", err)
	}
	if !proxyStopped {
		t.Fatal("orphan proxy remained running after Stop")
	}
}

func TestDeleteReturnsContainerRemovalFailure(t *testing.T) {
	d := lifecycleFixtureDriver(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/sandbox-id/json"):
			_, _ = io.WriteString(w, sandboxInspectFixture("exited"))
		case strings.HasSuffix(r.URL.Path, "/containers/quarshell-proxy-fixture/json"):
			_, _ = io.WriteString(w, proxyInspectFixture("exited"))
		case strings.HasSuffix(r.URL.Path, "/containers/proxy-id") && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/containers/sandbox-id") && r.Method == http.MethodDelete:
			http.Error(w, "daemon refused removal", http.StatusInternalServerError)
		case strings.HasSuffix(r.URL.Path, "/networks/quarshell-net-fixture") && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/volumes/quarshell-ca-fixture") && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/volumes/quarshell-ssh-fixture") && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected Engine request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	})

	if err := d.Delete(context.Background(), core.ID("sandbox-id")); err == nil {
		t.Fatal("Delete() returned nil after the Engine rejected workload removal")
	}
}

func TestDeleteRetriesCleanupAfterWorkloadWasRemoved(t *testing.T) {
	manifestDir := filepath.Join(t.TempDir(), "cleanup")
	workloadPresent := true
	networkPresent := true
	networkRemoveAttempts := 0
	dataVolumePresent := true
	dataVolumeRemoveAttempts := 0
	d := lifecycleFixtureDriver(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/sandbox-id/json"):
			if !workloadPresent {
				http.NotFound(w, r)
				return
			}
			_, _ = io.WriteString(w, sandboxInspectWithDataVolumeFixture("exited"))
		case strings.HasSuffix(r.URL.Path, "/containers/quarshell-proxy-fixture/json"):
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, "/containers/sandbox-id") && r.Method == http.MethodDelete:
			workloadPresent = false
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/networks/quarshell-net-fixture") && r.Method == http.MethodGet:
			if !networkPresent {
				http.NotFound(w, r)
				return
			}
			_, _ = io.WriteString(w, `{"Name":"quarshell-net-fixture","Internal":true,"Labels":{"quarshell.sandbox":"1","quarshell.name":"fixture","quarshell.role":"network"}}`)
		case strings.HasSuffix(r.URL.Path, "/networks/quarshell-net-fixture") && r.Method == http.MethodDelete:
			networkRemoveAttempts++
			networkPresent = false
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/volumes/quarshell-data-fixture") && r.Method == http.MethodGet:
			if !dataVolumePresent {
				http.NotFound(w, r)
				return
			}
			_, _ = io.WriteString(w, volumeInspectFixture("quarshell-data-fixture", roleVolumeData))
		case strings.HasSuffix(r.URL.Path, "/volumes/quarshell-data-fixture") && r.Method == http.MethodDelete:
			dataVolumeRemoveAttempts++
			if dataVolumeRemoveAttempts == 1 {
				http.Error(w, "volume temporarily busy", http.StatusInternalServerError)
				return
			}
			dataVolumePresent = false
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/volumes/quarshell-ca-fixture") && r.Method == http.MethodGet,
			strings.HasSuffix(r.URL.Path, "/volumes/quarshell-ssh-fixture") && r.Method == http.MethodGet:
			http.NotFound(w, r)
		default:
			t.Errorf("unexpected Engine request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	})
	d.runtimeConfig.CleanupManifestDir = manifestDir
	ctx := context.Background()
	firstErr := d.Delete(ctx, core.ID("sandbox-id"))
	if firstErr == nil {
		t.Fatal("first Delete() succeeded despite a failed volume removal")
	}
	manifestPath := filepath.Join(manifestDir, "sandbox-id.json")
	if _, err := os.Stat(manifestPath); err != nil {
		t.Fatalf("cleanup manifest missing after partial deletion (Delete error: %v): %v", firstErr, err)
	}
	if err := d.Delete(ctx, core.ID("sandbox-id")); err != nil {
		t.Fatalf("retry Delete() error = %v", err)
	}
	if dataVolumePresent || dataVolumeRemoveAttempts != 2 || networkPresent || networkRemoveAttempts != 1 {
		t.Fatalf("data volume present=%t removal attempts=%d; network present=%t removal attempts=%d; want both absent", dataVolumePresent, dataVolumeRemoveAttempts, networkPresent, networkRemoveAttempts)
	}
	if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
		t.Fatalf("cleanup manifest still exists after successful retry: stat error=%v", err)
	}
}

func TestEnsureVolumeRejectsForeignVolume(t *testing.T) {
	var createRequests int
	d := lifecycleFixtureDriver(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/volumes/quarshell-data-fixture") && r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `{"Name":"quarshell-data-fixture","Labels":{"owner":"another-sandbox"}}`)
		case strings.HasSuffix(r.URL.Path, "/volumes/create"):
			createRequests++
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"Name":"quarshell-data-fixture"}`)
		default:
			t.Errorf("unexpected Engine request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	})
	if _, err := d.ensureVolume(context.Background(), "quarshell-data-fixture", "fixture", roleVolumeData); err == nil {
		t.Fatal("ensureVolume accepted a foreign volume")
	}
	if createRequests != 0 {
		t.Fatalf("attempted to replace an existing volume %d times", createRequests)
	}
}

func TestDeleteIsIdempotentWhenWorkloadAndManifestAreAbsent(t *testing.T) {
	d := lifecycleFixtureDriver(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	if err := d.Delete(context.Background(), core.ID("sandbox-id")); err != nil {
		t.Fatalf("Delete() on an already absent workload returned %v", err)
	}
}

func TestDeleteRefusesToRemoveForeignVolume(t *testing.T) {
	var deleteRequests int
	d := lifecycleFixtureDriver(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/volumes/quarshell-data-fixture") && r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `{"Name":"quarshell-data-fixture","Labels":{"owner":"foreign"}}`)
		case strings.HasSuffix(r.URL.Path, "/volumes/quarshell-data-fixture") && r.Method == http.MethodDelete:
			deleteRequests++
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected Engine request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	})
	err := d.removeManagedVolume(context.Background(), cleanupManifest{SandboxName: "fixture"}, "quarshell-data-fixture", roleVolumeData)
	if err == nil {
		t.Fatal("cleanup accepted a foreign volume")
	}
	if deleteRequests != 0 {
		t.Fatalf("attempted to remove foreign volume %d times", deleteRequests)
	}
}

func sandboxInspectWithDataVolumeFixture(state string) string {
	return strings.Replace(sandboxInspectFixture(state), `"quarshell.ca_volume":"quarshell-ca-fixture"`, `"quarshell.volume":"quarshell-data-fixture","quarshell.ca_volume":"quarshell-ca-fixture"`, 1)
}

func volumeInspectFixture(name, role string) string {
	return `{"Name":"` + name + `","Labels":{"quarshell.sandbox":"1","quarshell.name":"fixture","quarshell.role":"` + role + `","quarshell.volume":"1"}}`
}
