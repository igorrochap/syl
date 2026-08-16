package claude

import (
	"testing"

	"github.com/igorrochap/rig/internal/config"
	"github.com/igorrochap/rig/internal/harness"
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
