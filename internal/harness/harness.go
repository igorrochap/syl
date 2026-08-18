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

type Request struct {
	Model  string
	Effort config.Effort
	Prompt string
	MCP    bool
}

type Stream interface {
	Events() <-chan Event
	Wait() error
}

type Adapter interface {
	Run(ctx context.Context, request Request) (Stream, error)
	Resume(ctx context.Context, sessionID string, request Request) (Stream, error)
	Attach(ctx context.Context, request Request) error
}
