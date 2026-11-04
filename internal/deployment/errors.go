package deployment

import (
	"fmt"

	"github.com/ml8s/agent-runtime/internal/domain"
)

// Stable deployment contract and artifact error codes. Protocol surfaces,
// startup diagnostics, and deployment tooling may match these values without
// matching human-readable messages.
const (
	CodeDeploymentPathRequired         = "deployment_path_required"
	CodeDeploymentUnreadable           = "deployment_unreadable"
	CodeDeploymentManifestTooLarge     = "deployment_manifest_too_large"
	CodeDeploymentSchemaVersionInvalid = "deployment_schema_version_invalid"
	CodeDeploymentMetadataInvalid      = "deployment_metadata_invalid"
	CodeDeploymentArtifactInvalid      = "deployment_artifact_invalid"
	CodeDeploymentArtifactTooLarge     = "deployment_artifact_too_large"
	CodeDeploymentArtifactReadFailed   = "deployment_artifact_read_failed"
	CodeDeploymentMCPServerInvalid     = "deployment_mcp_server_invalid"
	CodeDeploymentMCPServerDuplicate   = "deployment_mcp_server_duplicate"
	CodeDeploymentAgentMissing         = "deployment_agent_missing"
	CodeDeploymentAgentInvalid         = "deployment_agent_invalid"
	CodeDeploymentAgentDuplicate       = "deployment_agent_duplicate"
	CodeDeploymentGraphInvalid         = "deployment_graph_invalid"
	CodeDeploymentOutputSchemaInvalid  = "deployment_output_schema_invalid"
	CodeDeploymentOutputPointerInvalid = "deployment_output_pointer_invalid"
	CodeDeploymentToolAllowlistInvalid = "deployment_tool_allowlist_invalid"
	CodeDeploymentSkillsBindingInvalid = "deployment_skills_binding_invalid"
	CodeDeploymentIdentityInvalid      = "deployment_identity_invalid"
	CodeDeploymentIntegrityInvalid     = "deployment_integrity_invalid"
)

func validationError(code, format string, args ...any) error {
	return domain.NewError(code, fmt.Sprintf(format, args...), domain.ErrInvalidInput)
}

func validationErrorWrap(code, message string, cause error) error {
	if cause != nil {
		message = message + ": " + cause.Error()
	}
	return domain.NewError(code, message, cause)
}

func artifactError(code, format string, args ...any) error {
	return domain.NewError(code, fmt.Sprintf(format, args...), nil)
}

func artifactErrorWrap(code, message string, cause error) error {
	if cause != nil {
		message = message + ": " + cause.Error()
	}
	return domain.NewError(code, message, cause)
}
