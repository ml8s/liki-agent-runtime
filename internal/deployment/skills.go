package deployment

import (
	"fmt"
	"slices"
)

// SkillToolsetServer is the virtual (non-MCP) server name used in tools.allow
// for the skilltoolset builtin tools.
const SkillToolsetServer = "skilltoolset"

var skillToolsetToolNames = []string{"list_skills", "load_skill", "load_skill_resource"}

// ValidateSkillToolsetAllow requires exactly the three skilltoolset tools:
// audit allowlisting resolves tool references per agent, so a partial list
// would surface as runtime "not allowlisted" failures instead of fail-fast
// validation.
func ValidateSkillToolsetAllow(tools []string) error {
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
