package a2a

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	deploymentpkg "github.com/ml8s/agent-runtime/internal/deployment"
	"github.com/ml8s/agent-runtime/internal/execution"
	"google.golang.org/adk/v2/server/adka2a/v2"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// agentPart maps native ADK parts onto A2A artifact parts. Model-text
// visibility is decided per event author by exposesText: plain Authors stream
// their text, structured or unknown Authors never emit raw model text. Only a
// structured entrypoint's validated final answer becomes a standard
// application/json data part; the user-facing text is the JSON field selected
// by its JSON Pointer, and no private metadata is carried on the wire.
func agentPart(
	event *session.Event,
	part *genai.Part,
	exposesText func(author string) bool,
	entrypoint *deploymentpkg.ResolvedAgentDefinition,
	structured *execution.OutputResult,
) (*a2a.Part, error) {
	if part == nil {
		return nil, nil
	}
	if part.FunctionCall != nil || part.FunctionResponse != nil {
		var longRunningToolIDs []string
		if event != nil {
			longRunningToolIDs = event.LongRunningToolIDs
		}
		return adka2a.ToA2APart(part, longRunningToolIDs)
	}
	if event != nil && exposesText != nil && exposesText(event.Author) {
		if strings.TrimSpace(part.Text) == "" {
			return nil, nil
		}
		var longRunningToolIDs []string
		if event != nil {
			longRunningToolIDs = event.LongRunningToolIDs
		}
		return adka2a.ToA2APart(part, longRunningToolIDs)
	}
	if event != nil && entrypoint != nil && event.Author == entrypoint.Name && event.IsFinalResponse() {
		value, ok := event.Actions.StateDelta[execution.StructuredOutputStateKey(entrypoint.Name)]
		if ok {
			var output execution.OutputResult
			if structured != nil {
				// The server already parsed this event once for a prior part.
				output = *structured
			} else {
				parsed, err := execution.ParseStructuredOutput(value, entrypoint)
				if err != nil {
					return nil, err
				}
				output = parsed
			}
			var data map[string]any
			if document, ok := output.Document.(map[string]any); ok {
				// The retained document is exactly what decoding the canonical
				// JSON with UseNumber would produce; reuse it without a second
				// decode of the same bytes.
				data = document
			} else {
				decoder := json.NewDecoder(bytes.NewReader(output.JSON))
				decoder.UseNumber()
				if err := decoder.Decode(&data); err != nil {
					return nil, fmt.Errorf("decode structured A2A data: %w", err)
				}
			}
			dataPart := a2a.NewDataPart(data)
			dataPart.MediaType = "application/json"
			return dataPart, nil
		}
	}
	return nil, nil
}
