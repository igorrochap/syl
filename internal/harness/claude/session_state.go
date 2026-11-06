package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/igorrochap/syl/internal/harness/claude/transcript"
)

// SessionState summarizes the main transcript and subagent metadata for one Claude session.
type SessionState struct {
	LastMainEntryType                  string
	LastMainEntryTimestamp             string
	HasUnansweredAPIRequest            bool
	HasUnansweredAPIRequestAge         bool
	UnansweredAPIRequestAge            time.Duration
	EnqueuedTaskNotifications          int
	DequeuedOrRemovedTaskNotifications int
	SubagentCount                      int
	FinishedSubagentCount              int
}

// String returns the stable, human-readable session state summary.
func (state SessionState) String() string {
	entryType := state.LastMainEntryType
	if entryType == "" {
		entryType = "unknown"
	}
	timestamp := state.LastMainEntryTimestamp
	if timestamp == "" {
		timestamp = "unknown"
	}
	lines := []string{
		fmt.Sprintf("Last main transcript entry: %s at %s", entryType, timestamp),
	}
	if state.HasUnansweredAPIRequest {
		age := "unavailable"
		if state.HasUnansweredAPIRequestAge {
			age = state.UnansweredAPIRequestAge.Round(time.Second).String()
		}
		lines = append(lines, "Unanswered api-request age: "+age)
	}
	lines = append(lines,
		fmt.Sprintf("Task notifications: %d enqueued, %d dequeued or removed",
			state.EnqueuedTaskNotifications, state.DequeuedOrRemovedTaskNotifications),
		fmt.Sprintf("Subagents: %d total, %d finished", state.SubagentCount, state.FinishedSubagentCount),
	)
	return strings.Join(lines, "\n")
}

// ReadSessionState reads the latest transcript state for a Claude session.
// Malformed or unknown transcript entries are skipped so diagnostics remain useful.
func (a *PTYAdapter) ReadSessionState(sessionID string) (SessionState, error) {
	return a.readSessionState(sessionID, 0)
}

// ReadSessionStateSince summarizes a session while counting queue operations
// only from the specified main-transcript entry onward.
func (a *PTYAdapter) ReadSessionStateSince(sessionID string, firstCurrentEntry int) (SessionState, error) {
	return a.readSessionState(sessionID, firstCurrentEntry)
}

func (a *PTYAdapter) readSessionState(sessionID string, firstCurrentEntry int) (SessionState, error) {
	sessionID = strings.TrimSpace(sessionID)
	if firstCurrentEntry < 0 {
		firstCurrentEntry = 0
	}
	reader := transcript.New(a.homeDir)
	path, err := reader.Find(a.projectRoot, sessionID)
	if err != nil {
		return SessionState{}, fmt.Errorf("find Claude transcript for session state: %w", err)
	}
	subagentsDir := filepath.Join(filepath.Dir(path), sessionID, "subagents")
	return readSessionStateSince(path, subagentsDir, time.Now().UTC(), firstCurrentEntry)
}

func readSessionState(path, subagentsDir string, now time.Time) (SessionState, error) {
	return readSessionStateSince(path, subagentsDir, now, 0)
}

func readSessionStateSince(path, subagentsDir string, now time.Time, firstCurrentEntry int) (SessionState, error) {
	state := SessionState{}
	if err := readMainTranscriptState(path, now, &state, firstCurrentEntry); err != nil {
		return state, err
	}
	readSubagentMetadata(subagentsDir, &state)
	return state, nil
}

func readMainTranscriptState(path string, now time.Time, state *SessionState, firstCurrentEntry int) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open Claude transcript for session state %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	reader := transcriptStateReader{state: state}
	entryIndex := 0
	for scanner.Scan() {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		if entryIndex == firstCurrentEntry {
			reader.pendingTaskNotifications = nil
		}
		reader.readEntry(raw, entryIndex >= firstCurrentEntry)
		entryIndex++
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read Claude transcript for session state %s: %w", path, err)
	}
	reader.setUnansweredRequestAge(now)
	return nil
}

type transcriptStateReader struct {
	state                    *SessionState
	unansweredRequestTime    time.Time
	pendingTaskNotifications []bool
}

func (reader *transcriptStateReader) readEntry(raw []byte, isCurrentTurn bool) {
	var entry map[string]json.RawMessage
	if json.Unmarshal(raw, &entry) != nil || entry == nil {
		return
	}
	entryType := jsonString(entry["type"])
	if entryType == "" {
		return
	}
	reader.state.LastMainEntryType = entryType
	reader.state.LastMainEntryTimestamp = jsonString(entry["timestamp"])
	if isAPIRequest(entryType) {
		reader.setUnansweredAPIRequest(entry["timestamp"])
		return
	}
	if isAssistantEntry(entryType, entry["message"]) {
		reader.state.HasUnansweredAPIRequest = false
		reader.state.HasUnansweredAPIRequestAge = false
		reader.unansweredRequestTime = time.Time{}
	}
	reader.countTaskNotificationOperation(entry, isCurrentTurn)
}

func (reader *transcriptStateReader) setUnansweredAPIRequest(rawTimestamp json.RawMessage) {
	reader.state.HasUnansweredAPIRequest = true
	reader.state.HasUnansweredAPIRequestAge = false
	reader.unansweredRequestTime = time.Time{}
	timestamp := jsonString(rawTimestamp)
	if timestamp == "" {
		return
	}
	parsed, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return
	}
	reader.unansweredRequestTime = parsed
	reader.state.HasUnansweredAPIRequestAge = true
}

func (reader *transcriptStateReader) setUnansweredRequestAge(now time.Time) {
	if !reader.state.HasUnansweredAPIRequest || !reader.state.HasUnansweredAPIRequestAge {
		return
	}
	reader.state.UnansweredAPIRequestAge = now.Sub(reader.unansweredRequestTime)
	if reader.state.UnansweredAPIRequestAge < 0 {
		reader.state.UnansweredAPIRequestAge = 0
	}
}

func jsonString(raw json.RawMessage) string {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func isAPIRequest(entryType string) bool {
	return entryType == "api-request" || entryType == "api_request"
}

func isAssistantEntry(entryType string, rawMessage json.RawMessage) bool {
	if entryType == "assistant" {
		return true
	}
	var message struct {
		Role string `json:"role"`
	}
	return json.Unmarshal(rawMessage, &message) == nil && message.Role == "assistant"
}

func (reader *transcriptStateReader) countTaskNotificationOperation(entry map[string]json.RawMessage, isCurrentTurn bool) {
	entryType := jsonString(entry["type"])
	if entryType != "queue-operation" && entryType != "queue_operation" {
		return
	}
	operation := jsonString(entry["operation"])
	switch operation {
	case "enqueue":
		reader.enqueueTaskNotification(entry["content"], isCurrentTurn)
	case "dequeue":
		reader.resolveTaskNotification(entry["content"], false, isCurrentTurn)
	case "remove":
		reader.resolveTaskNotification(entry["content"], true, isCurrentTurn)
	}
}

func (reader *transcriptStateReader) enqueueTaskNotification(content json.RawMessage, isCurrentTurn bool) {
	isTaskNotification := containsTaskNotification(content)
	reader.pendingTaskNotifications = append(reader.pendingTaskNotifications, isTaskNotification)
	if isTaskNotification && isCurrentTurn {
		reader.state.EnqueuedTaskNotifications++
	}
}

func (reader *transcriptStateReader) resolveTaskNotification(content json.RawMessage, remove, isCurrentTurn bool) {
	var removed bool
	var matched bool
	if remove {
		removed, matched, reader.pendingTaskNotifications = removeIsTaskNotification(content, reader.pendingTaskNotifications)
	} else {
		removed, matched, reader.pendingTaskNotifications = dequeueIsTaskNotification(content, reader.pendingTaskNotifications)
	}
	if matched && removed && isCurrentTurn {
		reader.state.DequeuedOrRemovedTaskNotifications++
	}
}

func dequeueIsTaskNotification(
	content json.RawMessage,
	pendingTaskNotifications []bool,
) (bool, bool, []bool) {
	if hasQueuePayload(content) {
		isTaskNotification := containsTaskNotification(content)
		return removePendingQueueEntry(pendingTaskNotifications, isTaskNotification)
	}
	return takeOldestPendingTaskNotification(pendingTaskNotifications)
}

func removeIsTaskNotification(
	content json.RawMessage,
	pendingTaskNotifications []bool,
) (bool, bool, []bool) {
	if !hasQueuePayload(content) {
		return false, false, pendingTaskNotifications
	}
	isTaskNotification := containsTaskNotification(content)
	return removePendingQueueEntry(pendingTaskNotifications, isTaskNotification)
}

func hasQueuePayload(content json.RawMessage) bool {
	value := strings.TrimSpace(string(content))
	return value != "" && value != "null"
}

func takeOldestPendingTaskNotification(
	pendingTaskNotifications []bool,
) (bool, bool, []bool) {
	if len(pendingTaskNotifications) == 0 {
		return false, false, pendingTaskNotifications
	}
	return pendingTaskNotifications[0], true, pendingTaskNotifications[1:]
}

func removePendingQueueEntry(
	pendingTaskNotifications []bool,
	isTaskNotification bool,
) (bool, bool, []bool) {
	for index, pendingIsTaskNotification := range pendingTaskNotifications {
		if pendingIsTaskNotification != isTaskNotification {
			continue
		}
		copy(pendingTaskNotifications[index:], pendingTaskNotifications[index+1:])
		return pendingIsTaskNotification, true, pendingTaskNotifications[:len(pendingTaskNotifications)-1]
	}
	return false, false, pendingTaskNotifications
}

func containsTaskNotification(raw json.RawMessage) bool {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return strings.Contains(string(raw), "<task-notification>")
	}
	return containsTaskNotificationValue(value)
}

func containsTaskNotificationValue(value any) bool {
	switch value := value.(type) {
	case string:
		return strings.Contains(value, "<task-notification>")
	case []any:
		for _, item := range value {
			if containsTaskNotificationValue(item) {
				return true
			}
		}
	case map[string]any:
		for _, item := range value {
			if containsTaskNotificationValue(item) {
				return true
			}
		}
	}
	return false
}

func readSubagentMetadata(directory string, state *SessionState) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".meta.json") {
			continue
		}
		state.SubagentCount++
		contents, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			continue
		}
		var metadata struct {
			Finished bool `json:"finished"`
		}
		if json.Unmarshal(contents, &metadata) == nil && metadata.Finished {
			state.FinishedSubagentCount++
		}
	}
}
