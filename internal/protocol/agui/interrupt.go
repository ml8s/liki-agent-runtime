package agui

import (
	"encoding/json"
	"fmt"
	"time"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	aguitypes "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/types"
	"github.com/ml8s/agent-runtime/internal/execution"
)

// interruptTTL is the protocol hint carried on each interrupt. Durable
// enforcement belongs to the trusted protocol client that owns the pending
// decision, not to this stateless adapter.
const interruptTTL = 30 * time.Minute

// interruptEvents converts a runtime client-tool pause into the official AG-UI
// interrupt-aware terminal lifecycle. Messages are snapshotted first so a
// standard client can reconstruct the pending tool call after reconnect.
func interruptEvents(
	threadID string,
	runID string,
	pending []execution.PendingClientTool,
	messages []aguitypes.Message,
) ([]aguievents.Event, error) {
	if len(pending) == 0 {
		return nil, fmt.Errorf("interrupt terminal requires a pending client tool")
	}
	interrupts := make([]aguitypes.Interrupt, 0, len(pending))
	for index, call := range pending {
		interrupts = append(interrupts, aguitypes.Interrupt{
			ID:         fmt.Sprintf("%s:interrupt:%d", runID, index+1),
			Reason:     "tool_call",
			ToolCallID: call.ToolCallID,
			ExpiresAt:  time.Now().UTC().Add(interruptTTL).Format(time.RFC3339),
		})
	}

	snapshotMessages := append([]aguitypes.Message(nil), messages...)
	for _, call := range pending {
		var args any
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return nil, fmt.Errorf("decode pending tool args: %w", err)
		}
		encodedArgs, err := json.Marshal(args)
		if err != nil {
			return nil, fmt.Errorf("encode pending tool args: %w", err)
		}
		snapshotMessages = append(snapshotMessages, aguitypes.Message{
			ID:   runID + ":assistant:" + call.ToolCallID,
			Role: aguitypes.RoleAssistant,
			ToolCalls: []aguitypes.ToolCall{{
				ID:   call.ToolCallID,
				Type: aguitypes.ToolCallTypeFunction,
				Function: aguitypes.FunctionCall{
					Name:      call.ToolName,
					Arguments: string(encodedArgs),
				},
			}},
		})
	}
	messagesSnapshot := aguievents.NewMessagesSnapshotEvent(snapshotMessages)

	finished := aguievents.NewRunFinishedEventWithOptions(
		threadID,
		runID,
		aguievents.WithInterruptOutcome(interrupts),
	)
	return []aguievents.Event{messagesSnapshot, finished}, nil
}

func toolCallResultEvent(runID string, result toolResult) aguievents.Event {
	output := result.Output
	if len(output) == 0 || string(output) == "null" {
		output = []byte("{}")
	}
	if result.Error != "" {
		encoded, err := json.Marshal(map[string]any{"error": result.Error})
		if err == nil {
			output = encoded
		}
	}
	return aguievents.NewToolCallResultEvent(runID+":tools", result.ToolCallID, string(output))
}
