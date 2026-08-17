package codex

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/igorrochap/rig/internal/config"
	"github.com/igorrochap/rig/internal/harness"
)

func codexEffortSetting(effort config.Effort) (string, error) {
	// Codex accepts these four normalized values directly. Keep the explicit
	// mapping here so the adapter owns the translation if either vocabulary
	// changes independently.
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
		return "", fmt.Errorf("unsupported Codex effort %q", effort)
	}
}

func composePrompt(prompt string) string {
	trimmed := strings.TrimLeft(prompt, " \t\r\n")
	lineEnd := strings.IndexByte(trimmed, '\n')
	commandEnd := lineEnd
	if commandEnd == -1 {
		commandEnd = len(trimmed)
	}
	command := strings.TrimSpace(trimmed[:commandEnd])
	if !strings.HasPrefix(command, "/") || len(command) == 1 || strings.ContainsAny(command[1:], " \t") {
		return prompt
	}
	if lineEnd == -1 {
		return "$" + command[1:]
	}
	return "$" + command[1:] + trimmed[lineEnd:]
}

type codexEvent struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Message  string `json:"message"`
	Error    struct {
		Message string `json:"message"`
	} `json:"error"`
	Item codexItem `json:"item"`
}

type codexItem struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Text    string `json:"text"`
	Command string `json:"command"`
	Message string `json:"message"`
}

type codexDecoder struct {
	startedCommands map[string]struct{}
}

func (d *codexDecoder) decodeLine(raw string) []harness.Event {
	var message codexEvent
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &message); err != nil {
		return []harness.Event{{Type: harness.EventRaw, Raw: raw}}
	}
	if d.startedCommands == nil {
		d.startedCommands = make(map[string]struct{})
	}

	var event harness.Event
	switch message.Type {
	case "thread.started":
		if message.ThreadID != "" {
			event = harness.Event{Type: harness.EventSession, SessionID: message.ThreadID}
		}
	case "item.started":
		if message.Item.Type == "command_execution" && message.Item.Command != "" {
			d.startedCommands[message.Item.ID] = struct{}{}
			event = commandEvent(message.Item.Command)
		}
	case "item.completed":
		switch message.Item.Type {
		case "agent_message":
			if message.Item.Text != "" {
				event = harness.Event{Type: harness.EventAssistantText, Text: message.Item.Text}
			}
		case "command_execution":
			if _, started := d.startedCommands[message.Item.ID]; !started && message.Item.Command != "" {
				event = commandEvent(message.Item.Command)
			}
		case "error":
			if message.Item.Message != "" {
				event = harness.Event{Type: harness.EventResult, Text: message.Item.Message, IsError: true}
			}
		}
	case "turn.completed":
		event = harness.Event{Type: harness.EventResult}
	case "turn.failed":
		event = harness.Event{Type: harness.EventResult, Text: message.Error.Message, IsError: true}
	case "error":
		event = harness.Event{Type: harness.EventResult, Text: message.Message, IsError: true}
	}
	if event.Type == "" {
		return []harness.Event{{Type: harness.EventRaw, Raw: raw}}
	}
	event.Raw = raw
	return []harness.Event{event}
}

func commandEvent(command string) harness.Event {
	return harness.Event{
		Type:         harness.EventToolUse,
		ToolName:     "Bash",
		ArgumentGist: commandGist(command),
	}
}

func commandGist(command string) string {
	command = strings.Join(strings.Fields(command), " ")
	const maxGistLength = 160
	if len(command) > maxGistLength {
		return command[:maxGistLength-1] + "…"
	}
	return command
}
