package execution

import "testing"

func TestDefaultStructuredOutputMode(t *testing.T) {
	testCases := []struct {
		provider string
		want     string
	}{
		{provider: "openai", want: StructuredOutputJSONSchema},
		{provider: "zhipu", want: StructuredOutputJSONObject},
		{provider: "", want: StructuredOutputJSONObject},
		{provider: "bigmodel", want: StructuredOutputJSONObject},
		{provider: "glm", want: StructuredOutputJSONObject},
	}
	for _, testCase := range testCases {
		if got := DefaultStructuredOutputMode(testCase.provider); got != testCase.want {
			t.Fatalf("DefaultStructuredOutputMode(%q) = %q, want %q",
				testCase.provider, got, testCase.want)
		}
	}
}
