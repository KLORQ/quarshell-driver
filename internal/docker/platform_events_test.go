package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	core "github.com/KLORQ/quarshell-core"
	"github.com/KLORQ/quarshell-driver/driver"
	"github.com/moby/moby/client"
)

func TestWatchPlatformEventsFiltersAndSanitizes(t *testing.T) {
	const sandboxID = "container-id"
	var gotFilters map[string]map[string]bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1.47/events" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		if err := json.Unmarshal([]byte(r.URL.Query().Get("filters")), &gotFilters); err != nil {
			http.Error(w, "invalid filter query", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		flusher := w.(http.Flusher)
		_, _ = fmt.Fprintln(w, `{"Type":"container","Action":"exec_create: echo do-not-leak","Actor":{"ID":"container-id","Attributes":{"name":"sandbox","secret":"do-not-leak"}},"TimeNano":1792610000000000000}`)
		_, _ = fmt.Fprintln(w, `{"Type":"container","Action":"die","Actor":{"ID":"container-id","Attributes":{"name":"sandbox"}},"Time":1792610000}`)
		flusher.Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	engine, err := client.NewClientWithOpts(client.WithHost(server.URL), client.WithVersion("1.47"))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	backend := NewFromClientWithRuntimeConfig(engine, RuntimeConfig{BackendID: "podman"})

	ctx, cancel := context.WithCancel(context.Background())
	results, err := backend.WatchPlatformEvents(ctx, driver.PlatformEventWatchRequest{SandboxID: core.ID(sandboxID), SandboxName: "sandbox"})
	if err != nil {
		t.Fatal(err)
	}
	first := receivePlatformEvent(t, results)
	if first.Err != nil {
		t.Fatal(first.Err)
	}
	if first.Event.SandboxID != core.ID(sandboxID) || first.Event.Source != "podman" || first.Event.Type != "container" || first.Event.Reason != "exec_create" || first.Event.Terminal {
		t.Fatalf("first event = %#v", first.Event)
	}
	if first.Event.Time != time.Unix(0, 1792610000000000000).UTC() || first.Event.Message != "container exec_create" || len(first.Event.Metadata) != 0 {
		t.Fatalf("first event leaked or lost normalized fields: %#v", first.Event)
	}
	second := receivePlatformEvent(t, results)
	if second.Err != nil || !second.Event.Terminal || second.Event.Reason != "die" || second.Event.Time != time.Unix(1792610000, 0).UTC() {
		t.Fatalf("second event = %#v", second)
	}

	if !gotFilters["type"]["container"] || !gotFilters["container"][sandboxID] {
		t.Fatalf("Engine event filters = %#v", gotFilters)
	}
	cancel()
	select {
	case _, ok := <-results:
		if ok {
			t.Fatal("event stream remained open after cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event stream did not close after cancellation")
	}
}

func TestWatchPlatformEventsRejectsMissingIdentity(t *testing.T) {
	backend := NewFromClientWithRuntimeConfig(nil, RuntimeConfig{BackendID: "docker"})
	if _, err := backend.WatchPlatformEvents(context.Background(), driver.PlatformEventWatchRequest{}); err == nil {
		t.Fatal("watch with an empty sandbox ID unexpectedly succeeded")
	}
}

func receivePlatformEvent(t *testing.T, results <-chan driver.PlatformEventResult) driver.PlatformEventResult {
	t.Helper()
	select {
	case result, ok := <-results:
		if !ok {
			t.Fatal("platform event stream closed unexpectedly")
		}
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for platform event")
		return driver.PlatformEventResult{}
	}
}
