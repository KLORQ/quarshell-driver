package driver

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel/propagation"
)

const (
	supervisorOTLPEndpointEnv = "OPENSHELL_OTLP_ENDPOINT"
	supervisorTraceParentEnv  = "TRACEPARENT"
	supervisorTraceStateEnv   = "TRACESTATE"
)

// SupervisorTelemetryEnvironment replaces caller-supplied telemetry values
// with the Gateway endpoint and the active backend span's W3C context. Runtime
// code removes these private values before starting the sandbox workload.
func SupervisorTelemetryEnvironment(ctx context.Context, endpoint string, environment []string) []string {
	out := make([]string, 0, len(environment)+3)
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if key == supervisorOTLPEndpointEnv || key == supervisorTraceParentEnv || key == supervisorTraceStateEnv {
			continue
		}
		out = append(out, entry)
	}

	if endpoint = strings.TrimSpace(endpoint); endpoint == "" {
		return out
	}
	out = append(out, supervisorOTLPEndpointEnv+"="+endpoint)
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	if parent := strings.TrimSpace(carrier.Get("traceparent")); parent != "" {
		out = append(out, supervisorTraceParentEnv+"="+parent)
	}
	if state := strings.TrimSpace(carrier.Get("tracestate")); state != "" {
		out = append(out, supervisorTraceStateEnv+"="+state)
	}
	return out
}
