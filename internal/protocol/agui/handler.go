// Package agui exposes the same ADK runtime as AG-UI over its official SSE
// event encoding. The browser-facing product gateway remains the trust boundary.
package agui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	aguitypes "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/types"
	aguisse "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/encoding/sse"
	"github.com/ml8s/liki-agent-runtime/internal/agent"
	"github.com/ml8s/liki-agent-runtime/internal/domain"
	"github.com/ml8s/liki-agent-runtime/internal/notify"
	"github.com/ml8s/liki-agent-runtime/internal/observability"
	platformconfig "github.com/ml8s/liki-agent-runtime/internal/platform/config"
	"github.com/ml8s/liki-agent-runtime/internal/platform/identity"
	"google.golang.org/adk/v2/session"
)

// Config owns the AG-UI adapter settings.
type Config struct {
	RunTimeout time.Duration
	Metrics    observability.ProtocolMetrics
	Notifier   CompletionNotifier
}

// AG-UI request-validation error codes. They are raised before a run starts,
// so they never enter audit evidence; as adapter-owned contract violations
// they live here instead of the domain code registry. Values are stable.
const (
	CodeInvalidAGUIRequest     = "invalid_agui_request"
	CodeInvalidAGUIMessage     = "invalid_agui_message"
	CodeUnsupportedAGUIFeature = "unsupported_agui_feature"
)

// CompletionNotifier receives one record per terminal product run. It is
// implemented by *notify.Notifier; nil disables notifications.
type CompletionNotifier interface {
	Notify(notify.Record)
}

// Handler is the AG-UI protocol adapter.
type Handler struct {
	runtime    runtime
	runTimeout time.Duration
	metrics    observability.ProtocolMetrics
	notifier   CompletionNotifier
	entrypoint string
	writer     *aguisse.SSEWriter
}

// runtime is the single ADK runtime dependency consumed by this protocol adapter.
type runtime interface {
	Run(ctx context.Context, request agent.RunRequest, observe func(*session.Event) error) (agent.RunResult, error)
	Entrypoint() *agent.AgentDefinition
}

// New creates the standard AG-UI HTTP handler.
func New(runtime runtime, config Config) (*Handler, error) {
	if runtime == nil {
		return nil, fmt.Errorf("adk runtime is required")
	}
	if config.RunTimeout <= 0 {
		config.RunTimeout = platformconfig.DefaultRunTimeout
	}
	entrypoint := runtime.Entrypoint()
	if entrypoint == nil || entrypoint.Name == "" {
		return nil, fmt.Errorf("runtime entrypoint is required")
	}
	return &Handler{
		runtime:    runtime,
		runTimeout: config.RunTimeout,
		metrics:    config.Metrics,
		notifier:   config.Notifier,
		entrypoint: entrypoint.Name,
		writer:     aguisse.NewSSEWriter(),
	}, nil
}

// ServeHTTP implements POST /ag-ui and AG-UI SSE responses.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeProtocolError(w, http.StatusMethodNotAllowed, domain.CodeMethodNotAllowed, "AG-UI requires POST")
		return
	}

	var input aguitypes.RunAgentInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeProtocolError(w, http.StatusBadRequest, CodeInvalidAGUIRequest, "invalid AG-UI request body")
		return
	}

	threadID := strings.TrimSpace(input.ThreadID)
	if threadID == "" {
		threadID = aguievents.GenerateThreadID()
	}
	runID := strings.TrimSpace(input.RunID)
	if runID == "" {
		runID = aguievents.GenerateRunID()
	}
	callerIdentity, _ := identity.FromContext(r.Context())
	caller := callerIdentity.UserID
	if !agent.ValidIdentifier(threadID) || !agent.ValidIdentifier(runID) {
		writeProtocolError(w, http.StatusUnprocessableEntity, CodeInvalidAGUIMessage, "thread and run identifiers must contain at most 128 printable bytes")
		return
	}
	if err := validateTextChatProfile(input); err != nil {
		writeProtocolError(w, http.StatusUnprocessableEntity, CodeUnsupportedAGUIFeature, err.Error())
		return
	}
	content, history, err := lastUserConversation(input.Messages)
	if err != nil {
		writeProtocolError(w, http.StatusUnprocessableEntity, CodeInvalidAGUIMessage, err.Error())
		return
	}
	if len(input.Messages) > agent.MaxHistoryMessages+1 {
		writeProtocolError(w, http.StatusUnprocessableEntity, CodeInvalidAGUIMessage, fmt.Sprintf("AG-UI request exceeds %d history messages", agent.MaxHistoryMessages))
		return
	}

	if h.metrics != nil {
		h.metrics.StreamOpened("ag_ui")
		defer h.metrics.StreamClosed("ag_ui")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	emit := func(event aguievents.Event) error {
		if err := event.Validate(); err != nil {
			return fmt.Errorf("AG-UI event is invalid: %w", err)
		}
		return h.writer.WriteEvent(r.Context(), w, event)
	}
	emitAll := func(events []aguievents.Event) error {
		for _, event := range events {
			if err := emit(event); err != nil {
				return err
			}
		}
		return nil
	}
	if err := emit(aguievents.NewRunStartedEvent(threadID, runID)); err != nil {
		return
	}

	streamer := newStream(streamConfig{
		runID:      runID,
		entrypoint: h.entrypoint,
	})
	spec := agent.RunRequest{
		RunID:       runID,
		ThreadID:    threadID,
		UserID:      caller,
		Protocol:    "ag_ui",
		UserMessage: content,
		History:     history,
		Context:     opaqueJSON(input.ForwardedProps),
	}
	runCtx, cancelRun := context.WithTimeout(r.Context(), h.runTimeout)
	defer cancelRun()
	startedAt := time.Now()
	result, runErr := h.runtime.Run(runCtx, spec, func(event *session.Event) error {
		next, outgoing := streamer.consume(event)
		if err := emitAll(outgoing); err != nil {
			return err
		}
		streamer = next
		return nil
	})
	finishedAt := time.Now()
	if runErr == nil {
		next, outgoing := streamer.finish(result.Text)
		runErr = emitAll(outgoing)
		if runErr == nil {
			streamer = next
		}
	}
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		message, code := publicError(runErr)
		next, outgoing := streamer.failSubagents(message, code)
		if err := emitAll(outgoing); err != nil {
			return
		}
		streamer = next
	}
	if runErr == nil {
		// 协议契约：RUN_FINISHED 必须带成功 outcome（与中继网关一致）；
		// 缺失 outcome 会被 AG-UI 客户端判成非成功结果（result 为 undefined）。
		finished := aguievents.NewRunFinishedEventWithOptions(
			threadID,
			runID,
			aguievents.WithSuccessOutcome(),
		)
		finished.Result = map[string]any{
			"definition": result.Definition,
			"output":     json.RawMessage(result.Output),
			"answer":     result.Text,
		}
		next, outgoing := streamer.finishRun(finished)
		if err := emitAll(outgoing); err != nil {
			runErr = err
		}
		// RUN_FINISHED is terminal even if its SSE write fails; the
		// protocol forbids RUN_ERROR after a terminal success event.
		streamer = next
	}
	if runErr != nil && !errors.Is(runErr, context.Canceled) && !streamer.state.runFinished {
		message, code := publicError(runErr)
		_ = emit(aguievents.NewRunErrorEvent(message, aguievents.WithErrorCode(code), aguievents.WithRunID(runID)))
	}
	h.notifyCompletion(spec, result, runErr, r.Context().Err(), callerIdentity.MutualTLS, startedAt, finishedAt)
}

// notifyCompletion delivers one terminal run record to the control plane. The
// terminal state derives once from the run result and the request context
// (client disconnect) and reuses the A2A TaskState vocabulary.
//
// Only runs that arrived over the mutually authenticated listener notify: the
// callback is the direct-connect persistence path, while relay runs are
// persisted by liki-web itself. Notifying relay runs too would write every
// exchange twice (the relay is the rollback lever and may carry traffic while
// the callback secret is already configured).
func (h *Handler) notifyCompletion(
	spec agent.RunRequest,
	result agent.RunResult,
	runErr error,
	clientErr error,
	direct bool,
	startedAt time.Time,
	finishedAt time.Time,
) {
	if h.notifier == nil || !direct {
		return
	}
	status := "completed"
	switch {
	case errors.Is(runErr, context.Canceled) || errors.Is(clientErr, context.Canceled):
		status = "canceled"
	case runErr != nil:
		status = "failed"
	}
	record := notify.Record{
		RunID:       spec.RunID,
		ThreadID:    spec.ThreadID,
		UserID:      spec.UserID,
		UserMessage: spec.UserMessage,
		Status:      status,
		Answer:      result.Text,
		Usage: notify.Usage{
			LLMCalls:         result.Usage.LLMCalls,
			PromptTokens:     result.Usage.PromptTokens,
			CompletionTokens: result.Usage.CompletionTokens,
			ThoughtTokens:    result.Usage.ThoughtTokens,
		},
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
	}
	if status == "failed" && runErr != nil {
		record.Error = runErr.Error()
	}
	h.notifier.Notify(record)
}

func publicError(err error) (message, code string) {
	var domainErr *domain.Error
	if errors.As(err, &domainErr) {
		message := domainErr.Message
		if message == "" {
			message = domainErr.Code
		}
		return message, domainErr.Code
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "agent run exceeded its deadline", domain.CodeRuntimeTimeout
	}
	return "agent run failed", domain.CodeRuntimeFailed
}

func writeProtocolError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
	w.WriteHeader(status)
	// RFC 9457 problem+json payload with the runtime's stable code in the
	// standard extension field.
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":   "about:blank",
		"title":  http.StatusText(status),
		"status": status,
		"code":   code,
		"detail": message,
	})
}
