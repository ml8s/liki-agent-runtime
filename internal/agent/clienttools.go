package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/ml8s/agent-runtime/internal/execution"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// ClientToolServer identifies AG-UI client tools in execution evidence. They
// are not MCP tools: the product gateway declares them and the client owns
// their product behavior. The runtime only acknowledges the call.
const ClientToolServer = "client"

type clientToolsKey struct{}

// WithClientTools makes this run's AG-UI client tool declarations available to
// the shared ADK graph. Declarations are run-scoped and never become durable
// deployment state.
func WithClientTools(ctx context.Context, tools []execution.ClientTool) context.Context {
	if len(tools) == 0 {
		return ctx
	}
	return context.WithValue(ctx, clientToolsKey{}, tools)
}

func clientToolsFromContext(ctx context.Context) []execution.ClientTool {
	tools, _ := ctx.Value(clientToolsKey{}).([]execution.ClientTool)
	return tools
}

func clientToolFromContext(ctx context.Context, name string) (execution.ClientTool, bool) {
	for _, declared := range clientToolsFromContext(ctx) {
		if declared.Name == name {
			return declared, true
		}
	}
	return execution.ClientTool{}, false
}

// clientToolset adapts per-run AG-UI client tool declarations to the ADK's
// standard function-tool interface. Every handler immediately acknowledges the
// call; AG-UI protocol events remain the product-facing result.
type clientToolset struct{}

func newClientToolset() *clientToolset {
	return &clientToolset{}
}

func (*clientToolset) Name() string {
	return ClientToolServer
}

func (*clientToolset) Tools(ctx adkagent.ReadonlyContext) ([]tool.Tool, error) {
	declarations := clientToolsFromContext(ctx)
	if len(declarations) == 0 {
		return nil, nil
	}

	tools := make([]tool.Tool, 0, len(declarations))
	for _, declared := range declarations {
		inputSchema := &jsonschema.Schema{Type: "object"}
		if len(declared.Parameters) != 0 {
			if err := json.Unmarshal(declared.Parameters, inputSchema); err != nil {
				return nil, fmt.Errorf("decode client tool %q schema: %w", declared.Name, err)
			}
		}

		clientTool, err := functiontool.New(functiontool.Config{
			Name:        declared.Name,
			Description: declared.Description,
			InputSchema: inputSchema,
			OutputSchema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"status": {Type: "string"},
				},
				Required: []string{"status"},
			},
		}, func(_ adkagent.Context, _ map[string]any) (map[string]any, error) {
			return map[string]any{"status": "forwarded_to_client"}, nil
		})
		if err != nil {
			return nil, fmt.Errorf("create client tool %q: %w", declared.Name, err)
		}
		tools = append(tools, clientTool)
	}
	return tools, nil
}
