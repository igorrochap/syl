package glab

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunnerRunsGlabInConfiguredDirectoryAndIncludesStderr(t *testing.T) {
	binDir := t.TempDir()
	projectDir := t.TempDir()
	commandPath := filepath.Join(binDir, "glab")
	command := "#!/bin/sh\npwd\nprintf 'simulated glab stderr' >&2\nexit 1\n"
	if err := os.WriteFile(commandPath, []byte(command), 0o755); err != nil {
		t.Fatalf("write fake glab: %v", err)
	}
	t.Setenv("PATH", binDir)

	output, err := (Runner{Dir: projectDir}).Run(context.Background(), "issue", "list")
	if err == nil || !strings.Contains(err.Error(), "simulated glab stderr") {
		t.Fatalf("Runner.Run() error = %v, want captured stderr", err)
	}
	if strings.TrimSpace(output) != projectDir {
		t.Fatalf("Runner.Run() output = %q, want command directory %q", output, projectDir)
	}
}
