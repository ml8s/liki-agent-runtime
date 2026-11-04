package agent

import (
	"context"
	"fmt"
	"os"

	agentdeployment "github.com/ml8s/agent-runtime/internal/deployment"
	"github.com/ml8s/agent-runtime/internal/domain"

	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/skilltoolset"
	"google.golang.org/adk/v2/tool/skilltoolset/skill"
)

// skillBinding resolves the effective skills binding for an agent as a copy
// of the deployment value: the artifact owns the root, and callers can adjust
// the copy without mutating the shared definition.
func skillBinding(definition *agentdeployment.AgentDefinition) *agentdeployment.SkillsBinding {
	if definition.Skills == nil {
		return nil
	}
	binding := *definition.Skills
	return &binding
}

// newSkillToolset builds the ADK skilltoolset for a binding. The filesystem
// source lists immediate subdirectories as skills (ADK layout rule); the
// preload wrapper reads all frontmatters eagerly so invalid frontmatter
// (including name/directory mismatch) fails at build time, not per request.
func newSkillToolset(binding *agentdeployment.SkillsBinding) (tool.Toolset, error) {
	if binding == nil {
		return nil, domain.NewError(domain.CodeSkillToolsetUnavailable, "skills binding is required", nil)
	}
	ctx := context.Background()
	base := skill.NewFileSystemSource(os.DirFS(binding.Root))
	var source skill.Source
	switch binding.Preload {
	case "complete":
		preload, _, err := skill.WithCompletePreloadSource(ctx, base)
		if err != nil {
			return nil, domain.NewError(domain.CodeSkillToolsetUnavailable, fmt.Sprintf("preload skills from %q", binding.Root), err)
		}
		source = preload
	default:
		preload, _, err := skill.WithFrontmatterPreloadSource(ctx, base)
		if err != nil {
			return nil, domain.NewError(domain.CodeSkillToolsetUnavailable, fmt.Sprintf("preload skills from %q", binding.Root), err)
		}
		source = preload
	}
	ts, err := skilltoolset.New(ctx, skilltoolset.Config{Source: source})
	if err != nil {
		return nil, domain.NewError(domain.CodeSkillToolsetUnavailable, fmt.Sprintf("create skill toolset for %q", binding.Root), err)
	}
	return ts, nil
}
