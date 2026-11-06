package claude

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/harness/claude/transcript"
)

const subagentTranscriptLineLimit = 16 * 1024 * 1024

func readFinishedSubagentReports(reader transcript.Reader, projectRoot, sessionID string) []harness.SubagentReport {
	paths, err := reader.FindSubagents(projectRoot, sessionID)
	if err != nil {
		return nil
	}

	var reports []harness.SubagentReport
	remainingTextBytes := harness.StalledSessionReportTextLimit
	for _, path := range paths {
		if !isAgentTranscript(path) || !subagentIsFinished(path) {
			continue
		}
		report := harness.SubagentReport{Path: path}
		text, tooLarge, err := readFinalAssistantText(path, remainingTextBytes)
		if err != nil {
			report.TooLarge = true
			reports = append(reports, report)
			remainingTextBytes = 0
			continue
		}
		report.Text = text
		report.TooLarge = tooLarge
		remainingTextBytes -= len(text)
		if tooLarge {
			remainingTextBytes = 0
		}
		reports = append(reports, report)
	}
	return reports
}

func isAgentTranscript(path string) bool {
	name := filepath.Base(path)
	return strings.HasPrefix(name, "agent-") && strings.HasSuffix(name, ".jsonl")
}

func subagentIsFinished(path string) bool {
	metadataPath := strings.TrimSuffix(path, ".jsonl") + ".meta.json"
	contents, err := os.ReadFile(metadataPath)
	if err != nil {
		return false
	}
	var metadata struct {
		Finished bool `json:"finished"`
	}
	return json.Unmarshal(contents, &metadata) == nil && metadata.Finished
}

func readFinalAssistantText(path string, limit int) (string, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), subagentTranscriptLineLimit)
	var finalText string
	var finalTooLarge bool
	for scanner.Scan() {
		var message transcriptMessage
		if json.Unmarshal(scanner.Bytes(), &message) != nil || !isAssistantTranscriptMessage(message) {
			continue
		}
		blocks, err := transcriptContentBlocks(message.Message.Content)
		if err != nil {
			continue
		}
		text, tooLarge := assistantText(blocks, limit)
		if text != "" || tooLarge {
			finalText, finalTooLarge = text, tooLarge
		}
	}
	if err := scanner.Err(); err != nil {
		return "", false, err
	}
	return finalText, finalTooLarge, nil
}

func assistantText(blocks []claudeContentBlock, limit int) (string, bool) {
	var text strings.Builder
	for _, block := range blocks {
		if block.Type != "text" {
			continue
		}
		if text.Len()+len(block.Text) > limit {
			return text.String(), true
		}
		text.WriteString(block.Text)
	}
	return text.String(), false
}
