package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/harness"
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

func attachArgs(request harness.Request) ([]string, error) {
	effort, err := claudeEffortFlag(request.Effort)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.Model) == "" {
		return nil, errors.New("Claude Code model is required")
	}
	if strings.TrimSpace(request.Prompt) == "" {
		return nil, errors.New("Claude Code prompt is required")
	}
	var args []string
	if !request.MCP {
		args = append(args, "--strict-mcp-config")
	}
	return append(args,
		"--model", request.Model,
		"--effort", effort,
		request.Prompt,
	), nil
}

func attachSessionArgs(sessionID string, request harness.Request) ([]string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("cannot attach to Claude Code session without a session id")
	}
	args := []string{"--resume", sessionID}
	if !request.MCP {
		args = append(args, "--strict-mcp-config")
	}
	return args, nil
}

type claudeContentBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Text  string          `json:"text"`
	Input json.RawMessage `json:"input"`
}

func subagentToolLabel(input json.RawMessage) string {
	var values struct {
		Description string `json:"description"`
		Prompt      string `json:"prompt"`
	}
	if err := json.Unmarshal(input, &values); err != nil {
		return "sub-agent"
	}
	if label := normalizeSubagentLabel(values.Description); label != "" {
		return label
	}
	if label := normalizeSubagentLabel(values.Prompt); label != "" {
		return label
	}
	return "sub-agent"
}

func normalizeSubagentLabel(label string) string {
	return strings.Join(strings.Fields(label), " ")
}

func subagentToolGist(input json.RawMessage) string {
	var values map[string]string
	if err := json.Unmarshal(input, &values); err != nil {
		return argumentGist(input)
	}
	for _, key := range []string{"file_path", "path", "command", "pattern"} {
		if value := strings.TrimSpace(values[key]); value != "" {
			return value
		}
	}
	return argumentGist(input)
}

func isSubagentTool(name string) bool {
	return name == "Agent" || name == "Task"
}

func transcriptSubagentLink(raw json.RawMessage) (string, string) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", ""
	}
	fields := make(map[string]string)
	collectTranscriptIdentifiers(value, fields)
	return fields["agentid"], fields["tooluseid"]
}

func collectTranscriptIdentifiers(value any, fields map[string]string) {
	switch value := value.(type) {
	case map[string]any:
		for key, nested := range value {
			normalizedKey := strings.ToLower(strings.ReplaceAll(key, "_", ""))
			if text, ok := nested.(string); ok && (normalizedKey == "agentid" || normalizedKey == "tooluseid") {
				if fields[normalizedKey] == "" {
					fields[normalizedKey] = strings.TrimSpace(text)
				}
			}
			collectTranscriptIdentifiers(nested, fields)
		}
	case []any:
		for _, nested := range value {
			collectTranscriptIdentifiers(nested, fields)
		}
	}
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
