package podman

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNativeNetworkCreateRequiresBlackholeRouteSupport(t *testing.T) {
	var createRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v5.0.0/libpod/networks/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))
		case r.Method == http.MethodPost && r.URL.Path == "/v5.0.0/libpod/networks/create":
			createRequests++
			var request nativeNetworkSpec
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode network create request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if len(request.Routes) != 1 || request.Routes[0].RouteType != "blackhole" || request.Routes[0].Gateway != "" {
				t.Errorf("network routes = %+v; want gateway-independent blackhole route", request.Routes)
			}
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"route gateway nil"}`))
		default:
			t.Errorf("unexpected native network request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	create, _ := nativeNetworkCallbacks(server.URL)
	err := create(context.Background(), "compat-probe", true, nil)
	if err == nil || !strings.Contains(err.Error(), "does not support blackhole network routes") {
		t.Fatalf("network create error = %v; want fail-closed blackhole capability error", err)
	}
	if createRequests != 1 {
		t.Fatalf("network create requests = %d; want one request and no retry without the route", createRequests)
	}
}

func TestNativeNetworkVerifyRequiresInspectedBlackholeRoute(t *testing.T) {
	for _, tc := range []struct {
		name   string
		routes []nativeNetworkRoute
		wantOK bool
	}{
		{name: "route persisted", routes: []nativeNetworkRoute{{Destination: "172.20.0.1/32", RouteType: "blackhole"}}, wantOK: true},
		{name: "route omitted", routes: nil},
		{name: "route has wrong type", routes: []nativeNetworkRoute{{Destination: "172.20.0.1/32", RouteType: "unicast"}}},
		{name: "route targets another address", routes: []nativeNetworkRoute{{Destination: "172.20.0.2/32", RouteType: "blackhole"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v5.0.0/libpod/networks/probe/json" {
					t.Errorf("unexpected inspect request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(nativeNetworkDetails{
					Name: "probe", Internal: true,
					Subnets: []nativeNetworkSubnet{{Subnet: "172.20.0.0/24", Gateway: "172.20.0.1"}},
					Routes:  tc.routes,
				})
			}))
			defer server.Close()

			_, verify := nativeNetworkCallbacks(server.URL)
			err := verify(context.Background(), "probe", true)
			if (err == nil) != tc.wantOK {
				t.Fatalf("network verify error = %v; want success=%t", err, tc.wantOK)
			}
			if !tc.wantOK && err.Error() != "network has no blackhole route for gateway 172.20.0.1" {
				t.Fatalf("network verify error = %v; want missing blackhole route", err)
			}
		})
	}
}
