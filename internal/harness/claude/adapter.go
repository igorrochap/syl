// Package claude adapts the Claude Code CLI to the harness interface.
package claude

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/igorrochap/rig/internal/harness"
)

type Adapter struct {
	command     string
	projectRoot string
}

func New(projectRoot string) *Adapter {
	return &Adapter{command: "claude", projectRoot: projectRoot}
}

var _ harness.Adapter = (*Adapter)(nil)

func (a *Adapter) Run(ctx context.Context, request harness.Request) (harness.Stream, error) {
	args, err := runArgs(request)
	if err != nil {
		return nil, err
	}
	return a.start(ctx, args)
}

func (a *Adapter) Resume(ctx context.Context, sessionID, prompt string) (harness.Stream, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, errors.New("cannot resume Claude Code session without a session id")
	}
	if strings.TrimSpace(prompt) == "" {
		return nil, errors.New("cannot resume Claude Code session without a prompt")
	}
	return a.start(ctx, []string{
		"--resume", sessionID,
		"--print",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		prompt,
	})
}

func (*Adapter) Attach(context.Context, harness.Request) error {
	return errors.New("interactive harness attach is not supported yet")
}

func (a *Adapter) start(ctx context.Context, args []string) (harness.Stream, error) {
	command := a.command
	if command == "" {
		command = "claude"
	}
	process := exec.CommandContext(ctx, command, args...)
	if a.projectRoot != "" {
		process.Dir = a.projectRoot
	}
	stdout, err := process.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("prepare Claude Code output: %w", err)
	}
	var stderr bytes.Buffer
	process.Stderr = &stderr
	if err := process.Start(); err != nil {
		return nil, fmt.Errorf("start Claude Code: %w", err)
	}

	events := make(chan harness.Event, 16)
	done := make(chan error, 1)
	go func() {
		defer close(events)
		reader := bufio.NewReader(stdout)
		var streamErr error
		for {
			raw, readErr := reader.ReadString('\n')
			if raw != "" {
				for _, event := range decodeLine(raw) {
					events <- event
				}
			}
			if readErr != nil {
				if !errors.Is(readErr, io.EOF) {
					streamErr = fmt.Errorf("read Claude Code output: %w", readErr)
				}
				break
			}
		}
		waitErr := process.Wait()
		if waitErr != nil {
			if message := strings.TrimSpace(stderr.String()); message != "" {
				waitErr = fmt.Errorf("%w: %s", waitErr, message)
			}
		}
		if streamErr != nil {
			done <- streamErr
			return
		}
		if waitErr != nil {
			done <- fmt.Errorf("Claude Code exited unsuccessfully: %w", waitErr)
			return
		}
		done <- nil
	}()

	return processStream{events: events, done: done}, nil
}

type processStream struct {
	events <-chan harness.Event
	done   <-chan error
}

func (s processStream) Events() <-chan harness.Event { return s.events }

func (s processStream) Wait() error { return <-s.done }
