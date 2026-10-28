package a2a_test

import (
	"encoding/json"
	"flag"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

var updateAgentCardSnapshot = flag.Bool("update-agent-card-snapshot", false, "rewrite the Agent Card contract snapshot")

func TestAgentCardStandardContractSnapshot(t *testing.T) {
	publicURL, err := url.Parse("https://agent.internal")
	if err != nil {
		t.Fatal(err)
	}
	server, _, err := newA2AServer(t, publicURL)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	response := httptest.NewRecorder()
	server.AgentCardHandler().ServeHTTP(response, httptest.NewRequest("GET", "/.well-known/agent-card.json", nil))
	if response.Code != 200 {
		t.Fatalf("Agent Card status = %d", response.Code)
	}

	var actual map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
		t.Fatalf("decode Agent Card: %v", err)
	}
	normalizeSnapshotURLs("", actual)
	canonical, err := json.MarshalIndent(actual, "", "  ")
	if err != nil {
		t.Fatalf("encode Agent Card snapshot: %v", err)
	}
	canonical = append(canonical, '\n')

	path := filepath.Join("testdata", "agent_card.snapshot.json")
	if *updateAgentCardSnapshot {
		if err := os.WriteFile(path, canonical, 0o600); err != nil {
			t.Fatalf("update Agent Card snapshot: %v", err)
		}
		t.Log("updated Agent Card snapshot")
		return
	}
	expected, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Agent Card snapshot: %v", err)
	}
	if string(expected) != string(canonical) {
		t.Fatalf("Agent Card snapshot changed\n--- want\n%s\n--- got\n%s", expected, canonical)
	}
}

func normalizeSnapshotURLs(prefix string, value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			if path == "SupportedInterfaces.0.url" {
				typed[key] = "https://agent.internal/a2a"
				continue
			}
			normalizeSnapshotURLs(path, child)
		}
	case []any:
		for index, child := range typed {
			normalizeSnapshotURLs(prefix+"."+strconv.Itoa(index), child)
		}
	}
}
