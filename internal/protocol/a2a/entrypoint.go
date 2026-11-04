package a2a

import "github.com/ml8s/agent-runtime/internal/execution"

// structuredEntrypoint is the read-only runtime boundary required to map a
// validated structured final answer onto A2A.
type structuredEntrypoint interface {
	Name() string
	ParseStructuredOutput(value any) (execution.OutputResult, error)
}
