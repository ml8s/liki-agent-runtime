package execution

import (
	"encoding/json"
	"strings"

	"google.golang.org/genai"
)

// BuildUserContent frames context and untrusted user text for the model request.
func BuildUserContent(request RunRequest) *genai.Content {
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
