// Package observability defines protocol observation interfaces.
package observability

import "time"

// ProtocolMetrics observes protocol request volume, latency, and streams.
type ProtocolMetrics interface {
	ObserveHTTPRequest(protocol, operation, status string, duration time.Duration)
	StreamOpened(protocol string)
	StreamClosed(protocol string)
}

// DependencyMetrics observes the readiness of runtime dependencies.
type DependencyMetrics interface {
	SetDependencyReady(name string, ready bool)
}

// A2AMetrics observes standard A2A push notification configuration and
// delivery. Labels are bounded operational statuses, never callback secrets.
type A2AMetrics interface {
	ObservePushConfig(operation, status string)
	ObservePushDelivery(status string, duration time.Duration)
}
