package agent

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"

	"github.com/ml8s/liki-agent-runtime/internal/domain"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

type capturingLLM struct {
	request  *model.LLMRequest
	response *model.LLMResponse
}

func (m *capturingLLM) Name() string { return "capturing-model" }

func (m *capturingLLM) GenerateContent(
	_ context.Context,
	req *model.LLMRequest,
	_ bool,
) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.request = req
		yield(m.response, nil)
	}
}

func TestJSONObjectModelKeepsStructuredSemanticsOnJSONMode(t *testing.T) {
	inner := &capturingLLM{response: &model.LLMResponse{
		Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "{}"}}},
	}}
	agentDeployment := NewTestDeployment(t)
	entrypoint, err := agentDeployment.EntrypointDefinition()
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := newJSONObjectModel(inner)
	if err != nil {
		t.Fatalf("newJSONObjectModel() error = %v", err)
	}
	if adapter.Name() != inner.Name() {
		t.Fatalf("Name() = %q, want %q", adapter.Name(), inner.Name())
	}
	schema := mustRuntimeAgentDefinition(t, *entrypoint).GenaiOutputSchema
	request := &model.LLMRequest{
		Model: "glm-5.3-flash",
		Config: &genai.GenerateContentConfig{
			SystemInstruction: genai.NewContentFromText("You are a generic test agent.", systemRole),
			ResponseSchema:    schema,
			ResponseMIMEType:  "application/json",
		},
	}

	responses := 0
	for response, err := range adapter.GenerateContent(context.Background(), request, false) {
		if err != nil {
			t.Fatalf("GenerateContent() error = %v", err)
		}
		if response == nil {
			t.Fatal("GenerateContent() response = nil")
		}
		responses++
	}
	if responses != 1 {
		t.Fatalf("responses = %d, want 1", responses)
	}

	if request.Config.ResponseSchema != schema || request.Config.ResponseMIMEType != "application/json" {
		t.Fatal("adapter mutated the caller request")
	}
	adapted := inner.request
	if adapted == nil || adapted.Config == nil {
		t.Fatal("delegate did not receive adapted request config")
	}
	if adapted.Config.ResponseSchema != nil || adapted.Config.ResponseJsonSchema != nil {
		t.Fatal("provider-unsupported response schema was not removed")
	}
	if adapted.Config.ResponseMIMEType != "application/json" {
		t.Fatalf("response MIME type = %q, want application/json", adapted.Config.ResponseMIMEType)
	}
	var instruction strings.Builder
	for _, part := range adapted.Config.SystemInstruction.Parts {
		if part != nil && part.Text != "" {
			instruction.WriteString(part.Text)
		}
	}
	if !strings.Contains(instruction.String(), "You are a generic test agent.") {
		t.Fatal("existing system instruction was lost")
	}
	for _, marker := range []string{"answer"} {
		if !strings.Contains(instruction.String(), marker) {
			t.Fatalf("injected schema missing %q: %q", marker, instruction.String())
		}
	}
	if !strings.Contains(instruction.String(), structuredOutputInstructionPrefix) {
		t.Fatal("JSON-only output instruction was not injected")
	}
}

func TestJSONObjectModelLeavesPlainRequestUnchanged(t *testing.T) {
	inner := &capturingLLM{response: &model.LLMResponse{
		Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "hello"}}},
	}}
	adapter, err := newJSONObjectModel(inner)
	if err != nil {
		t.Fatalf("newJSONObjectModel() error = %v", err)
	}
	config := &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText("You are a generic test agent.", systemRole),
	}
	request := &model.LLMRequest{Model: "glm-5.3-flash", Config: config}
	for _, err := range adapter.GenerateContent(context.Background(), request, false) {
		if err != nil {
			t.Fatalf("GenerateContent() error = %v", err)
		}
	}
	if inner.request != request {
		t.Fatal("plain request was copied or changed")
	}
	if inner.request.Config.ResponseMIMEType == "application/json" {
		t.Fatal("plain request was converted to JSON mode")
	}
	if strings.Contains(inner.request.Config.SystemInstruction.Parts[len(inner.request.Config.SystemInstruction.Parts)-1].Text, structuredOutputInstructionPrefix) {
		t.Fatal("schema instruction was injected without a response schema")
	}
}

func TestJSONObjectModelRejectsInvalidConstruction(t *testing.T) {
	validModel := &capturingLLM{}
	testCases := []struct {
		name    string
		inner   model.LLM
		wantErr error
	}{
		{name: "nil delegate", inner: nil, wantErr: domain.ErrInvalidInput},
		{name: "valid delegate", inner: validModel, wantErr: nil},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := newJSONObjectModel(testCase.inner)
			if testCase.wantErr == nil {
				if err != nil {
					t.Fatalf("newJSONObjectModel() error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("newJSONObjectModel() error = %v, want %v", err, testCase.wantErr)
			}
		})
	}
}

func TestJSONObjectModelNormalizesCommentaryAroundJSONPayload(t *testing.T) {
	payload := `{"answer":"analysis shows a tendency","confidence":0.8}`
	inner := &capturingLLM{response: &model.LLMResponse{
		Content: &genai.Content{Role: "model", Parts: []*genai.Part{
			{Text: "I have the execution facts now.", Thought: true},
			{Text: "以下是结构化结果：\n" + payload + "\n谢谢。"},
		}},
	}}
	agentDeployment := NewTestDeployment(t)
	entrypoint, err := agentDeployment.EntrypointDefinition()
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := newJSONObjectModel(inner)
	if err != nil {
		t.Fatalf("newJSONObjectModel() error = %v", err)
	}
	request := &model.LLMRequest{Config: &genai.GenerateContentConfig{ResponseSchema: mustRuntimeAgentDefinition(t, *entrypoint).GenaiOutputSchema}}
	var normalizedResponse *model.LLMResponse
	for response, err := range adapter.GenerateContent(context.Background(), request, false) {
		if err != nil {
			t.Fatalf("GenerateContent() error = %v", err)
		}
		normalizedResponse = response
	}
	response := normalizedResponse
	if response == nil || response.Content == nil {
		t.Fatal("delegate response was missing")
	}
	var payloadParts []*genai.Part
	for _, part := range response.Content.Parts {
		if part != nil && part.Text != "" && !part.Thought {
			payloadParts = append(payloadParts, part)
		}
	}
	if len(payloadParts) != 1 || payloadParts[0].Text != payload {
		t.Fatalf("payload parts = %#v, want one normalized JSON part", payloadParts)
	}
}

func TestExtractJSONObjectIgnoresBracesInStrings(t *testing.T) {
	payload := `{"answer":"包含 { braces } 和\"引号\"。","confidence":0.7,"topic":"general","key_factors":[],"limitations":[]}`
	raw := "说明\n" + payload + "\n结尾"
	got, ok := extractJSONObject(raw)
	if !ok || got != payload {
		t.Fatalf("extractJSONObject() = %q, %v", got, ok)
	}
	if _, ok := extractJSONObject("没有对象"); ok {
		t.Fatal("extractJSONObject() accepted text without a JSON object")
	}
}
