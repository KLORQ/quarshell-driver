package podman

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strings"
)

type nativeNetworkRoute struct {
	Destination string `json:"destination"`
	Gateway     string `json:"gateway,omitempty"`
	RouteType   string `json:"route_type,omitempty"`
}

type nativeNetworkSubnet struct {
	Subnet  string `json:"subnet"`
	Gateway string `json:"gateway,omitempty"`
}

type nativeNetworkSpec struct {
	Name       string                `json:"name"`
	Driver     string                `json:"driver"`
	Internal   bool                  `json:"internal"`
	DNSEnabled bool                  `json:"dns_enabled"`
	Labels     map[string]string     `json:"labels,omitempty"`
	Subnets    []nativeNetworkSubnet `json:"subnets"`
	Routes     []nativeNetworkRoute  `json:"routes,omitempty"`
}

type nativeNetworkSummary struct {
	Name    string                `json:"name"`
	Subnets []nativeNetworkSubnet `json:"subnets"`
}

type nativeNetworkDetails struct {
	Name     string                `json:"name"`
	Internal bool                  `json:"internal"`
	Subnets  []nativeNetworkSubnet `json:"subnets"`
	Routes   []nativeNetworkRoute  `json:"routes"`
}

func nativeNetworkCallbacks(host string) (func(context.Context, string, bool, map[string]string) error, func(context.Context, string, bool) error) {
	create := func(ctx context.Context, name string, internal bool, labels map[string]string) error {
		endpoint, err := nativeEndpoint(host)
		if err != nil {
			return err
		}
		endpoint.Path = path.Join(endpoint.Path, "/v5.0.0/libpod/networks/create")
		httpClient, err := nativeHTTPClient(host, endpoint)
		if err != nil {
			return err
		}
		for attempt := 0; attempt < 32; attempt++ {
			spec := nativeNetworkSpec{Name: name, Driver: "bridge", Internal: internal, DNSEnabled: true, Labels: labels}
			if internal {
				allocationName := name
				if attempt > 0 {
					allocationName = fmt.Sprintf("%s-retry-%d", name, attempt)
				}
				prefix, gateway, err := availableNetworkSubnet(ctx, host, allocationName)
				if err != nil {
					return err
				}
				spec.Subnets = []nativeNetworkSubnet{{Subnet: prefix.String(), Gateway: gateway.String()}}
				spec.Routes = []nativeNetworkRoute{{Destination: netip.PrefixFrom(gateway, 32).String(), RouteType: "blackhole"}}
			}
			payload, err := json.Marshal(spec)
			if err != nil {
				return err
			}
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
			if err != nil {
				return err
			}
			request.Header.Set("Content-Type", "application/json")
			response, err := httpClient.Do(request)
			if err != nil {
				return fmt.Errorf("podman libpod network create: %w", err)
			}
			var message struct {
				Message string `json:"message"`
				Cause   string `json:"cause"`
			}
			_ = json.NewDecoder(response.Body).Decode(&message)
			response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				return nil
			}
			if message.Message == "" {
				message.Message = message.Cause
			}
			messageLower := strings.ToLower(message.Message)
			if internal && strings.Contains(messageLower, "route gateway nil") {
				return fmt.Errorf("podman API does not support blackhole network routes; host-gateway isolation requires Podman 6.0 or newer")
			}
			if !internal || !strings.Contains(messageLower, "subnet") || !strings.Contains(messageLower, "used") {
				return fmt.Errorf("podman libpod network create returned %s: %s", response.Status, message.Message)
			}
		}
		return fmt.Errorf("podman libpod network create: exhausted subnet allocation retries")
	}
	verify := func(ctx context.Context, name string, internal bool) error {
		endpoint, err := nativeEndpoint(host)
		if err != nil {
			return err
		}
		endpoint.Path = path.Join(endpoint.Path, "/v5.0.0/libpod/networks", url.PathEscape(name), "json")
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return err
		}
		httpClient, err := nativeHTTPClient(host, endpoint)
		if err != nil {
			return err
		}
		response, err := httpClient.Do(request)
		if err != nil {
			return fmt.Errorf("podman libpod network inspect: %w", err)
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return fmt.Errorf("podman libpod network inspect returned %s", response.Status)
		}
		var details nativeNetworkDetails
		if err := json.NewDecoder(response.Body).Decode(&details); err != nil {
			return fmt.Errorf("decode Podman network inspection: %w", err)
		}
		if internal && !details.Internal {
			return fmt.Errorf("network is not internal")
		}
		if internal {
			if len(details.Subnets) == 0 {
				return fmt.Errorf("network has no inspectable subnets")
			}
			for _, subnet := range details.Subnets {
				prefix, err := netip.ParsePrefix(subnet.Subnet)
				gateway, gatewayErr := netip.ParseAddr(subnet.Gateway)
				if err != nil || gatewayErr != nil {
					return fmt.Errorf("network has a subnet without a valid gateway")
				}
				found := false
				for _, route := range details.Routes {
					dst, parseErr := netip.ParsePrefix(route.Destination)
					if parseErr == nil && strings.EqualFold(route.RouteType, "blackhole") && dst == netip.PrefixFrom(gateway, gateway.BitLen()) && prefix.Contains(gateway) {
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("network has no blackhole route for gateway %s", gateway)
				}
			}
			return nil
		}
		return nil
	}
	return create, verify
}

func availableNetworkSubnet(ctx context.Context, host, name string) (netip.Prefix, netip.Addr, error) {
	endpoint, err := nativeEndpoint(host)
	if err != nil {
		return netip.Prefix{}, netip.Addr{}, err
	}
	endpoint.Path = path.Join(endpoint.Path, "/v5.0.0/libpod/networks/json")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return netip.Prefix{}, netip.Addr{}, err
	}
	httpClient, err := nativeHTTPClient(host, endpoint)
	if err != nil {
		return netip.Prefix{}, netip.Addr{}, err
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return netip.Prefix{}, netip.Addr{}, fmt.Errorf("list Podman networks: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return netip.Prefix{}, netip.Addr{}, fmt.Errorf("list Podman networks returned %s", response.Status)
	}
	var networks []nativeNetworkSummary
	if err := json.NewDecoder(response.Body).Decode(&networks); err != nil {
		return netip.Prefix{}, netip.Addr{}, fmt.Errorf("decode Podman network list: %w", err)
	}
	used := make([]netip.Prefix, 0)
	for _, network := range networks {
		for _, subnet := range network.Subnets {
			if prefix, err := netip.ParsePrefix(subnet.Subnet); err == nil {
				used = append(used, prefix)
			}
		}
	}
	hash := sha256.Sum256([]byte(name))
	start := int(hash[0])<<8 | int(hash[1])
	for i := 0; i < 4096; i++ {
		index := (start + i) % 4096
		second := 16 + index/256
		third := index % 256
		base := netip.AddrFrom4([4]byte{172, byte(second), byte(third), 0})
		prefix := netip.PrefixFrom(base, 24)
		collision := false
		for _, candidate := range used {
			if prefixesOverlap(prefix, candidate) {
				collision = true
				break
			}
		}
		if !collision {
			return prefix, base.Next(), nil
		}
	}
	return netip.Prefix{}, netip.Addr{}, fmt.Errorf("no unused sandbox subnet is available")
}

func prefixesOverlap(a, b netip.Prefix) bool { return a.Contains(b.Addr()) || b.Contains(a.Addr()) }
