package main

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func canonicalDeploymentPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(file), "..", "..", "dev", "agent-deployment", "deployment.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("canonical deployment fixture: %v", err)
	}
	return path
}

func captureStdout(t *testing.T, run func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = original }()

	done := make(chan string)
	go func() {
		var buf strings.Builder
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	runErr := run()
	_ = w.Close()
	output := <-done
	_ = r.Close()
	return output, runErr
}

func TestValidateRequiresDeploymentPath(t *testing.T) {
	t.Setenv("LIKI_AGENT_RUNTIME_DEPLOYMENT_FILE", "")
	err := validate(nil)
	if err == nil || !strings.Contains(err.Error(), "deployment path is required") {
		t.Fatalf("error = %v, want deployment path is required", err)
	}
}

func TestValidateRejectsPositionalArguments(t *testing.T) {
	t.Setenv("LIKI_AGENT_RUNTIME_DEPLOYMENT_FILE", canonicalDeploymentPath(t))
	err := validate([]string{"unexpected"})
	if err == nil || !strings.Contains(err.Error(), "positional arguments") {
		t.Fatalf("error = %v, want positional arguments rejected", err)
	}
}

func TestValidateRejectsUnknownFlag(t *testing.T) {
	t.Setenv("LIKI_AGENT_RUNTIME_DEPLOYMENT_FILE", canonicalDeploymentPath(t))
	if err := validate([]string{"-no-such-flag"}); err == nil {
		t.Fatal("expected flag parse error")
	}
}

func TestValidateRejectsMissingDeploymentFile(t *testing.T) {
	t.Setenv("LIKI_AGENT_RUNTIME_DEPLOYMENT_FILE", "")
	err := validate([]string{"-deployment", filepath.Join(t.TempDir(), "absent.json")})
	if err == nil {
		t.Fatal("expected error for missing deployment file")
	}
}

func TestValidateAcceptsFlagPath(t *testing.T) {
	t.Setenv("LIKI_AGENT_RUNTIME_DEPLOYMENT_FILE", "")
	output, err := captureStdout(t, func() error {
		return validate([]string{"-deployment", canonicalDeploymentPath(t)})
	})
	if err != nil {
		t.Fatalf("validate() error = %v", err)
	}
	if !strings.Contains(output, "name=liki-agent-runtime-deployment") {
		t.Fatalf("output missing deployment name: %q", output)
	}
	if !strings.Contains(output, "entrypoint=main") {
		t.Fatalf("output missing entrypoint: %q", output)
	}
	if !strings.Contains(output, "digest=sha256:") {
		t.Fatalf("output missing digest: %q", output)
	}
}

func TestValidateUsesEnvironmentDefault(t *testing.T) {
	t.Setenv("LIKI_AGENT_RUNTIME_DEPLOYMENT_FILE", canonicalDeploymentPath(t))
	output, err := captureStdout(t, func() error { return validate(nil) })
	if err != nil {
		t.Fatalf("validate() error = %v", err)
	}
	if !strings.Contains(output, "name=liki-agent-runtime-deployment") {
		t.Fatalf("output missing deployment name: %q", output)
	}
}
