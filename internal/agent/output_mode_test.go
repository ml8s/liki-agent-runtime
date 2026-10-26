package agent

import (
	"github.com/ml8s/liki-agent-runtime/internal/execution"
)

import "testing"

func TestDefaultStructuredOutputMode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		provider string
		want     string
	}{
		{provider: "openai", want: execution.StructuredOutputJSONSchema},
		{provider: "zhipu", want: execution.StructuredOutputJSONObject},
		{provider: "bigmodel", want: execution.StructuredOutputJSONObject},
		{provider: "glm", want: execution.StructuredOutputJSONObject},
		{provider: "", want: execution.StructuredOutputJSONObject},
	}
	for _, test := range tests {
		if got := execution.DefaultStructuredOutputMode(test.provider); got != test.want {
			t.Errorf("execution.DefaultStructuredOutputMode(%q) = %q, want %q", test.provider, got, test.want)
		}
	}
}
