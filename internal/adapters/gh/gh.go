// Package gh contains the external GitHub CLI adapter.
package gh

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/igorrochap/syl/internal/tracker"
)

// Runner runs gh commands in one project directory and satisfies tracker.GHRunner.
type Runner struct {
	Dir string
}

var _ tracker.GHRunner = Runner{}

func (r Runner) Run(ctx context.Context, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "gh", args...)
	if r.Dir != "" {
		command.Dir = r.Dir
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		errOutput := strings.TrimSpace(stderr.String())
		if errOutput == "" {
			return string(output), fmt.Errorf("run gh: %w", err)
		}
		return string(output), fmt.Errorf("run gh: %w: %s", err, errOutput)
	}
	return string(output), nil
}
