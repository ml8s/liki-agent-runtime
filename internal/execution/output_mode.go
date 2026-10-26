package execution

// Structured output is an optional Agent capability. Plain text is the
// default so that generic chat Agents do not need an output artifact.
const (
	StructuredOutputNone       = ""
	StructuredOutputJSONSchema = "json_schema"
	StructuredOutputJSONObject = "json_object"
)

// DefaultStructuredOutputMode selects the structured-output wire mode when the
// deployment declares output schemas but the operator did not choose a mode.
// The deployment decides whether structured output is needed; the provider only
// decides how it is requested: OpenAI exposes the standard JSON schema
// response format, while the bigmodel/zhipu family is addressed through JSON
// object mode.
func DefaultStructuredOutputMode(provider string) string {
	if provider == "openai" {
		return StructuredOutputJSONSchema
	}
	return StructuredOutputJSONObject
}
