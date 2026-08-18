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

	"github.com/igorrochap/syl/internal/harness"
)

type Adapter struct {
	command     string
	projectRoot string
}

type mcpEnabled bool

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

func (a *Adapter) Resume(ctx context.Context, sessionID string, request harness.Request) (harness.Stream, error) {
	args, err := resumeArgs(sessionID, request)
	if err != nil {
		return nil, err
	}
	return a.start(ctx, args)
}

func baseFlags(enabled mcpEnabled) []string {
	args := []string{
		"--print",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		// syl drives Claude Code headlessly, so there is no interactive prompt
		// to approve tool use. Without this both roles are blocked: the
		// implementer cannot write files and the reviewer cannot run git or
		// spawn sub-agents. bypassPermissions matches the autonomous, sandboxed
		// approval model the Codex adapter already relies on.
		"--permission-mode", "bypassPermissions",
	}
	if !enabled {
		args = append(args, "--strict-mcp-config")
	}
	return args
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
