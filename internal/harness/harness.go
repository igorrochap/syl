// Package harness defines the seam between syl roles and agent CLIs.
package harness

import (
	"context"

	"github.com/igorrochap/syl/internal/config"
)

type EventType string

const (
	EventAssistantText EventType = "assistant_text"
	EventToolUse       EventType = "tool_use"
	EventSession       EventType = "session"
	EventResult        EventType = "result"
	EventRaw           EventType = "raw"
)

type Event struct {
	Type         EventType
	Text         string
	ToolName     string
	ArgumentGist string
	SessionID    string
	IsError      bool
	Raw          string
}

// CompletionSignal names the transcript signal a role expects.
type CompletionSignal int

const (
	// CompletionReviewVerdict is the default for existing reviewer callers.
	CompletionReviewVerdict CompletionSignal = iota
	// CompletionTurnEnd accepts an assistant turn ending without a sentinel.
	CompletionTurnEnd
)

func (s CompletionSignal) String() string {
	if s == CompletionTurnEnd {
		return "turn end"
	}
	return "review verdict"
}

type Request struct {
	Completion  CompletionSignal
	Model       string
	Effort      config.Effort
	Prompt      string
	MCP         bool
	SandboxMode config.SandboxMode
}

type Stream interface {
	Events() <-chan Event
	Wait() error
}

// SessionStream exposes a session ID chosen before the harness starts. A
// caller can record the session without recovering its ID from output events.
type SessionStream interface {
	Stream
	SessionID() string
}

type Adapter interface {
	Run(ctx context.Context, request Request) (Stream, error)
	Resume(ctx context.Context, sessionID string, request Request) (Stream, error)
	Attach(ctx context.Context, request Request) error
	AttachSession(ctx context.Context, sessionID string, request Request) error
}
