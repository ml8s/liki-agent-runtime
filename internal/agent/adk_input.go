package agent

import (
	"encoding/json"
	"strings"

	"github.com/ml8s/agentgo/internal/execution"
	"google.golang.org/genai"
)

// BuildUserContent frames context and untrusted user text for the model request.
func BuildUserContent(request execution.RunRequest) *genai.Content {
	if request.UserMessage == "" && len(request.Resume) != 0 {
		return BuildResumeContent(request)
	}
	var prompt strings.Builder
	prompt.WriteString("<request_context>")
	if len(request.Context) != 0 && json.Valid(request.Context) {
		prompt.Write(request.Context)
	} else {
		prompt.WriteString("{}")
	}
	prompt.WriteString("</request_context>\n<user_message>")
	writeJSONString(&prompt, request.UserMessage)
	prompt.WriteString("</user_message>\n")
	return genai.NewContentFromText(prompt.String(), genai.RoleUser)
}

// BuildResumeContent frames a standard protocol resume without inventing user
// text. The authoritative tool responses are seeded as native function
// responses; this framing only tells the model why a continuation turn exists.
func BuildResumeContent(request execution.RunRequest) *genai.Content {
	resolutions := make([]map[string]any, 0, len(request.Resume))
	for _, resume := range request.Resume {
		resolutions = append(resolutions, map[string]any{
			"interrupt_id": resume.InterruptID,
			"tool_call_id": resume.ToolCallID,
			"status":       map[bool]string{true: "resolved", false: "cancelled"}[resume.Resolved],
		})
	}
	var prompt strings.Builder
	prompt.WriteString("<request_context>")
	if len(request.Context) != 0 && json.Valid(request.Context) {
		prompt.Write(request.Context)
	} else {
		prompt.WriteString("{}")
	}
	prompt.WriteString("</request_context>\n<client_tool_resume>")
	if encoded, err := json.Marshal(resolutions); err == nil {
		prompt.Write(encoded)
	} else {
		prompt.WriteString("[]")
	}
	prompt.WriteString("</client_tool_resume>\n")
	return genai.NewContentFromText(prompt.String(), genai.RoleUser)
}

func writeJSONString(prompt *strings.Builder, value string) {
	// json.Marshal handles quotes, backslashes, control characters, and UTF-8
	// escaping. Untrusted text therefore cannot close the surrounding framing.
	encoded, err := json.Marshal(value)
	if err != nil {
		// string marshalling is total; retain content rather than fail the run.
		prompt.WriteString(`"unable-to-encode-message"`)
		return
	}
	prompt.Write(encoded)
}
