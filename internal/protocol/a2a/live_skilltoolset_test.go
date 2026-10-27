package a2a

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	a2atypes "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
)

// TestLiveA2ASkillToolsetTrace drives a real LLM run against a running
// assembly stack to observe skilltoolset progressive loading. Skipped unless
// AGENT_RUNTIME_A2A_LIVE=1 and AGENT_RUNTIME_A2A_HOST is set (e.g. 172.20.0.5:8083).
func TestLiveA2ASkillToolsetTrace(t *testing.T) {
	if os.Getenv("AGENT_RUNTIME_A2A_LIVE") != "1" {
		t.Skip("set AGENT_RUNTIME_A2A_LIVE=1 to run against a live assembly stack")
	}
	host := os.Getenv("AGENT_RUNTIME_A2A_HOST")
	if host == "" {
		t.Fatal("AGENT_RUNTIME_A2A_HOST required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	resp, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+host+"/.well-known/agent-card.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	httpResp, err := http.DefaultClient.Do(resp)
	if err != nil {
		t.Fatalf("fetch card: %v", err)
	}
	defer func() { _ = httpResp.Body.Close() }()
	body, _ := io.ReadAll(httpResp.Body)
	var card a2atypes.AgentCard
	if err := json.Unmarshal(body, &card); err != nil {
		t.Fatalf("decode card: %v (%s)", err, body)
	}
	t.Logf("card: name=%s interfaces=%d", card.Name, len(card.SupportedInterfaces))
	// Rewrite reachable URLs (card advertises compose DNS).
	for i := range card.SupportedInterfaces {
		card.SupportedInterfaces[i].URL = "http://" + host + "/a2a"
	}
	hc := &http.Client{Transport: &bearerTransport{token: os.Getenv("AGENT_RUNTIME_A2A_TOKEN")}}
	client, err := a2aclient.NewFromCard(ctx, &card, a2aclient.WithJSONRPCTransport(hc))
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	question := os.Getenv("AGENT_RUNTIME_A2A_QUESTION")
	if question == "" {
		question = "我1990年3月5日早上8点出生，帮我看事业财运"
	}
	started := time.Now()
	result, err := client.SendMessage(ctx, &a2atypes.SendMessageRequest{
		Message: a2atypes.NewMessage(a2atypes.MessageRoleUser, a2atypes.NewTextPart(question)),
	})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	task, ok := result.(*a2atypes.Task)
	if !ok || len(task.Artifacts) == 0 {
		t.Fatalf("result = %#v", result)
	}
	text := ""
	for _, part := range task.Artifacts[0].Parts {
		if s, ok := part.Content.(a2atypes.Text); ok {
			text += string(s)
		}
	}
	t.Logf("state=%s elapsed=%s answer_len=%d", task.Status.State, elapsed, len(text))
	t.Logf("answer(full): %s", text)
	// Dump usage/metadata keys for token evidence.
	meta, _ := json.Marshal(task.Metadata)
	t.Logf("task metadata: %s", meta)
	fmt.Printf("LIVE_RUN elapsed=%s answer_len=%d\n", elapsed, len(text))
}

type bearerTransport struct{ token string }

func (b *bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}
