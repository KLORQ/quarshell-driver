package kubernetes

import (
	"context"
	"strings"
	"testing"
	"time"

	core "github.com/KLORQ/quarshell-core"
	"github.com/KLORQ/quarshell-driver/driver"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
)

func TestPlatformEventsFilterSandboxAndOmitArbitraryCRData(t *testing.T) {
	namespace, err := workspaceNamespaceName("gateway-a", "team-a")
	if err != nil {
		t.Fatal(err)
	}
	identity := sandboxRuntimeIdentity{Namespace: namespace, Name: "resource-a", UID: "uid-a"}
	target := platformEventSandboxObject(identity, "demo", "10", "Running")
	target.SetAnnotations(map[string]string{"private": "annotation-must-not-leak"})
	target.SetLabels(map[string]string{
		labelManagedBy: managedByValue, labelGatewayID: "gateway-a", labelSandboxWorkspace: "team-a",
		labelSandboxName: "demo", "private": "label-must-not-leak",
	})
	target.Object["status"] = map[string]any{
		"phase":      "Running",
		"conditions": []any{map[string]any{"type": "Ready", "status": "True", "message": "condition-must-not-leak"}},
	}
	foreign := platformEventSandboxObject(identity, "other", "11", "Running")
	foreign.SetName("resource-other")
	foreign.SetUID(types.UID("other-uid"))
	watcher := watch.NewFake()
	client := &scriptedSandboxWatchClient{
		lists:     []sandboxWatchListResult{{list: sandboxList("10", target, foreign)}},
		watches:   []sandboxWatchOpenResult{{watch: watcher}},
		watchOpen: make(chan string, 2),
	}
	results := make(chan driver.PlatformEventResult)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &Driver{gatewayID: "gateway-a"}
	finished := make(chan struct{})
	go func() {
		backend.forwardPlatformEvents(ctx, client, sandboxAPIVersionV1Beta1, identity, "demo", results)
		close(finished)
	}()

	initial := receivePlatformEventResult(t, results)
	if initial.Err != nil || initial.Event.SandboxID != core.ID(kubernetesRuntimeID(identity)) || initial.Event.Type != "SNAPSHOT" || initial.Event.Reason != "Running" {
		t.Fatalf("initial event = %+v", initial)
	}
	if len(initial.Event.Metadata) != 2 || initial.Event.Metadata["namespace"] != namespace || initial.Event.Metadata["resource_version"] != "10" {
		t.Fatalf("initial metadata = %#v", initial.Event.Metadata)
	}
	encoded := initial.Event.Message + initial.Event.Reason + strings.Join(mapKeys(initial.Event.Metadata), "")
	for _, secret := range []string{"private", "must-not-leak", "condition-must-not-leak", "label-must-not-leak"} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("event leaked arbitrary CR data %q: %+v", secret, initial.Event)
		}
	}

	updated := platformEventSandboxObject(identity, "demo", "12", "Stopped")
	updated.SetLabels(target.GetLabels())
	watcher.Modify(updated)
	modified := receivePlatformEventResult(t, results)
	if modified.Err != nil || modified.Event.Type != "MODIFIED" || modified.Event.Reason != "Stopped" || modified.Event.Terminal {
		t.Fatalf("modified event = %+v", modified)
	}

	wrongUID := platformEventSandboxObject(identity, "demo", "13", "Failed")
	wrongUID.SetUID(types.UID("replacement-uid"))
	wrongUID.SetLabels(target.GetLabels())
	watcher.Modify(wrongUID)
	select {
	case event := <-results:
		t.Fatalf("replacement resource event was not filtered: %+v", event)
	case <-time.After(25 * time.Millisecond):
	}

	deleted := platformEventSandboxObject(identity, "demo", "14", "Stopped")
	deleted.SetLabels(target.GetLabels())
	watcher.Delete(deleted)
	final := receivePlatformEventResult(t, results)
	if final.Err != nil || !final.Event.Terminal || final.Event.Type != "DELETED" || final.Event.Reason != "Deleted" {
		t.Fatalf("terminal event = %+v", final)
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("platform event watch did not stop on cancellation")
	}
}

func TestWatchPlatformEventsRejectsNonRuntimeID(t *testing.T) {
	backend := NewWithBackendConfig(BackendConfig{GatewayID: "gateway-a"})
	if _, err := backend.WatchPlatformEvents(context.Background(), driver.PlatformEventWatchRequest{SandboxID: "demo"}); err == nil {
		t.Fatal("non-runtime sandbox ID was accepted")
	}
}

func receivePlatformEventResult(t *testing.T, results <-chan driver.PlatformEventResult) driver.PlatformEventResult {
	t.Helper()
	select {
	case result, ok := <-results:
		if !ok {
			t.Fatal("platform event stream closed before delivering the event")
		}
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for platform event")
		return driver.PlatformEventResult{}
	}
}

func platformEventSandboxObject(identity sandboxRuntimeIdentity, sandboxName, resourceVersion, phase string) *unstructured.Unstructured {
	object := sandboxWatchObject(identity.Name, identity.Namespace, resourceVersion)
	object.SetUID(types.UID(identity.UID))
	object.SetLabels(map[string]string{
		labelManagedBy: managedByValue, labelGatewayID: "gateway-a", labelSandboxWorkspace: "team-a", labelSandboxName: sandboxName,
	})
	object.Object["spec"] = map[string]any{"operatingMode": "Running"}
	object.Object["status"] = map[string]any{"phase": phase}
	return object
}

func mapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key, value := range values {
		keys = append(keys, key, value)
	}
	return keys
}
