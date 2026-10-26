// Package prometheus exposes protocol, LLM, and dependency observations.
package prometheus

import (
	"net/http"
	"time"

	"github.com/ml8s/liki-agent-runtime/internal/domain"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics is the Prometheus implementation of the protocol and runtime
// observation interfaces.
type Metrics struct {
	registry           *prometheus.Registry
	protocolRequests   *prometheus.CounterVec
	protocolLatency    *prometheus.HistogramVec
	activeStreams      *prometheus.GaugeVec
	llmTokens          *prometheus.CounterVec
	llmCalls           *prometheus.CounterVec
	llmCallLatency     *prometheus.HistogramVec
	toolCalls          *prometheus.CounterVec
	toolLatency        *prometheus.HistogramVec
	delegations        *prometheus.CounterVec
	delegationLatency  *prometheus.HistogramVec
	dependencyUp       *prometheus.GaugeVec
	auditAppends       *prometheus.CounterVec
	auditAppendLatency *prometheus.HistogramVec
	mcpCatalogRequests *prometheus.CounterVec
	auditBatchEvents   *prometheus.HistogramVec
	auditQueueDepth    *prometheus.GaugeVec
	runRejections      *prometheus.CounterVec
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
		llmCallLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "liki_agents_llm_call_duration_seconds",
			Help:    "LLM call duration by model and status.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300},
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
		auditAppends: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "liki_agents_audit_appends_total",
			Help: "Audit event appends by outcome.",
		}, []string{"status"}),
		auditAppendLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "liki_agents_audit_append_duration_seconds",
			Help:    "Audit event append duration by outcome.",
			Buckets: []float64{0.0001, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		}, []string{"status"}),
		mcpCatalogRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "liki_agents_mcp_catalog_requests_total",
			Help: "MCP tool catalog resolutions by server and outcome.",
		}, []string{"server", "outcome"}),
		auditBatchEvents: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "liki_agents_audit_batch_events",
			Help:    "Audit events sharing one grouped commit.",
			Buckets: []float64{1, 2, 4, 8, 16, 32, 64},
		}, nil),
		auditQueueDepth: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "liki_agents_audit_queue_depth",
			Help: "Pending audit append requests.",
		}, nil),
		runRejections: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "liki_agents_run_rejections_total",
			Help: "Runs rejected before execution by reason.",
		}, []string{"reason"}),
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
		metrics.llmCallLatency,
		metrics.toolCalls,
		metrics.toolLatency,
		metrics.delegations,
		metrics.delegationLatency,
		metrics.dependencyUp,
		metrics.auditAppends,
		metrics.auditAppendLatency,
		metrics.mcpCatalogRequests,
		metrics.auditBatchEvents,
		metrics.auditQueueDepth,
		metrics.runRejections,
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

// ObserveLLMCallLatency records one model call's end-to-end duration.
func (m *Metrics) ObserveLLMCallLatency(model, status string, duration time.Duration) {
	m.llmCallLatency.WithLabelValues(model, status).Observe(duration.Seconds())
}

// ObserveAuditAppend records one audit persistence attempt and its outcome.
func (m *Metrics) ObserveAuditAppend(status string, duration time.Duration) {
	m.auditAppends.WithLabelValues(status).Inc()
	m.auditAppendLatency.WithLabelValues(status).Observe(duration.Seconds())
}

// ObserveMCPCatalog records one tool catalog resolution and its outcome.
func (m *Metrics) ObserveMCPCatalog(server, outcome string) {
	m.mcpCatalogRequests.WithLabelValues(server, outcome).Inc()
}

// ObserveAuditAppendBatch records how many events shared one grouped commit.
func (m *Metrics) ObserveAuditAppendBatch(eventsPerCommit int) {
	m.auditBatchEvents.WithLabelValues().Observe(float64(eventsPerCommit))
}

// ObserveAuditQueueDepth records the audit writer's pending backlog.
func (m *Metrics) ObserveAuditQueueDepth(depth int) {
	m.auditQueueDepth.WithLabelValues().Set(float64(depth))
}

// ObserveRunRejection records a run rejected before execution.
func (m *Metrics) ObserveRunRejection(reason string) {
	m.runRejections.WithLabelValues(reason).Inc()
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
