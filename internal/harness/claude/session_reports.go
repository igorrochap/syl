package claude

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/harness/claude/transcript"
)

const subagentTranscriptLineLimit = 16 * 1024 * 1024

type subagentStartState struct {
	sizes    map[string]int64
	finished map[string]bool
}

type reportDeliveryEvidence struct {
	paths    map[string]struct{}
	agentIDs map[string]struct{}
	texts    []string
}

func snapshotSubagents(reader transcript.Reader, projectRoot, sessionID string) (subagentStartState, error) {
	sizes, err := subagentTranscriptSizes(reader, projectRoot, sessionID)
	if err != nil {
		return subagentStartState{}, err
	}
	finished := make(map[string]bool, len(sizes))
	for path := range sizes {
		finished[path] = subagentIsFinished(path)
	}
	return subagentStartState{sizes: sizes, finished: finished}, nil
}

func readFinishedSubagentReports(
	reader transcript.Reader,
	projectRoot, sessionID string,
	mainTranscriptPath string,
	startState subagentStartState,
) []harness.SubagentReport {
	paths, err := reader.FindSubagents(projectRoot, sessionID)
	if err != nil {
		return nil
	}
	delivered := subagentReportDeliveryEvidence(reader, mainTranscriptPath, paths)
	sortSubagentsByTurn(paths, startState)

	var reports []harness.SubagentReport
	remainingTextBytes := harness.StalledSessionReportTextLimit
	for _, path := range paths {
		report, include, remaining := readUndeliveredSubagentReport(path, remainingTextBytes, delivered)
		remainingTextBytes = remaining
		if include {
			reports = append(reports, report)
		}
	}
	return reports
}

func sortSubagentsByTurn(paths []string, startState subagentStartState) {
	sort.SliceStable(paths, func(i, j int) bool {
		return subagentChangedThisTurn(paths[i], startState) && !subagentChangedThisTurn(paths[j], startState)
	})
}

func readUndeliveredSubagentReport(
	path string,
	remainingTextBytes int,
	delivered reportDeliveryEvidence,
) (harness.SubagentReport, bool, int) {
	if !isAgentTranscript(path) || !subagentIsFinished(path) {
		return harness.SubagentReport{}, false, remainingTextBytes
	}
	if _, wasDelivered := delivered.paths[path]; wasDelivered || deliveredByAgentID(agentTranscriptID(path), delivered) {
		return harness.SubagentReport{}, false, remainingTextBytes
	}
	text, tooLarge, err := readFinalAssistantText(path, subagentTranscriptLineLimit)
	if err != nil {
		return harness.SubagentReport{Path: path, TooLarge: true}, true, 0
	}
	if deliveredByText(text, delivered) {
		return harness.SubagentReport{}, false, remainingTextBytes
	}
	report := harness.SubagentReport{Path: path}
	report.Text, report.TooLarge = truncateReportText(text, remainingTextBytes)
	report.TooLarge = report.TooLarge || tooLarge
	remainingTextBytes -= len(text)
	if report.TooLarge {
		remainingTextBytes = 0
	}
	return report, true, remainingTextBytes
}

func subagentChangedThisTurn(path string, startState subagentStartState) bool {
	initialSize, existed := startState.sizes[path]
	if !existed || !startState.finished[path] {
		return true
	}
	info, err := os.Stat(path)
	return err == nil && info.Size() > initialSize
}

func truncateReportText(text string, limit int) (string, bool) {
	if len(text) <= limit {
		return text, false
	}
	return text[:limit], true
}

func subagentReportDeliveryEvidence(reader transcript.Reader, mainTranscriptPath string, reportPaths []string) reportDeliveryEvidence {
	entries, err := reader.Read(mainTranscriptPath)
	if err != nil {
		return reportDeliveryEvidence{}
	}
	delivered := reportDeliveryEvidence{
		paths:    make(map[string]struct{}),
		agentIDs: make(map[string]struct{}),
	}
	for _, entry := range entries {
		collectTranscriptEntryDeliveryEvidence(entry.Raw, reportPaths, &delivered)
	}
	return delivered
}

func collectTranscriptEntryDeliveryEvidence(
	raw json.RawMessage,
	reportPaths []string,
	evidence *reportDeliveryEvidence,
) {
	var message transcriptMessage
	if json.Unmarshal(raw, &message) != nil {
		return
	}
	if message.Type == "user" || message.Message.Role == "user" {
		collectPromptDeliveryEvidence(message.Message.Content, reportPaths, evidence)
		collectTaskNotificationEvidence(message.Message.Content, evidence)
	}
	collectToolResultDeliveryEvidence(raw, evidence)
}

func collectPromptDeliveryEvidence(content json.RawMessage, reportPaths []string, evidence *reportDeliveryEvidence) {
	text := userMessageText(content)
	for _, path := range reportPaths {
		if strings.Contains(text, path) {
			evidence.paths[path] = struct{}{}
		}
	}
}

func collectToolResultDeliveryEvidence(raw json.RawMessage, evidence *reportDeliveryEvidence) {
	var record map[string]json.RawMessage
	if json.Unmarshal(raw, &record) != nil {
		return
	}
	var toolUseResult any
	_ = json.Unmarshal(record["toolUseResult"], &toolUseResult)
	completed := hasCompletedToolResultStatus(toolUseResult)
	var messageRecord map[string]json.RawMessage
	if json.Unmarshal(record["message"], &messageRecord) != nil {
		return
	}
	var content any
	if json.Unmarshal(messageRecord["content"], &content) != nil || !collectToolResultEvidence(content, evidence, completed) {
		return
	}
	if completed {
		collectStringEvidence(toolUseResult, evidence)
	}
}

func collectTaskNotificationEvidence(content json.RawMessage, evidence *reportDeliveryEvidence) {
	text := userMessageText(content)
	if strings.Contains(strings.ToLower(text), "task-notification") {
		evidence.texts = append(evidence.texts, text)
	}
}

func collectToolResultEvidence(value any, evidence *reportDeliveryEvidence, completed bool) bool {
	switch value := value.(type) {
	case []any:
		found := false
		for _, item := range value {
			found = collectToolResultEvidence(item, evidence, completed) || found
		}
		return found
	case map[string]any:
		if value["type"] == "tool_result" {
			if completed || hasCompletedToolResultStatus(value) {
				collectStringEvidence(value, evidence)
				return true
			}
			return false
		}
		found := false
		for _, item := range value {
			found = collectToolResultEvidence(item, evidence, completed) || found
		}
		return found
	default:
		return false
	}
}

func hasCompletedToolResultStatus(value any) bool {
	result, ok := value.(map[string]any)
	if !ok {
		return false
	}
	status, ok := result["status"].(string)
	return ok && status == "completed"
}

func collectStringEvidence(value any, evidence *reportDeliveryEvidence) {
	switch value := value.(type) {
	case string:
		evidence.texts = append(evidence.texts, value)
	case []any:
		for _, item := range value {
			collectStringEvidence(item, evidence)
		}
	case map[string]any:
		for key, item := range value {
			if strings.EqualFold(key, "agentId") || strings.EqualFold(key, "agent_id") {
				if agentID, ok := item.(string); ok {
					evidence.agentIDs[agentID] = struct{}{}
				}
			}
			collectStringEvidence(item, evidence)
		}
	}
}

func deliveredByAgentID(agentID string, evidence reportDeliveryEvidence) bool {
	if agentID == "" {
		return false
	}
	if _, found := evidence.agentIDs[agentID]; found {
		return true
	}
	fullAgentID := "agent-" + agentID
	if _, found := evidence.agentIDs[fullAgentID]; found {
		return true
	}
	for _, text := range evidence.texts {
		if strings.Contains(text, agentID) || strings.Contains(text, fullAgentID) {
			return true
		}
	}
	return false
}

func deliveredByText(reportText string, evidence reportDeliveryEvidence) bool {
	reportText = strings.TrimSpace(reportText)
	if reportText == "" {
		return false
	}
	for _, text := range evidence.texts {
		if strings.Contains(text, reportText) {
			return true
		}
	}
	return false
}

func agentTranscriptID(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	return strings.TrimPrefix(name, "agent-")
}

func userMessageText(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text
	}
	blocks, err := transcriptContentBlocks(content)
	if err != nil {
		return ""
	}
	var builder strings.Builder
	for _, block := range blocks {
		if block.Type == "text" {
			builder.WriteString(block.Text)
		}
	}
	return builder.String()
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
