package agent

import (
	"testing"

	"github.com/ml8s/agentgo/internal/audit"
	"github.com/ml8s/agentgo/internal/execution"
)

func TestToolAuditorRecordsClientToolResolution(t *testing.T) {
	auditor, events, _, ctx := newToolAuditor(t)
	history := []execution.Message{
		{
			Role: execution.RoleAssistant,
			ToolCalls: []execution.ToolCall{{
				ID:   "call_client",
				Name: "render_ui",
				Args: []byte(`{"title":"agentgo"}`),
			}},
		},
		{
			Role:       execution.RoleTool,
			ToolCallID: "call_client",
			ToolOutput: []byte(`{"title":"agentgo"}`),
		},
	}
	err := auditor.RecordClientToolResolutions(ctx, newTestScope(), "main", []execution.ClientToolResolution{{
		InterruptID: "interrupt_1",
		ToolCallID:  "call_client",
		ToolName:    "render_ui",
		Status:      execution.ClientToolResolutionResolved,
	}}, history)
	if err != nil {
		t.Fatalf("RecordClientToolResolutions() error = %v", err)
	}
	completed := events.eventsOfType(audit.EventToolCallCompleted)
	if len(completed) != 1 || completed[0].Payload["mcp_server"] != "client" || completed[0].ToolName != "render_ui" {
		t.Fatalf("completed client tool events = %#v", completed)
	}
	if completed[0].Payload["input_digest"] == "" || completed[0].Payload["output_digest"] == "" {
		t.Fatalf("client tool audit lacks provenance: %#v", completed[0].Payload)
	}
}

func TestToolAuditorRecordsCancelledClientToolResolution(t *testing.T) {
	auditor, events, _, ctx := newToolAuditor(t)
	history := []execution.Message{
		{
			Role: execution.RoleAssistant,
			ToolCalls: []execution.ToolCall{{
				ID:   "call_client",
				Name: "render_ui",
				Args: []byte(`{"title":"agentgo"}`),
			}},
		},
		{
			Role:       execution.RoleTool,
			ToolCallID: "call_client",
			ToolOutput: []byte(`{}`),
		},
	}
	err := auditor.RecordClientToolResolutions(ctx, newTestScope(), "main", []execution.ClientToolResolution{{
		InterruptID: "interrupt_1",
		ToolCallID:  "call_client",
		ToolName:    "render_ui",
		Status:      execution.ClientToolResolutionCancelled,
	}}, history)
	if err != nil {
		t.Fatalf("RecordClientToolResolutions() error = %v", err)
	}
	failed := events.eventsOfType(audit.EventToolCallFailed)
	if len(failed) != 1 || failed[0].ErrorCode != "tool_execution_cancelled" {
		t.Fatalf("failed client tool events = %#v", failed)
	}
}
