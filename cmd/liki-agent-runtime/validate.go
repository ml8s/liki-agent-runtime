package main

import (
	"flag"
	"fmt"
	"os"

	agentruntime "github.com/ml8s/liki-agent-runtime/internal/agent"
)

func validate(args []string) error {
	flags := flag.NewFlagSet("liki-agent-runtime validate", flag.ContinueOnError)
	path := flags.String(
		"deployment",
		os.Getenv("LIKI_AGENT_RUNTIME_DEPLOYMENT_FILE"),
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

	deployment, err := agentruntime.LoadAgentDeployment(*path)
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
