package execution

// Structured output is an optional Agent capability. Plain text is the
// default so that generic chat Agents do not need an output artifact.
const (
	StructuredOutputNone       = ""
	StructuredOutputJSONSchema = "json_schema"
	StructuredOutputJSONObject = "json_object"
	// StructuredOutputNative selects ADK's native ResponseSchema path. The
	// provider model implementation translates genai.Schema to its own wire
	// format; no OpenAI-compatible shim is applied.
	StructuredOutputNative = "native"
)

// DefaultStructuredOutputMode selects the structured-output wire mode when the
// deployment declares output schemas but the operator did not choose a mode.
// The deployment decides whether structured output is needed; the provider only
// decides how it is requested. OpenAI supports the standard JSON schema
// response format; other OpenAI-compatible providers fall back to JSON object
// mode. Anthropic and Gemini handle genai.Schema natively through ADK.
func DefaultStructuredOutputMode(provider string) string {
	switch provider {
	case "openai":
		return StructuredOutputJSONSchema
	case "anthropic", "gemini":
		return StructuredOutputNative
	default:
		return StructuredOutputJSONObject
	}
}
