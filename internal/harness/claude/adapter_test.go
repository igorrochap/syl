package claude

import (
	"testing"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/harness"
)

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
