// Package agui exposes the same ADK runtime as AG-UI over its official SSE
// event encoding. The browser-facing product gateway remains the trust boundary.
package agui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	aguitypes "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/types"
	aguisse "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/encoding/sse"
	"github.com/ml8s/liki-agent-runtime/internal/agent"
	"github.com/ml8s/liki-agent-runtime/internal/domain"
	"github.com/ml8s/liki-agent-runtime/internal/notify"
	"github.com/ml8s/liki-agent-runtime/internal/observability"
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
		config.RunTimeout = 10 * time.Minute
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
	if err := emit(aguievents.NewRunStartedEvent(threadID, runID)); err != nil {
		return
	}

	streamer := &stream{
		runID:      runID,
		entrypoint: h.entrypoint,
	}
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
		return streamer.consume(event, emit)
	})
	finishedAt := time.Now()
	if runErr == nil {
		runErr = streamer.finish(result.Text, emit)
	}
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		message, code := publicError(runErr)
		if err := streamer.failSubagents(message, code, emit); err != nil {
			return
		}
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
		if err := streamer.emitRunFinished(finished, emit); err != nil {
			runErr = err
		}
	}
	if runErr != nil && !errors.Is(runErr, context.Canceled) && !streamer.runFinished {
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

func (s *stream) emitRunFinished(
	event *aguievents.RunFinishedEvent,
	emit func(aguievents.Event) error,
) error {
	if s.runFinished {
		return nil
	}
	s.runFinished = true
	return emit(event)
}

// stream converts framework-native execution facts into official AG-UI events.
// It owns no alternative runtime or business state.
type stream struct {
	runID            string
	entrypoint       string
	subagents        []*subagentActivation
	nextID           int
	toolSequence     int
	toolIDs          map[string]string
	messageSequence  int
	textMessageID    string
	textSubagentID   string
	textActive       bool
	rootStreamedText bool
	runFinished      bool
}

type subagentActivation struct {
	id     string
	name   string
	branch string
}

func (s *stream) consume(event *session.Event, emit func(aguievents.Event) error) error {
	if err := s.observeSubagent(event, emit); err != nil {
		return err
	}
	if event == nil || event.Content == nil {
		return nil
	}
	subagentID := s.subagentID(event)
	if s.textActive && s.textSubagentID != subagentID {
		if err := s.closeText(emit); err != nil {
			return err
		}
	}
	for _, part := range event.Content.Parts {
		if part == nil || part.Text == "" || part.Thought {
			continue
		}
		if err := s.emitTextDelta(part.Text, subagentID, emit); err != nil {
			return err
		}
	}

	for _, part := range event.Content.Parts {
		if part == nil || part.FunctionCall == nil {
			continue
		}
		if err := s.closeText(emit); err != nil {
			return err
		}
		call := part.FunctionCall
		callID := s.createToolCallID(call.Name, call.ID)
		start := aguievents.NewToolCallStartEvent(callID, call.Name)
		start.SubagentRunID = subagentID
		if err := emit(start); err != nil {
			return err
		}
		args, err := json.Marshal(call.Args)
		if err != nil {
			args = []byte("{}")
		}
		delta := string(args)
		if delta == "" || delta == "null" {
			delta = "{}"
		}
		argsEvent := aguievents.NewToolCallArgsEvent(callID, delta)
		argsEvent.SubagentRunID = subagentID
		if err := emit(argsEvent); err != nil {
			return err
		}
		// AG-UI tool-call lifecycle: START -> ARGS -> END -> RESULT. END closes
		// the argument stream as soon as the call is specified, so a call whose
		// response never arrives is not left dangling.
		endEvent := aguievents.NewToolCallEndEvent(callID)
		endEvent.SubagentRunID = subagentID
		if err := emit(endEvent); err != nil {
			return err
		}
	}

	for _, part := range event.Content.Parts {
		if part == nil || part.FunctionResponse == nil {
			continue
		}
		response := part.FunctionResponse
		if err := s.closeText(emit); err != nil {
			return err
		}
		responseID := s.toolCallID(response.Name, response.ID)
		payload, err := json.Marshal(response.Response)
		if err != nil || len(payload) == 0 || string(payload) == "null" {
			payload = []byte("{}")
		}
		resultEvent := aguievents.NewToolCallResultEvent(s.runID+":tools", responseID, string(payload))
		resultEvent.SubagentRunID = subagentID
		if err := emit(resultEvent); err != nil {
			return err
		}
		delete(s.toolIDs, response.Name)
	}

	return nil
}

// finish closes an active message and synthesizes the standard text lifecycle
// when the model returned only a structured final answer.
func (s *stream) finish(content string, emit func(aguievents.Event) error) error {
	if err := s.finishSubagents(emit); err != nil {
		return err
	}
	if err := s.closeText(emit); err != nil {
		return err
	}
	if s.rootStreamedText {
		return nil
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	if err := s.openText("", emit); err != nil {
		return err
	}
	if err := emit(aguievents.NewTextMessageContentEvent(s.textMessageID, content)); err != nil {
		return err
	}
	return s.closeText(emit)
}

func (s *stream) openText(subagentID string, emit func(aguievents.Event) error) error {
	if s.textActive {
		return nil
	}
	s.messageSequence++
	s.textMessageID = fmt.Sprintf("%s:assistant:%d", s.runID, s.messageSequence)
	s.textSubagentID = subagentID
	s.textActive = true
	if subagentID == "" {
		s.rootStreamedText = true
	}
	start := aguievents.NewTextMessageStartEvent(
		s.textMessageID,
		aguievents.WithRole(string(aguitypes.RoleAssistant)),
	)
	start.SubagentRunID = subagentID
	start.Name = s.activationName(subagentID)
	return emit(start)
}

func (s *stream) emitTextDelta(delta, subagentID string, emit func(aguievents.Event) error) error {
	if err := s.openText(subagentID, emit); err != nil {
		return err
	}
	content := aguievents.NewTextMessageContentEvent(s.textMessageID, delta)
	content.SubagentRunID = subagentID
	return emit(content)
}

func (s *stream) closeText(emit func(aguievents.Event) error) error {
	if !s.textActive {
		return nil
	}
	end := aguievents.NewTextMessageEndEvent(s.textMessageID)
	end.SubagentRunID = s.textSubagentID
	if err := emit(end); err != nil {
		return err
	}
	s.textActive = false
	s.textSubagentID = ""
	return nil
}

func (s *stream) subagentID(event *session.Event) string {
	if event == nil || event.Author == "" || event.Author == s.entrypoint {
		return ""
	}
	branch := effectiveBranch(event)
	for index := len(s.subagents) - 1; index >= 0; index-- {
		if isBranchPrefix(s.subagents[index].branch, branch) {
			return s.subagents[index].id
		}
	}
	return ""
}

func (s *stream) activationName(subagentID string) string {
	for _, activation := range s.subagents {
		if activation.id == subagentID {
			return activation.name
		}
	}
	return ""
}

func (s *stream) observeSubagent(event *session.Event, emit func(aguievents.Event) error) error {
	if event == nil || event.Author == "" || event.Author == s.entrypoint {
		return s.finishDeeperThan(eventBranch(event), emit)
	}
	branch := effectiveBranch(event)
	if err := s.finishDeeperThan(branch, emit); err != nil {
		return err
	}
	if branch == "" {
		return nil
	}
	for _, activation := range s.subagents {
		if activation.branch == branch {
			return nil
		}
	}

	s.nextID++
	activation := &subagentActivation{
		id:     s.runID + ":subagent:" + strconv.Itoa(s.nextID),
		name:   event.Author,
		branch: branch,
	}
	started := aguievents.NewSubagentStartedEvent(activation.id, activation.name)
	started.ParentSubagentRunID = s.parentID(branch)
	s.subagents = append(s.subagents, activation)
	return emit(started)
}

func eventBranch(event *session.Event) string {
	if event == nil {
		return ""
	}
	return strings.TrimSpace(event.Branch)
}

// effectiveBranch resolves an event's attribution branch. Task-mode
// sub-agents inherit the parent's branch — empty at the root — so events from
// a non-entrypoint author without a branch fall back to the author name as
// their attribution key. The reserved "user" author carries synthesized
// function responses and is never a sub-agent.
func effectiveBranch(event *session.Event) string {
	if branch := eventBranch(event); branch != "" {
		return branch
	}
	if event == nil || event.Author == "" || event.Author == "user" {
		return ""
	}
	return event.Author
}

func (s *stream) finishDeeperThan(branch string, emit func(aguievents.Event) error) error {
	closed := 0
	for index := len(s.subagents) - 1; index >= 0; index-- {
		activation := s.subagents[index]
		if isBranchPrefix(activation.branch, branch) {
			break
		}
		closed++
		if err := emit(aguievents.NewSubagentFinishedEvent(activation.id)); err != nil {
			return err
		}
	}
	if closed == 0 {
		return nil
	}
	s.subagents = s.subagents[:len(s.subagents)-closed]
	return nil
}

func (s *stream) finishSubagents(emit func(aguievents.Event) error) error {
	for index := len(s.subagents) - 1; index >= 0; index-- {
		if err := emit(aguievents.NewSubagentFinishedEvent(s.subagents[index].id)); err != nil {
			return err
		}
	}
	s.subagents = nil
	return nil
}

func (s *stream) failSubagents(message, code string, emit func(aguievents.Event) error) error {
	for index := len(s.subagents) - 1; index >= 0; index-- {
		if err := emit(aguievents.NewSubagentErrorEvent(
			s.subagents[index].id,
			message,
			aguievents.WithSubagentErrorCode(code),
		)); err != nil {
			return err
		}
	}
	s.subagents = nil
	return nil
}

func (s *stream) parentID(branch string) string {
	parent := branch
	found := false
	if index := strings.LastIndex(branch, "."); index >= 0 {
		parent = branch[:index]
		found = true
	}
	if !found {
		return ""
	}
	for index := len(s.subagents) - 1; index >= 0; index-- {
		if s.subagents[index].branch == parent {
			return s.subagents[index].id
		}
	}
	return ""
}

func isBranchPrefix(prefix, branch string) bool {
	return prefix == branch || strings.HasPrefix(branch, prefix+".")
}

// validateTextChatProfile rejects capabilities this runtime does not consume.
// Explicit rejection prevents silent protocol data loss at the adapter edge.
func validateTextChatProfile(input aguitypes.RunAgentInput) error {
	if input.ParentRunID != nil {
		return fmt.Errorf("parent runs are outside the AG-UI text-chat profile")
	}
	if !isEmptyClientState(input.State) {
		return fmt.Errorf("client state is outside the AG-UI text-chat profile")
	}
	if len(input.Tools) != 0 {
		return fmt.Errorf("client tools are outside the AG-UI text-chat profile")
	}
	if len(input.Context) != 0 {
		return fmt.Errorf("client context is outside the AG-UI text-chat profile")
	}
	if len(input.Resume) != 0 {
		return fmt.Errorf("resume is outside the AG-UI text-chat profile")
	}
	for _, message := range input.Messages {
		switch message.Role {
		case aguitypes.RoleUser, aguitypes.RoleAssistant:
		default:
			return fmt.Errorf("message role %q is outside the AG-UI text-chat profile", message.Role)
		}
		if len(message.ToolCalls) != 0 || message.ToolCallID != "" || message.ActivityType != "" || message.SubagentRunID != "" {
			return fmt.Errorf("tool or activity messages are outside the AG-UI text-chat profile")
		}
	}
	return nil
}

func isEmptyClientState(state any) bool {
	if state == nil {
		return true
	}
	switch value := state.(type) {
	case map[string]any:
		return len(value) == 0
	case []any:
		return len(value) == 0
	case string:
		return value == ""
	default:
		return false
	}
}

func lastUserConversation(messages []aguitypes.Message) (string, []agent.Message, error) {
	lastIndex := -1
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == aguitypes.RoleUser {
			lastIndex = index
			break
		}
	}
	if lastIndex < 0 {
		return "", nil, fmt.Errorf("AG-UI request must contain a user message")
	}
	content, err := conversationText(messages[lastIndex].Content)
	if err != nil {
		return "", nil, err
	}
	if strings.TrimSpace(content) == "" {
		return "", nil, fmt.Errorf("the latest user message is empty")
	}

	history := make([]agent.Message, 0, len(messages)-1)
	for index, message := range messages {
		if index == lastIndex || message.Role != aguitypes.RoleUser && message.Role != aguitypes.RoleAssistant {
			continue
		}
		text, err := conversationText(message.Content)
		if err != nil {
			return "", nil, err
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		role := agent.RoleUser
		if message.Role == aguitypes.RoleAssistant {
			role = agent.RoleAssistant
		}
		history = append(history, agent.Message{Role: role, Content: text})
	}
	return content, history, nil
}

func conversationText(content any) (string, error) {
	switch value := content.(type) {
	case string:
		return value, nil
	case []aguitypes.InputContent:
		var result strings.Builder
		for _, fragment := range value {
			if fragment.Type != aguitypes.InputContentTypeText {
				return "", fmt.Errorf("input content type %q is outside the AG-UI text-chat profile", fragment.Type)
			}
			result.WriteString(fragment.Text)
		}
		return result.String(), nil
	case []any:
		var result strings.Builder
		for _, item := range value {
			fragment, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if kind, ok := fragment["type"].(string); ok && kind != aguitypes.InputContentTypeText {
				return "", fmt.Errorf("input content type %q is outside the AG-UI text-chat profile", kind)
			}
			if text, ok := fragment["text"].(string); ok {
				result.WriteString(text)
			}
		}
		return result.String(), nil
	default:
		return "", fmt.Errorf("message content is outside the AG-UI text-chat profile")
	}
}

func opaqueJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil || bytes.Equal(raw, []byte("null")) {
		return json.RawMessage("{}")
	}
	return raw
}

func (s *stream) createToolCallID(toolName, id string) string {
	if id != "" {
		return id
	}
	if s.toolIDs == nil {
		s.toolIDs = make(map[string]string)
	}
	if existing, ok := s.toolIDs[toolName]; ok {
		return existing
	}
	s.toolSequence++
	generated := fmt.Sprintf("%s:tools:%s:%d", s.runID, toolName, s.toolSequence)
	s.toolIDs[toolName] = generated
	return generated
}

func (s *stream) toolCallID(toolName, id string) string {
	if id != "" {
		return id
	}
	return s.toolIDs[toolName]
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
