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

type claudeContentBlock struct {
	Type  string          `json:"type"`
	Name  string          `json:"name"`
	Text  string          `json:"text"`
	Input json.RawMessage `json:"input"`
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
