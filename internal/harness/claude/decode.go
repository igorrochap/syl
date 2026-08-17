package claude

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/igorrochap/rig/internal/config"
	"github.com/igorrochap/rig/internal/harness"
)

func claudeEffortFlag(effort config.Effort) (string, error) {
	switch effort {
	case config.EffortLow:
		return "low", nil
	case config.EffortMedium:
		return "medium", nil
	case config.EffortHigh:
		return "high", nil
	case config.EffortXHigh:
		return "xhigh", nil
	default:
		return "", fmt.Errorf("unsupported Claude Code effort %q", effort)
	}
}

func runArgs(request harness.Request) ([]string, error) {
	effort, err := claudeEffortFlag(request.Effort)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.Model) == "" {
		return nil, fmt.Errorf("Claude Code model is required")
	}
	if strings.TrimSpace(request.Prompt) == "" {
		return nil, fmt.Errorf("Claude Code prompt is required")
	}
	return []string{
		"--print",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--model", request.Model,
		"--effort", effort,
		request.Prompt,
	}, nil
}

type claudeStreamMessage struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Message   struct {
		Content []claudeContentBlock `json:"content"`
	} `json:"message"`
	Result  string `json:"result"`
	IsError bool   `json:"is_error"`
	Event   struct {
		Type  string `json:"type"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
		ContentBlock claudeContentBlock `json:"content_block"`
	} `json:"event"`
}

type claudeContentBlock struct {
	Type  string          `json:"type"`
	Name  string          `json:"name"`
	Text  string          `json:"text"`
	Input json.RawMessage `json:"input"`
}

func decodeLine(raw string) []harness.Event {
	var message claudeStreamMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &message); err != nil {
		return []harness.Event{{Type: harness.EventRaw, Raw: raw}}
	}

	events := make([]harness.Event, 0, 2)
	if message.SessionID != "" {
		events = append(events, harness.Event{Type: harness.EventSession, SessionID: message.SessionID})
	}
	switch message.Type {
	case "assistant":
		for _, block := range message.Message.Content {
			if block.Type != "tool_use" {
				continue
			}
			events = append(events, harness.Event{
				Type:         harness.EventToolUse,
				ToolName:     block.Name,
				ArgumentGist: argumentGist(block.Input),
			})
		}
	case "stream_event":
		if message.Event.Type == "content_block_delta" && message.Event.Delta.Type == "text_delta" && message.Event.Delta.Text != "" {
			events = append(events, harness.Event{Type: harness.EventAssistantText, Text: message.Event.Delta.Text})
		}
	case "result":
		events = append(events, harness.Event{
			Type:    harness.EventResult,
			Text:    message.Result,
			IsError: message.IsError,
		})
	}
	if len(events) == 0 {
		return []harness.Event{{Type: harness.EventRaw, Raw: raw}}
	}
	events[0].Raw = raw
	return events
}

func argumentGist(input json.RawMessage) string {
	if len(input) == 0 || string(input) == "null" {
		return ""
	}
	var value any
	if err := json.Unmarshal(input, &value); err != nil {
		return strings.Join(strings.Fields(string(input)), " ")
	}
	compact, err := json.Marshal(value)
	if err != nil {
		return strings.Join(strings.Fields(string(input)), " ")
	}
	const maxGistLength = 160
	if len(compact) > maxGistLength {
		return string(compact[:maxGistLength-1]) + "…"
	}
	return string(compact)
}
