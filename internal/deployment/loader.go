package deployment

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type definitionLoader struct {
	root string
}

// LoadAgentDeployment validates an AgentDeployment artifact, resolves its
// external artifacts, and computes its content digest.
func LoadAgentDeployment(path string) (*AgentDeployment, error) {
	if strings.TrimSpace(path) == "" {
		return nil, artifactError(CodeDeploymentPathRequired, "agent deployment file is required")
	}
	root, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return nil, artifactErrorWrap(CodeDeploymentUnreadable, "resolve agent deployment directory", err)
	}
	loader := &definitionLoader{root: root}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, artifactErrorWrap(CodeDeploymentUnreadable, "read agent deployment", err)
	}
	if len(raw) > maxManifestBytes {
		return nil, artifactError(CodeDeploymentManifestTooLarge, "agent deployment manifest exceeds %d bytes", maxManifestBytes)
	}
	deployment, err := decodeAgentDeployment(raw)
	if err != nil {
		return nil, err
	}
	if err := deployment.validate(); err != nil {
		return nil, err
	}
	if err := loader.loadAgents(deployment); err != nil {
		return nil, err
	}
	deployment.manifest = append([]byte(nil), raw...)
	deployment.ManifestDigest = sha256Hex(raw)
	if deployment.Digest, err = deploymentDigest(deployment); err != nil {
		return nil, artifactErrorWrap(CodeDeploymentIdentityInvalid, "deployment digest", err)
	}
	snapshot, err := deployment.Clone()
	if err != nil {
		return nil, artifactErrorWrap(CodeDeploymentIdentityInvalid, "snapshot agent deployment", err)
	}
	if err := snapshot.ValidateIntegrity(); err != nil {
		return nil, artifactErrorWrap(CodeDeploymentIntegrityInvalid, "validate deployment integrity", err)
	}
	return snapshot, nil
}

func (l *definitionLoader) readFile(path string) ([]byte, error) {
	root, err := os.OpenRoot(l.root)
	if err != nil {
		return nil, artifactErrorWrap(CodeDeploymentArtifactReadFailed, "open agent deployment directory", err)
	}
	data, err := root.ReadFile(path)
	if err != nil {
		closeErr := root.Close()
		if closeErr != nil {
			return nil, artifactErrorWrap(CodeDeploymentArtifactReadFailed, fmt.Sprintf("read %q (close failed: %v)", path, closeErr), err)
		}
		return nil, err
	}
	if err := root.Close(); err != nil {
		return nil, artifactErrorWrap(CodeDeploymentArtifactReadFailed, "close agent deployment directory", err)
	}
	return data, nil
}
