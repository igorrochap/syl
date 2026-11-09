package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/igorrochap/syl/internal/harness/claude/transcript"
)

func TestReadFinishedSubagentReportsExcludesReportsAlreadyDelivered(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	const sessionID = "resumed-review"
	reader := transcript.New(home)
	mainPath, err := reader.Find(root, sessionID)
	if err != nil {
		t.Fatalf("find parent transcript: %v", err)
	}
	subagentsDir := filepath.Join(filepath.Dir(mainPath), sessionID, "subagents")
	if err := os.MkdirAll(subagentsDir, 0o755); err != nil {
		t.Fatalf("create subagents directory: %v", err)
	}

	previousPath := writeFinishedSubagent(t, subagentsDir, "agent-previous", "Earlier iteration report.")
	currentPath := writeFinishedSubagent(t, subagentsDir, "agent-current", "Current iteration report.")
	mainPrompt := `{"type":"user","message":{"role":"user","content":"Finished subagent reports:\n- ` + previousPath + `"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"I saw ` + currentPath + ` in a tool result."}}` + "\n"
	if err := os.WriteFile(mainPath, []byte(mainPrompt), 0o600); err != nil {
		t.Fatalf("write main transcript: %v", err)
	}
	startState, err := snapshotSubagents(reader, root, sessionID)
	if err != nil {
		t.Fatalf("snapshot subagents: %v", err)
	}

	reports := readFinishedSubagentReports(
		reader,
		root,
		sessionID,
		mainPath,
		startState,
	)
	if len(reports) != 1 || reports[0].Path != currentPath || reports[0].Text != "Current iteration report." {
		t.Fatalf("reports = %#v, want only the undelivered finished report", reports)
	}
}

func TestReadFinishedSubagentReportsExcludesToolResultsBeforeUsingTextBudget(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	const sessionID = "tool-result-review"
	reader := transcript.New(home)
	mainPath, err := reader.Find(root, sessionID)
	if err != nil {
		t.Fatalf("find parent transcript: %v", err)
	}
	subagentsDir := filepath.Join(filepath.Dir(mainPath), sessionID, "subagents")
	if err := os.MkdirAll(subagentsDir, 0o755); err != nil {
		t.Fatalf("create subagents directory: %v", err)
	}
	largeUndelivered := strings.Repeat("earlier undelivered finding ", 4000)
	largeDeliveredByText := "Delivered by text: " + strings.Repeat("stale finding ", 4000)
	writeFinishedSubagent(t, subagentsDir, "agent-a-old", largeUndelivered)
	writeFinishedSubagent(t, subagentsDir, "agent-b-id", strings.Repeat("delivered by id ", 4000))
	writeFinishedSubagent(t, subagentsDir, "agent-c-text", largeDeliveredByText)
	startState, err := snapshotSubagents(reader, root, sessionID)
	if err != nil {
		t.Fatalf("snapshot subagents: %v", err)
	}
	writeFinishedSubagent(t, subagentsDir, "agent-z-current", "Current turn report.")
	mainTranscript := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":[{"type":"text","text":"Completed agent tool result"}]}]},"toolUseResult":{"status":"completed","agentId":"b-id"}}` + "\n" +
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":[{"type":"text","text":"` + largeDeliveredByText + `"}]}]},"toolUseResult":{"status":"completed"}}` + "\n"
	if err := os.WriteFile(mainPath, []byte(mainTranscript), 0o600); err != nil {
		t.Fatalf("write main transcript: %v", err)
	}

	reports := readFinishedSubagentReports(reader, root, sessionID, mainPath, startState)
	if len(reports) != 2 {
		t.Fatalf("reports = %#v, want the current report before one undelivered earlier report", reports)
	}
	if reports[0].Path != filepath.Join(subagentsDir, "agent-z-current.jsonl") || reports[0].Text != "Current turn report." {
		t.Fatalf("first report = %#v, want the new current-turn report first", reports[0])
	}
	if reports[1].Path != filepath.Join(subagentsDir, "agent-a-old.jsonl") || !reports[1].TooLarge {
		t.Fatalf("second report = %#v, want the undelivered old report with remaining budget", reports[1])
	}
}

func TestReadFinishedSubagentReportsDoesNotTreatLaunchResultAsDelivery(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	const sessionID = "async-launch-review"
	reader := transcript.New(home)
	mainPath, err := reader.Find(root, sessionID)
	if err != nil {
		t.Fatalf("find parent transcript: %v", err)
	}
	subagentsDir := filepath.Join(filepath.Dir(mainPath), sessionID, "subagents")
	if err := os.MkdirAll(subagentsDir, 0o755); err != nil {
		t.Fatalf("create subagents directory: %v", err)
	}
	const reportText = "Finished background agent report."
	reportPath := writeFinishedSubagent(t, subagentsDir, "agent-launched", reportText)
	startState, err := snapshotSubagents(reader, root, sessionID)
	if err != nil {
		t.Fatalf("snapshot subagents: %v", err)
	}
	launchRecord := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":[{"type":"text","text":"Background agent launched"}]}]},"toolUseResult":{"status":"async_launched","agentId":"launched"}}` + "\n"
	if err := os.WriteFile(mainPath, []byte(launchRecord), 0o600); err != nil {
		t.Fatalf("write launch record: %v", err)
	}

	reports := readFinishedSubagentReports(reader, root, sessionID, mainPath, startState)
	if len(reports) != 1 || reports[0].Path != reportPath || reports[0].Text != reportText {
		t.Fatalf("reports = %#v, want the report for the launched but undelivered agent", reports)
	}
}

func TestReadFinishedSubagentReportsTreatsTaskNotificationAsDelivery(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	const sessionID = "task-notification-review"
	reader := transcript.New(home)
	mainPath, err := reader.Find(root, sessionID)
	if err != nil {
		t.Fatalf("find parent transcript: %v", err)
	}
	subagentsDir := filepath.Join(filepath.Dir(mainPath), sessionID, "subagents")
	if err := os.MkdirAll(subagentsDir, 0o755); err != nil {
		t.Fatalf("create subagents directory: %v", err)
	}
	writeFinishedSubagent(t, subagentsDir, "agent-notified", "Finished notified agent report.")
	startState, err := snapshotSubagents(reader, root, sessionID)
	if err != nil {
		t.Fatalf("snapshot subagents: %v", err)
	}
	notification := `{"type":"user","message":{"role":"user","content":"<task-notification>agentId: notified</task-notification>"}}` + "\n"
	if err := os.WriteFile(mainPath, []byte(notification), 0o600); err != nil {
		t.Fatalf("write task notification: %v", err)
	}

	reports := readFinishedSubagentReports(reader, root, sessionID, mainPath, startState)
	if len(reports) != 0 {
		t.Fatalf("reports = %#v, want the task-notified report excluded", reports)
	}
}

func writeFinishedSubagent(t *testing.T, dir, name, text string) string {
	t.Helper()
	path := filepath.Join(dir, name+".jsonl")
	contents := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"` + text + `"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write subagent transcript: %v", err)
	}
	metadata := filepath.Join(dir, name+".meta.json")
	if err := os.WriteFile(metadata, []byte(`{"finished":true}`), 0o600); err != nil {
		t.Fatalf("write subagent metadata: %v", err)
	}
	return path
}
