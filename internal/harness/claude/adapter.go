// Package claude adapts the Claude Code CLI to the harness interface.
package claude

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/igorrochap/syl/internal/harness"
)

func (a *PTYAdapter) attach(ctx context.Context, request harness.Request) error {
	args, err := attachArgs(request)
	if err != nil {
		return err
	}
	return a.runInteractive(ctx, args)
}

// AttachSession hands the terminal to a recorded Claude Code session.
func (a *PTYAdapter) AttachSession(
	ctx context.Context,
	sessionID string,
	request harness.Request,
) error {
	args, err := attachSessionArgs(sessionID, request)
	if err != nil {
		return err
	}
	return a.runInteractive(ctx, args)
}

func (a *PTYAdapter) runInteractive(ctx context.Context, args []string) error {
	command := a.command
	if command == "" {
		command = "claude"
	}
	process := exec.CommandContext(ctx, command, args...)
	if a.projectRoot != "" {
		process.Dir = a.projectRoot
	}
	process.Stdin = os.Stdin
	process.Stdout = os.Stdout
	process.Stderr = os.Stderr
	if err := process.Run(); err != nil {
		return fmt.Errorf("Claude Code exited unsuccessfully: %w", err)
	}
	return nil
}

type processStream struct {
	events <-chan harness.Event
	done   <-chan error
}

func (s processStream) Events() <-chan harness.Event { return s.events }

func (s processStream) Wait() error { return <-s.done }
