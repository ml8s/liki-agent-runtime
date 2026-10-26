package agent

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
		return nil, fmt.Errorf("agent deployment file is required")
	}
	root, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("resolve agent deployment directory: %w", err)
	}
	loader := &definitionLoader{root: root}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read agent deployment: %w", err)
	}
	if len(raw) > maxManifestBytes {
		return nil, fmt.Errorf("agent deployment manifest exceeds %d bytes", maxManifestBytes)
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
	if deployment.Digest, err = deploymentDigest(raw, deployment); err != nil {
		return nil, fmt.Errorf("deployment digest: %w", err)
	}
	return deployment, nil
}

func (l *definitionLoader) readFile(path string) ([]byte, error) {
	root, err := os.OpenRoot(l.root)
	if err != nil {
		return nil, fmt.Errorf("open agent deployment directory: %w", err)
	}
	data, err := root.ReadFile(path)
	if err != nil {
		closeErr := root.Close()
		if closeErr != nil {
			return nil, fmt.Errorf("read %q (close failed: %w): %w", path, closeErr, err)
		}
		return nil, err
	}
	if err := root.Close(); err != nil {
		return nil, fmt.Errorf("close agent deployment directory: %w", err)
	}
	return data, nil
}
