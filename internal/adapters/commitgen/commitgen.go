// Package commitgen provides the process adapter for the optional commitgen tool.
package commitgen

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type commandExecutor interface {
	LookPath(name string) (string, error)
	Run(ctx context.Context, executable, workRoot string, args ...string) (string, error)
}

type processExecutor struct{}

func (processExecutor) LookPath(name string) (string, error) {
	return exec.LookPath(name)
}

func (processExecutor) Run(ctx context.Context, executable, workRoot string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, executable, args...)
	command.Dir = workRoot
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		errOutput := strings.TrimSpace(stderr.String())
		if errOutput == "" {
			return string(output), fmt.Errorf("run commitgen: %w", err)
		}
		return string(output), fmt.Errorf("run commitgen: %w: %s", err, errOutput)
	}
	return string(output), nil
}

// Runner invokes commitgen commands in the supplied Work root.
type Runner struct {
	executor commandExecutor
}

func (r Runner) executorOrDefault() commandExecutor {
	if r.executor != nil {
		return r.executor
	}
	return processExecutor{}
}

// Available reports whether commitgen can be resolved from PATH.
func (r Runner) Available() bool {
	_, err := r.executorOrDefault().LookPath("commitgen")
	return err == nil
}

// Run executes commitgen with the supplied arguments in workRoot.
func (r Runner) Run(ctx context.Context, workRoot string, args ...string) (string, error) {
	executor := r.executorOrDefault()
	executable, err := executor.LookPath("commitgen")
	if err != nil {
		return "", fmt.Errorf("find commitgen: %w", err)
	}
	return executor.Run(ctx, executable, workRoot, args...)
}
