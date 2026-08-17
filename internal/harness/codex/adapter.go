// Package codex adapts the Codex CLI to the harness interface.
//
// Codex discovers project-local skills from .agents/skills when it is started
// in the project root. The adapter rewrites the existing slash-command prompt
// prefix to Codex's native $skill-name form so headless runs trigger the
// discovered skill. It deliberately does not inline SKILL.md: native discovery
// is the same skill-loading path used by interactive Codex and keeps the prompt
// and skill source canonical.
package codex

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/igorrochap/rig/internal/harness"
)

type Adapter struct {
	command     string
	projectRoot string
}

func New(projectRoot string) *Adapter {
	return &Adapter{command: "codex", projectRoot: projectRoot}
}

var _ harness.Adapter = (*Adapter)(nil)

func (a *Adapter) Run(ctx context.Context, request harness.Request) (harness.Stream, error) {
	args, err := a.runArgs(request)
	if err != nil {
		return nil, err
	}
	return a.start(ctx, args)
}

func (a *Adapter) Resume(ctx context.Context, sessionID, prompt string) (harness.Stream, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, errors.New("cannot resume Codex session without a session id")
	}
	if strings.TrimSpace(prompt) == "" {
		return nil, errors.New("cannot resume Codex session without a prompt")
	}
	args := []string{"exec", "resume", "--json"}
	args = a.withProjectRoot(args)
	args = append(args, sessionID, prompt)
	return a.start(ctx, args)
}

func (a *Adapter) Attach(ctx context.Context, request harness.Request) error {
	args, err := a.attachArgs(request)
	if err != nil {
		return err
	}
	process := exec.CommandContext(ctx, a.executable(), args...)
	if a.projectRoot != "" {
		process.Dir = a.projectRoot
	}
	process.Stdin = os.Stdin
	process.Stdout = os.Stdout
	process.Stderr = os.Stderr
	if err := process.Run(); err != nil {
		return fmt.Errorf("Codex exited unsuccessfully: %w", err)
	}
	return nil
}

func (a *Adapter) runArgs(request harness.Request) ([]string, error) {
	args, err := a.baseArgs(request)
	if err != nil {
		return nil, err
	}
	args = append([]string{"exec", "--json"}, args...)
	args = a.withProjectRoot(args)
	return append(args, composePrompt(request.Prompt)), nil
}

func (a *Adapter) attachArgs(request harness.Request) ([]string, error) {
	args, err := a.baseArgs(request)
	if err != nil {
		return nil, err
	}
	args = a.withProjectRoot(args)
	return append(args, composePrompt(request.Prompt)), nil
}

func (a *Adapter) baseArgs(request harness.Request) ([]string, error) {
	effort, err := codexEffortSetting(request.Effort)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.Model) == "" {
		return nil, errors.New("Codex model is required")
	}
	if strings.TrimSpace(request.Prompt) == "" {
		return nil, errors.New("Codex prompt is required")
	}
	return []string{
		"--model", request.Model,
		"--config", `model_reasoning_effort="` + effort + `"`,
	}, nil
}

func (a *Adapter) withProjectRoot(args []string) []string {
	if a.projectRoot == "" {
		return args
	}
	return append(args, "--cd", a.projectRoot)
}

func (a *Adapter) executable() string {
	if a.command == "" {
		return "codex"
	}
	return a.command
}

func (a *Adapter) start(ctx context.Context, args []string) (harness.Stream, error) {
	process := exec.CommandContext(ctx, a.executable(), args...)
	if a.projectRoot != "" {
		process.Dir = a.projectRoot
	}
	stdout, err := process.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("prepare Codex output: %w", err)
	}
	var stderr bytes.Buffer
	process.Stderr = &stderr
	if err := process.Start(); err != nil {
		return nil, fmt.Errorf("start Codex: %w", err)
	}

	events := make(chan harness.Event, 16)
	done := make(chan error, 1)
	go func() {
		defer close(events)
		decoder := &codexDecoder{}
		reader := bufio.NewReader(stdout)
		var streamErr error
		for {
			raw, readErr := reader.ReadString('\n')
			if raw != "" {
				for _, event := range decoder.decodeLine(raw) {
					events <- event
				}
			}
			if readErr != nil {
				if !errors.Is(readErr, io.EOF) {
					streamErr = fmt.Errorf("read Codex output: %w", readErr)
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
			done <- fmt.Errorf("Codex exited unsuccessfully: %w", waitErr)
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
