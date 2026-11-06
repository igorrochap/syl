package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/igorrochap/syl/internal/harness/claude/transcript"
)

func TestReadSessionStateSummarizesTranscriptAndSkipsMalformedEntries(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	const sessionID = "review-session"
	reader := transcript.New(home)
	path, err := reader.Find(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	requestAt := time.Now().Add(-2 * time.Minute).UTC().Truncate(time.Second)
	contents := fmt.Sprintf(`{"type":"api-request","timestamp":%q}
not valid JSON
{"type":"queue-operation","operation":"enqueue","content":"<task-notification>one</task-notification>"}
{"type":"queue-operation","operation":"enqueue","content":"<task-notification>two</task-notification>"}
{"type":"queue-operation","operation":"remove","content":"<task-notification>three</task-notification>"}
{"type":"queue-operation","operation":"enqueue","content":"ordinary prompt"}
{"type":"unknown-future-entry","timestamp":"2026-11-03T10:00:00Z"}
`, requestAt.Format(time.RFC3339Nano))
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	subagentsDir := filepath.Join(filepath.Dir(path), sessionID, "subagents")
	if err := os.MkdirAll(subagentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{
		"agent-a.meta.json": `{"finished":true}`,
		"agent-b.meta.json": `{"finished":true}`,
	} {
		if err := os.WriteFile(filepath.Join(subagentsDir, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	adapter := New(root)
	adapter.homeDir = home
	state, err := adapter.ReadSessionState(sessionID)
	if err != nil {
		t.Fatalf("ReadSessionState() error = %v", err)
	}
	if state.LastMainEntryType != "unknown-future-entry" || state.LastMainEntryTimestamp != "2026-11-03T10:00:00Z" {
		t.Fatalf("last main entry = (%q, %q), want the last valid entry", state.LastMainEntryType, state.LastMainEntryTimestamp)
	}
	if !state.HasUnansweredAPIRequest || state.UnansweredAPIRequestAge < time.Minute {
		t.Fatalf("unanswered API request = (%t, %s), want an age over one minute", state.HasUnansweredAPIRequest, state.UnansweredAPIRequestAge)
	}
	if state.EnqueuedTaskNotifications != 2 || state.DequeuedOrRemovedTaskNotifications != 1 {
		t.Fatalf("task notification counts = (%d enqueued, %d dequeued/removed), want (2, 1)", state.EnqueuedTaskNotifications, state.DequeuedOrRemovedTaskNotifications)
	}
	if state.SubagentCount != 2 || state.FinishedSubagentCount != 2 {
		t.Fatalf("subagent counts = (%d total, %d finished), want (2, 2)", state.SubagentCount, state.FinishedSubagentCount)
	}
	for _, want := range []string{
		"Last main transcript entry: unknown-future-entry at 2026-11-03T10:00:00Z",
		"Unanswered api-request age:",
		"Task notifications: 2 enqueued, 1 dequeued or removed",
		"Subagents: 2 total, 2 finished",
	} {
		if !strings.Contains(state.String(), want) {
			t.Fatalf("SessionState.String() = %q, want %q", state.String(), want)
		}
	}
}

func TestReadSessionStateClearsAnsweredRequestsAndCountsTaskOperations(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "transcript.jsonl")
	contents := `not JSON
{"type":"api-request","timestamp":"not a timestamp"}
{"type":"assistant","message":{"role":"assistant","content":[]}}
{"type":"queue-operation","operation":"enqueue","content":"ordinary prompt one"}
{"type":"queue-operation","operation":"enqueue","content":{"text":["<task-notification>one</task-notification>"]}}
{"type":"queue-operation","operation":"enqueue","content":"ordinary prompt two"}
{"type":"queue-operation","operation":"enqueue","content":"<task-notification>two</task-notification>"}
{"type":"queue-operation","operation":"dequeue","content":"ordinary prompt one"}
{"type":"queue-operation","operation":"dequeue","content":"<task-notification>one</task-notification>"}
{"type":"queue-operation","operation":"dequeue"}
{"type":"queue-operation","operation":"dequeue"}
{"type":"queue-operation","operation":"remove","content":"ordinary prompt two"}
{"type":"final-entry"}
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	subagentsDir := filepath.Join(root, "subagents")
	if err := os.MkdirAll(filepath.Join(subagentsDir, "ignored.meta.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{
		"agent-a.meta.json": `{"finished":false}`,
		"agent-b.meta.json": `not JSON`,
	} {
		if err := os.WriteFile(filepath.Join(subagentsDir, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	state, err := readSessionState(path, subagentsDir, time.Now().UTC())
	if err != nil {
		t.Fatalf("readSessionState() error = %v", err)
	}
	if state.HasUnansweredAPIRequest || state.HasUnansweredAPIRequestAge {
		t.Fatalf("answered API request remained outstanding: %#v", state)
	}
	if state.LastMainEntryType != "final-entry" || state.LastMainEntryTimestamp != "" {
		t.Fatalf("last main entry = (%q, %q), want missing timestamp", state.LastMainEntryType, state.LastMainEntryTimestamp)
	}
	if state.EnqueuedTaskNotifications != 2 || state.DequeuedOrRemovedTaskNotifications != 2 {
		t.Fatalf("task notification counts = (%d, %d), want (2 notifications enqueued, 2 dequeued)", state.EnqueuedTaskNotifications, state.DequeuedOrRemovedTaskNotifications)
	}
	if state.SubagentCount != 2 || state.FinishedSubagentCount != 0 {
		t.Fatalf("subagent counts = (%d, %d), want (2, 0)", state.SubagentCount, state.FinishedSubagentCount)
	}
	if strings.Contains(state.String(), "Unanswered api-request age:") {
		t.Fatalf("summary contains an answered API request: %q", state.String())
	}
}

func TestReadSessionStateSinceScopesPendingTasksToSessionTurn(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "transcript.jsonl")
	contents := `{"type":"queue-operation","operation":"enqueue","content":"<task-notification>old</task-notification>"}
{"type":"assistant","message":{"role":"assistant","content":[]}}
{"type":"queue-operation","operation":"dequeue"}
{"type":"queue-operation","operation":"enqueue","content":"<task-notification>new</task-notification>"}
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	subagentsDir := filepath.Join(root, "subagents")
	if err := os.MkdirAll(subagentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subagentsDir, "agent-a.meta.json"), []byte(`{"finished":true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	currentTurn, err := readSessionStateSince(path, subagentsDir, time.Now().UTC(), 2)
	if err != nil {
		t.Fatalf("readSessionStateSince() error = %v", err)
	}
	if currentTurn.EnqueuedTaskNotifications != 1 || currentTurn.DequeuedOrRemovedTaskNotifications != 0 {
		t.Fatalf("current-turn task notifications = (%d, %d), want (1, 0)",
			currentTurn.EnqueuedTaskNotifications, currentTurn.DequeuedOrRemovedTaskNotifications)
	}
	if !sessionStateCanStall(currentTurn) {
		t.Fatal("new task notification should remain pending after the old notification is dequeued")
	}

	resumedAgain, err := readSessionStateSince(path, subagentsDir, time.Now().UTC(), 4)
	if err != nil {
		t.Fatalf("readSessionStateSince() error = %v", err)
	}
	if resumedAgain.EnqueuedTaskNotifications != 0 || resumedAgain.DequeuedOrRemovedTaskNotifications != 0 {
		t.Fatalf("new-session task notifications = (%d, %d), want (0, 0)",
			resumedAgain.EnqueuedTaskNotifications, resumedAgain.DequeuedOrRemovedTaskNotifications)
	}
	if sessionStateCanStall(resumedAgain) {
		t.Fatal("an older pending task notification should not stall a resumed turn")
	}
}

func TestReadSessionStateSinceClearsPreBaselinePendingTasks(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "transcript.jsonl")
	contents := `{"type":"queue-operation","operation":"enqueue","content":"<task-notification>old</task-notification>"}
{"type":"queue-operation","operation":"enqueue","content":"<task-notification>new</task-notification>"}
{"type":"queue-operation","operation":"dequeue"}
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	state, err := readSessionStateSince(path, filepath.Join(root, "subagents"), time.Now().UTC(), 1)
	if err != nil {
		t.Fatalf("readSessionStateSince() error = %v", err)
	}
	if state.EnqueuedTaskNotifications != 1 || state.DequeuedOrRemovedTaskNotifications != 1 {
		t.Fatalf("current-turn task notifications = (%d, %d), want (1, 1)",
			state.EnqueuedTaskNotifications, state.DequeuedOrRemovedTaskNotifications)
	}
	if sessionStateCanStall(state) {
		t.Fatal("a current-turn dequeue should resolve its notification despite an older pending enqueue")
	}
}

func TestSessionStateCanStallRequiresAtLeastOneFinishedSubagent(t *testing.T) {
	pendingNotification := SessionState{EnqueuedTaskNotifications: 1}
	if sessionStateCanStall(pendingNotification) {
		t.Fatal("pending notification without subagents should not count as a stall")
	}

	unfinishedSubagent := SessionState{
		EnqueuedTaskNotifications: 1,
		SubagentCount:             1,
	}
	if sessionStateCanStall(unfinishedSubagent) {
		t.Fatal("pending notification with an unfinished subagent should not count as a stall")
	}

	finishedSubagent := unfinishedSubagent
	finishedSubagent.FinishedSubagentCount = 1
	if !sessionStateCanStall(finishedSubagent) {
		t.Fatal("pending notification with a finished subagent should count as a stall")
	}
}

func TestReadSessionStateHandlesMissingTranscriptAndReportsOtherReadErrors(t *testing.T) {
	root := t.TempDir()
	missingState, err := readSessionState(filepath.Join(root, "missing.jsonl"), filepath.Join(root, "subagents"), time.Now())
	if err != nil || missingState.LastMainEntryType != "" {
		t.Fatalf("missing transcript state = (%#v, %v), want empty state without error", missingState, err)
	}
	parent := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(parent, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = readSessionState(filepath.Join(parent, "transcript.jsonl"), filepath.Join(root, "subagents"), time.Now())
	if err == nil || !strings.Contains(err.Error(), "open Claude transcript for session state") {
		t.Fatalf("readSessionState() error = %v, want transcript open error", err)
	}
}

func TestReadSessionStateHandlesMissingFieldsFutureRequestsAndEmptyQueues(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "transcript.jsonl")
	contents := `{"timestamp":"entry without a type"}
{"type":"api-request"}
{"type":"api_request","timestamp":"2030-01-01T00:00:00Z"}
{"type":"message","message":{"role":"assistant"}}
{"type":"api-request","timestamp":"2030-01-01T00:00:00Z"}
{"type":"queue-operation","operation":"dequeue"}
{"type":"queue-operation","operation":"remove"}
{"type":"queue-operation","operation":"enqueue","content":"<task-notification>one</task-notification>"}
{"type":"queue-operation","operation":"remove","content":"ordinary prompt"}
{"type":"queue-operation","operation":"dequeue"}
{"type":"queue-operation","operation":"dequeue"}
{"type":"final-entry"}
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	subagentsDir := filepath.Join(root, "subagents")
	if err := os.MkdirAll(subagentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "missing-meta"), filepath.Join(subagentsDir, "agent-unreadable.meta.json")); err != nil {
		t.Fatal(err)
	}

	state, err := readSessionState(path, subagentsDir, time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("readSessionState() error = %v", err)
	}
	if !state.HasUnansweredAPIRequest || !state.HasUnansweredAPIRequestAge || state.UnansweredAPIRequestAge != 0 {
		t.Fatalf("future unanswered API request = (%t, %t, %s), want (true, true, 0s)",
			state.HasUnansweredAPIRequest, state.HasUnansweredAPIRequestAge, state.UnansweredAPIRequestAge)
	}
	if state.EnqueuedTaskNotifications != 1 || state.DequeuedOrRemovedTaskNotifications != 1 {
		t.Fatalf("task notification counts = (%d, %d), want (1, 1)",
			state.EnqueuedTaskNotifications, state.DequeuedOrRemovedTaskNotifications)
	}
	if state.SubagentCount != 1 || state.FinishedSubagentCount != 0 {
		t.Fatalf("subagent counts = (%d, %d), want unreadable metadata counted but unfinished", state.SubagentCount, state.FinishedSubagentCount)
	}
}

func TestReadSessionStateReportsOversizedTranscriptEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 17<<20)), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := readSessionState(path, t.TempDir(), time.Now())
	if err == nil || !strings.Contains(err.Error(), "read Claude transcript for session state") {
		t.Fatalf("readSessionState() error = %v, want scanner read error", err)
	}
}

func TestContainsTaskNotificationHandlesMalformedAndUnsupportedContent(t *testing.T) {
	if !containsTaskNotification(json.RawMessage(`invalid <task-notification> content`)) {
		t.Fatal("containsTaskNotification() missed a tag in malformed raw content")
	}
	if containsTaskNotificationValue(float64(42)) || containsTaskNotificationValue(nil) {
		t.Fatal("containsTaskNotificationValue() matched unsupported content")
	}
}
