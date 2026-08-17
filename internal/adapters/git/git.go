package git

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// ExecGitRunner runs git in one project directory.
type ExecGitRunner struct {
	Dir string
}

var _ interface {
	Run(context.Context, ...string) (string, error)
} = ExecGitRunner{}

func (r ExecGitRunner) Run(ctx context.Context, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	if r.Dir != "" {
		command.Dir = r.Dir
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		errOutput := strings.TrimSpace(stderr.String())
		if errOutput == "" {
			return string(output), fmt.Errorf("run git: %w", err)
		}
		return string(output), fmt.Errorf("run git: %w: %s", err, errOutput)
	}
	return string(output), nil
}
