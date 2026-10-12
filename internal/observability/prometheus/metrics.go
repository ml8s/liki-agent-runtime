// Package prometheus exposes protocol, LLM, and dependency observations.
package prometheus

import (
	"net/http"
	"time"

	"github.com/ml8s/liki-agents/internal/domain"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics is the Prometheus implementation of the protocol and runtime
// observation interfaces.
type Metrics struct {
	registry          *prometheus.Registry
	protocolRequests  *prometheus.CounterVec
	protocolLatency   *prometheus.HistogramVec
	activeStreams     *prometheus.GaugeVec
	llmTokens         *prometheus.CounterVec
	llmCalls          *prometheus.CounterVec
	toolCalls         *prometheus.CounterVec
	toolLatency       *prometheus.HistogramVec
	delegations       *prometheus.CounterVec
	delegationLatency *prometheus.HistogramVec
	dependencyUp      *prometheus.GaugeVec
}

// New builds a metrics registry with runtime and protocol collectors.
func New() *Metrics {
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	metrics := &Metrics{
		registry: registry,
		protocolRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "liki_agents_protocol_requests_total",
			Help: "Agent protocol requests by protocol, operation, and status.",
		}, []string{"protocol", "operation", "status"}),
		protocolLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "liki_agents_protocol_request_duration_seconds",
			Help:    "Agent protocol request duration.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300},
		}, []string{"protocol", "operation"}),
		activeStreams: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "liki_agents_protocol_active_streams",
			Help: "Currently active streaming protocol requests.",
		}, []string{"protocol"}),
		llmTokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "liki_agents_llm_tokens_total",
			Help: "LLM tokens by model and token type.",
		}, []string{"model", "type"}),
		llmCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "liki_agents_llm_calls_total",
			Help: "LLM calls by model and status.",
		}, []string{"model", "status"}),
		toolCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "liki_agents_tool_calls_total",
			Help: "MCP tool calls by agent, tool, and status.",
		}, []string{"agent", "tool", "status"}),
		toolLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "liki_agents_tool_duration_seconds",
			Help:    "MCP tool duration by agent and tool.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
		}, []string{"agent", "tool"}),
		delegations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "liki_agents_delegations_total",
			Help: "Agent delegations by caller, target, and status.",
		}, []string{"caller", "target", "status"}),
		delegationLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "liki_agents_delegation_duration_seconds",
			Help:    "Agent delegation duration by caller and target.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300},
		}, []string{"caller", "target"}),
		dependencyUp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "liki_agents_dependency_up",
			Help: "Whether a runtime dependency is ready.",
		}, []string{"dependency"}),
	}
	registry.MustRegister(
		metrics.protocolRequests,
		metrics.protocolLatency,
		metrics.activeStreams,
		metrics.llmTokens,
		metrics.llmCalls,
		metrics.toolCalls,
		metrics.toolLatency,
		metrics.delegations,
		metrics.delegationLatency,
		metrics.dependencyUp,
	)
	return metrics
}

// Handler returns the Prometheus scrape handler.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// ObserveHTTPRequest records one protocol request and its latency.
func (m *Metrics) ObserveHTTPRequest(protocol, operation, status string, duration time.Duration) {
	m.protocolRequests.WithLabelValues(protocol, operation, status).Inc()
	m.protocolLatency.WithLabelValues(protocol, operation).Observe(duration.Seconds())
}

// ObserveLLMCall records one model call and its token usage.
func (m *Metrics) ObserveLLMCall(model, status string, usage domain.LLMTokenUsage) {
	m.llmCalls.WithLabelValues(model, status).Inc()
	m.llmTokens.WithLabelValues(model, "prompt").Add(float64(usage.PromptTokens))
	m.llmTokens.WithLabelValues(model, "completion").Add(float64(usage.CompletionTokens))
	m.llmTokens.WithLabelValues(model, "thought").Add(float64(usage.ThoughtTokens))
	m.llmTokens.WithLabelValues(model, "total").Add(float64(usage.TotalTokens))
}

// ObserveToolCall records one tool invocation and its duration.
func (m *Metrics) ObserveToolCall(agent, tool, status string, duration time.Duration) {
	m.toolCalls.WithLabelValues(agent, tool, status).Inc()
	m.toolLatency.WithLabelValues(agent, tool).Observe(duration.Seconds())
}

// ObserveAgentDelegation records one sub-agent delegation and its duration.
func (m *Metrics) ObserveAgentDelegation(caller, target, status string, duration time.Duration) {
	m.delegations.WithLabelValues(caller, target, status).Inc()
	m.delegationLatency.WithLabelValues(caller, target).Observe(duration.Seconds())
}

// StreamOpened records a new active protocol stream.
func (m *Metrics) StreamOpened(protocol string) {
	m.activeStreams.WithLabelValues(protocol).Inc()
}

// StreamClosed records the end of an active protocol stream.
func (m *Metrics) StreamClosed(protocol string) {
	m.activeStreams.WithLabelValues(protocol).Dec()
}

// SetDependencyReady records the readiness of one dependency.
func (m *Metrics) SetDependencyReady(name string, ready bool) {
	value := 0.0
	if ready {
		value = 1
	}
	m.dependencyUp.WithLabelValues(name).Set(value)
}
