package main

import (
	"flag"
	"fmt"
	"os"

	deployment "github.com/ml8s/agent-runtime/internal/deployment"
)

func validate(args []string) error {
	flags := flag.NewFlagSet("agent-runtime validate", flag.ContinueOnError)
	path := flags.String(
		"deployment",
		os.Getenv("AGENT_RUNTIME_DEPLOYMENT_FILE"),
		"AgentDeployment artifact path",
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("validate accepts no positional arguments")
	}
	if *path == "" {
		return fmt.Errorf("deployment path is required")
	}

	deployment, err := deployment.LoadAgentDeployment(*path)
	if err != nil {
		return err
	}
	entrypoint, err := deployment.EntrypointDefinition()
	if err != nil {
		return err
	}
	fmt.Printf(
		"valid agent deployment name=%s version=%s digest=%s entrypoint=%s agents=%d\n",
		deployment.Metadata.Name,
		deployment.Metadata.Version,
		deployment.Digest,
		entrypoint.Name,
		len(deployment.Spec.Agents),
	)
	return nil
}
