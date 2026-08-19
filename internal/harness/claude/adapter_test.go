package claude

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/harness"
)

func TestClaudeAttachInvokesInteractivePrompt(t *testing.T) {
	root := t.TempDir()
	argsPath := filepath.Join(root, "args")
	command := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + argsPath + "\"\n"
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	adapter := &Adapter{command: command, projectRoot: root}

	err := adapter.Attach(context.Background(), harness.Request{
		Model:  "claude-opus-5",
		Effort: config.EffortHigh,
		Prompt: "/to-tickets\n\nTopic: offline mode",
		MCP:    false,
	})
	if err != nil {
		t.Fatalf("Attach() error = %v", err)
	}

	contents, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	gotArgs := strings.Split(strings.TrimSpace(string(contents)), "\n")
	wantArgs := []string{
		"--strict-mcp-config",
		"--model", "claude-opus-5",
		"--effort", "high",
		"/to-tickets",
		"",
		"Topic: offline mode",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("Claude attach args = %#v, want %#v", gotArgs, wantArgs)
	}
}

func TestClaudeEffortMapping(t *testing.T) {
	tests := []struct {
		effort config.Effort
		want   string
	}{
		{effort: config.EffortLow, want: "low"},
		{effort: config.EffortMedium, want: "medium"},
		{effort: config.EffortHigh, want: "high"},
		{effort: config.EffortXHigh, want: "xhigh"},
	}
	for _, tt := range tests {
		t.Run(string(tt.effort), func(t *testing.T) {
			got, err := claudeEffortFlag(tt.effort)
			if err != nil {
				t.Fatalf("claudeEffortFlag() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("claudeEffortFlag() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRunArgsGrantAutonomousPermissions guards against the headless-permission
// regression: without bypassPermissions Claude Code denies every tool call, so
// the implementer cannot write files and the reviewer cannot run git.
func TestRunArgsGrantAutonomousPermissions(t *testing.T) {
	args, err := runArgs(harness.Request{Model: "claude-sonnet-5", Effort: config.EffortMedium, Prompt: "do it"})
	if err != nil {
		t.Fatalf("runArgs() error = %v", err)
	}
	if !containsFlagValue(args, "--permission-mode", "bypassPermissions") {
		t.Fatalf("runArgs() = %v, want --permission-mode bypassPermissions", args)
	}
}

func TestClaudeMCPConfigurationIsAppliedToRunAndResume(t *testing.T) {
	for _, tt := range []struct {
		name string
		mcp  bool
		want bool
	}{
		{name: "inherit MCP", mcp: true, want: false},
		{name: "strip MCP", mcp: false, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			request := harness.Request{
				Model:  "claude-sonnet-5",
				Effort: config.EffortMedium,
				Prompt: "do it",
				MCP:    tt.mcp,
			}
			run, err := runArgs(request)
			if err != nil {
				t.Fatalf("runArgs() error = %v", err)
			}
			resume, err := resumeArgs("session-1", request)
			if err != nil {
				t.Fatalf("resumeArgs() error = %v", err)
			}

			if got := containsArg(run, "--strict-mcp-config"); got != tt.want {
				t.Fatalf("runArgs() strict MCP flag = %t, want %t: %v", got, tt.want, run)
			}
			if got := containsArg(resume, "--strict-mcp-config"); got != tt.want {
				t.Fatalf("resumeArgs() strict MCP flag = %t, want %t: %v", got, tt.want, resume)
			}
		})
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func containsFlagValue(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestDecodeClaudeStreamMapsStructuredEvents(t *testing.T) {
	line := `{"type":"assistant","session_id":"session-1","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git diff --stat"}}]}}`
	events := decodeLine(line + "\n")
	if len(events) != 2 {
		t.Fatalf("decodeLine() returned %d events, want session and tool event: %#v", len(events), events)
	}
	if events[0].Type != harness.EventSession || events[0].SessionID != "session-1" {
		t.Fatalf("session event = %#v", events[0])
	}
	if events[1].Type != harness.EventToolUse || events[1].ToolName != "Bash" || events[1].ArgumentGist != `{"command":"git diff --stat"}` {
		t.Fatalf("tool event = %#v", events[1])
	}
	if events[0].Raw == "" || events[1].Raw != "" {
		t.Fatalf("raw output should be attached once, got first=%q second=%q", events[0].Raw, events[1].Raw)
	}
}
