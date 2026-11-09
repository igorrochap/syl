// Package harness defines the seam between syl roles and agent CLIs.
package harness

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/igorrochap/syl/internal/config"
)

// StalledSessionReportTextLimit bounds the report text included in recovery prompts.
const StalledSessionReportTextLimit = 64 * 1024

// ErrSessionStalled matches a session that reached a Claude session limit.
var ErrSessionStalled = errors.New("session stalled")

// SessionStallLimit names the timer that ended a silent Claude session.
type SessionStallLimit string

const (
	// StallWindowLimit identifies the short window for the known stalled-session pattern.
	StallWindowLimit SessionStallLimit = "stall window"
	// NoProgressLimit identifies the longer cap for all other silent sessions.
	NoProgressLimit SessionStallLimit = "no-progress limit"
)

// SubagentReport contains a finished subagent's final assistant response.
// TooLarge means the report should be read from Path instead of embedded.
type SubagentReport struct {
	Path     string
	Text     string
	TooLarge bool
}

// SessionStalledError reports a Claude session that reached a stall limit.
type SessionStalledError struct {
	SessionID    string
	Limit        SessionStallLimit
	Completion   string
	StateSummary string
	Reports      []SubagentReport
}

func (e *SessionStalledError) Error() string {
	summary := strings.TrimSpace(e.StateSummary)
	if summary == "" {
		summary = "unavailable"
	}
	message := fmt.Sprintf("%s: %s", ErrSessionStalled, e.SessionID)
	if e.Limit != "" {
		message += " after " + string(e.Limit)
	}
	if e.Completion != "" {
		message += " without a " + e.Completion
	}
	return fmt.Sprintf("%s\nSession state:\n%s", message, summary)
}

// Is reports whether the error matches ErrSessionStalled.
func (e *SessionStalledError) Is(target error) bool {
	return target == ErrSessionStalled
}

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
