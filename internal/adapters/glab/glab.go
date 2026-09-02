// Package glab contains the external GitLab CLI adapter.
package glab

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/igorrochap/syl/internal/tracker"
)

// Runner runs glab commands in one project directory and satisfies tracker.GLabRunner.
type Runner struct {
	Dir string
}

var _ tracker.GLabRunner = Runner{}

// Run executes glab with the configured project directory as its working directory.
func (r Runner) Run(ctx context.Context, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "glab", args...)
	if r.Dir != "" {
		command.Dir = r.Dir
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		errOutput := strings.TrimSpace(stderr.String())
		if errOutput == "" {
			return string(output), fmt.Errorf("run glab: %w", err)
		}
		return string(output), fmt.Errorf("run glab: %w: %s", err, errOutput)
	}
	return string(output), nil
}
