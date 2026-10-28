package agent

import (
	"context"

	"github.com/ml8s/agent-runtime/internal/execution"

	"github.com/ml8s/agent-runtime/internal/domain"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// seedConversationHistory rebuilds the caller-owned conversation history as
// native multi-turn events in the run-scoped ADK session before the runner
// appends the current turn.
//
// This replaces embedding history as a JSON array inside one user message:
// the model now receives chronological user/model turns, the latest message
// lands last, and providers can prefix-cache every earlier turn. Invariants:
//
//   - the session stays run-scoped and is deleted at terminal execution, so
//     the runtime never becomes a second durable conversation store;
//   - assistant turns are authored by the entrypoint Agent so ADK's history
//     filter keeps them as native model turns (any other author would be
//     downgraded to a "For context:" user note);
//   - events carry the root branch and no isolation scope, matching the root
//     chat invocation's visibility rules;
//   - untrusted history text stays opaque inside its own structural content
//     part; native turns have no textual framing to break out of.
func (r *Runtime) seedConversationHistory(ctx context.Context, service session.Service, request execution.RunRequest, sessionID string) error {
	if len(request.History) == 0 {
		return nil
	}
	response, err := service.Create(ctx, &session.CreateRequest{
		AppName:   r.config.AppName,
		UserID:    request.UserID,
		SessionID: sessionID,
	})
	if err != nil {
		return domain.NewError(domain.CodeRuntimeSessionUnavailable, "create run session for history", err)
	}
	if response == nil || response.Session == nil {
		return domain.NewError(domain.CodeRuntimeSessionUnavailable, "create run session for history", nil)
	}
	for _, message := range request.History {
		role := genai.Role(genai.RoleUser)
		author := "user"
		if message.Role == execution.RoleAssistant {
			role = genai.Role(genai.RoleModel)
			author = r.entrypoint.Name
		}
		event := &session.Event{
			Author: author,
			// Root-branch visibility and no isolation scope match the root
			// chat invocation; ADK drops partial events on append, so the
			// events must stay non-partial.
			Branch:         "",
			IsolationScope: "",
			LLMResponse: model.LLMResponse{
				Content: genai.NewContentFromText(message.Content, role),
			},
		}
		if appendErr := service.AppendEvent(ctx, response.Session, event); appendErr != nil {
			return domain.NewError(domain.CodeRuntimeSessionUnavailable, "seed conversation history", appendErr)
		}
	}
	return nil
}
