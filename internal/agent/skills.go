package agent

import (
	"context"
	"fmt"
	"os"
	"slices"

	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/skilltoolset"
	"google.golang.org/adk/v2/tool/skilltoolset/skill"
)

// skillToolsetServer is the virtual (non-MCP) server name used in
// tools.allow for the skilltoolset builtin tools.
const skillToolsetServer = "skilltoolset"

var skillToolsetToolNames = []string{"list_skills", "load_skill", "load_skill_resource"}

// validateSkillToolsetAllow requires exactly the three skilltoolset tools:
// audit allowlisting resolves tool references per agent, so a partial list
// would surface as runtime "not allowlisted" failures instead of fail-fast
// validation.
func validateSkillToolsetAllow(tools []string) error {
	for _, name := range skillToolsetToolNames {
		if !slices.Contains(tools, name) {
			return fmt.Errorf("missing builtin tool %q (required set: %v)", name, skillToolsetToolNames)
		}
	}
	for _, name := range tools {
		if !slices.Contains(skillToolsetToolNames, name) {
			return fmt.Errorf("unknown builtin tool %q (allowed: %v)", name, skillToolsetToolNames)
		}
	}
	return nil
}

// skillBinding resolves the effective skills binding for an agent as a copy
// of the deployment value: the artifact owns the root, and callers can adjust
// the copy without mutating the shared definition.
func skillBinding(definition *AgentDefinition) *SkillsBinding {
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
func newSkillToolset(binding *SkillsBinding) (tool.Toolset, error) {
	if binding == nil {
		return nil, fmt.Errorf("skills binding is required")
	}
	ctx := context.Background()
	base := skill.NewFileSystemSource(os.DirFS(binding.Root))
	var source skill.Source
	switch binding.Preload {
	case "complete":
		preload, _, err := skill.WithCompletePreloadSource(ctx, base)
		if err != nil {
			return nil, fmt.Errorf("preload skills from %q: %w", binding.Root, err)
		}
		source = preload
	default:
		preload, _, err := skill.WithFrontmatterPreloadSource(ctx, base)
		if err != nil {
			return nil, fmt.Errorf("preload skills from %q: %w", binding.Root, err)
		}
		source = preload
	}
	ts, err := skilltoolset.New(ctx, skilltoolset.Config{Source: source})
	if err != nil {
		return nil, fmt.Errorf("create skill toolset for %q: %w", binding.Root, err)
	}
	return ts, nil
}
