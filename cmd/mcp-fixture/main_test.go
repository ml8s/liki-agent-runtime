package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestFixtureUsesOfficialStreamableMCP(t *testing.T) {
	config := fixtureConfig{
		serverName: "tools-a",
		prefix:     "tool_a",
		count:      4,
	}
	httpServer := httptest.NewServer(newHandler(config))
	t.Cleanup(httpServer.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "agent-runtime-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             httpServer.URL + "/mcp",
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect official MCP client: %v", err)
	}
	defer func() { _ = session.Close() }()

	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	want := []string{"tool_a_1", "tool_a_2", "tool_a_3", "tool_a_4"}
	if len(listed.Tools) != len(want) {
		t.Fatalf("tools/list returned %d tools, want %d", len(listed.Tools), len(want))
	}
	for index, tool := range listed.Tools {
		if tool.Name != want[index] {
			t.Fatalf("tools/list[%d] = %q, want %q", index, tool.Name, want[index])
		}
	}

	called, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "tool_a_2",
		Arguments: map[string]any{"value": "fixture"},
	})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if called.IsError {
		t.Fatalf("tools/call returned protocol error: %+v", called)
	}
	raw, err := json.Marshal(called.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured result: %v", err)
	}
	var output fixtureOutput
	if err := json.Unmarshal(raw, &output); err != nil {
		t.Fatalf("decode structured result %s: %v", raw, err)
	}
	wantOutput := fixtureOutput{Tool: "tool_a_2", Server: "tools-a", Value: "fixture"}
	if output != wantOutput {
		t.Fatalf("structured result = %+v, want %+v", output, wantOutput)
	}
}

func TestFixtureConfigFailsClosed(t *testing.T) {
	env := map[string]string{
		"MCP_FIXTURE_SERVER_NAME": "tools-b",
		"MCP_FIXTURE_PREFIX":      "tool-b",
		"MCP_FIXTURE_COUNT":       "3",
	}
	if _, err := loadConfig(func(key string) string { return env[key] }); err == nil {
		t.Fatal("loadConfig() error = nil, want invalid prefix")
	}
	env["MCP_FIXTURE_PREFIX"] = "tool_b"
	if _, err := loadConfig(func(key string) string { return env[key] }); err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	env["MCP_FIXTURE_COUNT"] = "33"
	if _, err := loadConfig(func(key string) string { return env[key] }); err == nil {
		t.Fatal("loadConfig() error = nil, want count above maximum")
	}
}

func TestFixtureHealthEndpoint(t *testing.T) {
	server := httptest.NewServer(newHandler(fixtureConfig{serverName: "tools-a", prefix: "tool_a", count: 1}))
	t.Cleanup(server.Close)
	response, err := http.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /healthz status = %d, want %d", response.StatusCode, http.StatusOK)
	}
}
