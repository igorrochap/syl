package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/harness/claude/transcript"
)

func TestDecodeTranscriptEntryEmitsSubagentSpawn(t *testing.T) {
	contents := readClaudeFixture(t, "testdata/subagents/role-spawns.jsonl")
	lines := strings.Split(strings.TrimSpace(string(contents)), "\n")
	var firstEntry, secondEntry, thirdEntry, fourthEntry json.RawMessage
	if err := json.Unmarshal([]byte(lines[0]), &firstEntry); err != nil {
		t.Fatalf("parse spawn transcript fixture: %v", err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &secondEntry); err != nil {
		t.Fatalf("parse second spawn fixture: %v", err)
	}
	if err := json.Unmarshal([]byte(lines[2]), &thirdEntry); err != nil {
		t.Fatalf("parse first tool result fixture: %v", err)
	}
	if err := json.Unmarshal([]byte(lines[3]), &fourthEntry); err != nil {
		t.Fatalf("parse second tool result fixture: %v", err)
	}
	events, err := decodeTranscriptEntry(firstEntry)
	if err != nil {
		t.Fatalf("decode spawn transcript entry: %v", err)
	}
	if len(events) != 2 || events[0].Type != harness.EventToolUse || events[1].Type != harness.EventSubagentStarted {
		t.Fatalf("spawn events = %#v, want tool use followed by subagent start", events)
	}
	if events[1].SubagentID != "toolu-standards" || events[1].SubagentLabel != "Standards review" {
		t.Fatalf("spawn event = %#v, want tool-use id and description label", events[1])
	}
	secondSpawn, err := decodeTranscriptEntry(secondEntry)
	if err != nil || len(secondSpawn) != 2 || secondSpawn[1].Type != harness.EventSubagentStarted || secondSpawn[1].SubagentID != "toolu-spec" || secondSpawn[1].SubagentLabel != "Spec review" {
		t.Fatalf("second spawn events = %#v, error = %v, want Agent spawn with description label", secondSpawn, err)
	}
	if agentID, toolUseID := transcriptSubagentLink(thirdEntry); agentID != "standards" || toolUseID != "toolu-standards" {
		t.Fatalf("transcript link = (%q, %q), want (standards, toolu-standards)", agentID, toolUseID)
	}
	if agentID, toolUseID := transcriptSubagentLink(fourthEntry); agentID != "spec" || toolUseID != "toolu-spec" {
		t.Fatalf("second transcript link = (%q, %q), want (spec, toolu-spec)", agentID, toolUseID)
	}
}

func TestResumedTranscriptStateRestoresSubagentLinks(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	reader := transcript.New(home)
	path, err := reader.Find(root, "resumed-child-span-session")
	if err != nil {
		t.Fatalf("find transcript path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create transcript directory: %v", err)
	}
	if err := os.WriteFile(path, readClaudeFixture(t, "testdata/subagents/role-spawns.jsonl"), 0o600); err != nil {
		t.Fatalf("write transcript fixture: %v", err)
	}
	seenEntries, links, err := resumedTranscriptState(reader, path, transcriptStartResume)
	if err != nil {
		t.Fatalf("read resumed transcript state: %v", err)
	}
	if seenEntries != 4 || links["standards"] != "toolu-standards" || links["spec"] != "toolu-spec" {
		t.Fatalf("resumed transcript state = (%d, %#v), want two restored child links", seenEntries, links)
	}
}

func TestTranscriptMonitorEmitsSubagentToolAndFinishEvents(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	reader := transcript.New(home)
	mainPath, err := reader.Find(root, "child-span-session")
	if err != nil {
		t.Fatalf("find main transcript path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(mainPath), 0o755); err != nil {
		t.Fatalf("create main transcript directory: %v", err)
	}
	mainContents := readClaudeFixture(t, "testdata/subagents/role-spawns.jsonl")
	if err := os.WriteFile(mainPath, mainContents, 0o600); err != nil {
		t.Fatalf("write main transcript fixture: %v", err)
	}
	childDirectory := filepath.Join(filepath.Dir(mainPath), "child-span-session", "subagents")
	if err := os.MkdirAll(childDirectory, 0o755); err != nil {
		t.Fatalf("create subagent directory: %v", err)
	}
	childPath := filepath.Join(childDirectory, "agent-standards.jsonl")
	specPath := filepath.Join(childDirectory, "agent-spec.jsonl")
	childContents := readClaudeFixture(t, "testdata/subagents/agent-tools.jsonl")
	for _, path := range []string{childPath, specPath} {
		if err := os.WriteFile(path, childContents, 0o600); err != nil {
			t.Fatalf("write child transcript fixture: %v", err)
		}
	}

	events := make(chan harness.Event, 8)
	monitor := transcriptMonitor{
		reader: reader, projectRoot: root, sessionID: "child-span-session", events: events,
		subagentSizes: make(map[string]int64), subagentSeenEntries: make(map[string]int),
		subagentIDs: make(map[string]string), finishedSubagents: make(map[string]bool),
	}
	seenEntries, _, err := readTranscript(reader, mainPath, 0, events, harness.CompletionTurnEnd, monitor.linkSubagent)
	if err != nil {
		t.Fatalf("read spawn transcript: %v", err)
	}
	monitor.seenEntries = seenEntries
	spawnEvents := takeHarnessEvents(events)
	if len(spawnEvents) != 6 || spawnEvents[1].Type != harness.EventSubagentStarted || spawnEvents[3].Type != harness.EventSubagentStarted {
		t.Fatalf("main transcript events = %#v, want two spawn events", spawnEvents)
	}

	if _, err := monitor.checkSubagentProgress(); err != nil {
		t.Fatalf("read child tool progress: %v", err)
	}
	toolEvents := takeHarnessEvents(events)
	if len(toolEvents) != 2 {
		t.Fatalf("child tool events = %#v, want one event per child", toolEvents)
	}
	for _, event := range toolEvents {
		if event.Type != harness.EventToolUse || event.SubagentID != "toolu-standards" && event.SubagentID != "toolu-spec" || event.ArgumentGist != "internal/ui/stream.go" {
			t.Fatalf("child tool event = %#v, want linked child ID and readable path gist", event)
		}
	}
	if err := os.WriteFile(filepath.Join(childDirectory, "agent-spec.meta.json"), []byte(`{"finished":true}`), 0o600); err != nil {
		t.Fatalf("finish Spec metadata: %v", err)
	}
	if _, err := monitor.checkSubagentProgress(); err != nil {
		t.Fatalf("read child completion: %v", err)
	}
	finishedEvents := takeHarnessEvents(events)
	if len(finishedEvents) != 1 || finishedEvents[0].Type != harness.EventSubagentFinished || finishedEvents[0].SubagentID != "toolu-spec" {
		t.Fatalf("child finish events = %#v, want Spec finish for the spawn id", finishedEvents)
	}
	if err := os.WriteFile(filepath.Join(childDirectory, "agent-standards.meta.json"), []byte(`{"finished":true}`), 0o600); err != nil {
		t.Fatalf("finish Standards metadata: %v", err)
	}
	if _, err := monitor.checkSubagentProgress(); err != nil {
		t.Fatalf("read Standards completion: %v", err)
	}
	finishedEvents = takeHarnessEvents(events)
	if len(finishedEvents) != 1 || finishedEvents[0].Type != harness.EventSubagentFinished || finishedEvents[0].SubagentID != "toolu-standards" {
		t.Fatalf("second child finish events = %#v, want Standards finish for the spawn id", finishedEvents)
	}
}

func TestTranscriptMonitorLinksForegroundChildFromMetadataBeforeParentResult(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	reader := transcript.New(home)
	mainPath, err := reader.Find(root, "foreground-child-span-session")
	if err != nil {
		t.Fatalf("find main transcript path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(mainPath), 0o755); err != nil {
		t.Fatalf("create main transcript directory: %v", err)
	}
	fixture := readClaudeFixture(t, "testdata/subagents/foreground-role-spawns.jsonl")
	spawnEntry := strings.SplitN(strings.TrimSpace(string(fixture)), "\n", 2)[0] + "\n"
	if err := os.WriteFile(mainPath, []byte(spawnEntry), 0o600); err != nil {
		t.Fatalf("write parent spawn entry: %v", err)
	}
	childDirectory := filepath.Join(filepath.Dir(mainPath), "foreground-child-span-session", "subagents")
	if err := os.MkdirAll(childDirectory, 0o755); err != nil {
		t.Fatalf("create subagent directory: %v", err)
	}
	childPath := filepath.Join(childDirectory, "agent-foreground.jsonl")
	if err := os.WriteFile(childPath, readClaudeFixture(t, "testdata/subagents/agent-tools.jsonl"), 0o600); err != nil {
		t.Fatalf("write child transcript fixture: %v", err)
	}
	metadataPath := filepath.Join(childDirectory, "agent-foreground.meta.json")
	if err := os.WriteFile(metadataPath, []byte(`{"toolUseId":"toolu-foreground","finished":false}`), 0o600); err != nil {
		t.Fatalf("write child metadata: %v", err)
	}

	events := make(chan harness.Event, 4)
	monitor := transcriptMonitor{
		reader: reader, projectRoot: root, sessionID: "foreground-child-span-session", events: events,
		subagentSizes: make(map[string]int64), subagentSeenEntries: make(map[string]int),
		subagentIDs: make(map[string]string), finishedSubagents: make(map[string]bool),
	}
	if _, _, err := readTranscript(reader, mainPath, 0, events, harness.CompletionTurnEnd, monitor.linkSubagent); err != nil {
		t.Fatalf("read parent spawn entry: %v", err)
	}
	_ = takeHarnessEvents(events)
	if _, err := monitor.checkSubagentProgress(); err != nil {
		t.Fatalf("read foreground child progress: %v", err)
	}
	toolEvents := takeHarnessEvents(events)
	if len(toolEvents) != 1 || toolEvents[0].Type != harness.EventToolUse || toolEvents[0].SubagentID != "toolu-foreground" || toolEvents[0].ArgumentGist != "internal/ui/stream.go" {
		t.Fatalf("foreground child events = %#v, want tool gist linked from .meta.json before parent result", toolEvents)
	}

	if err := os.WriteFile(metadataPath, []byte(`{"toolUseId":"toolu-foreground","finished":true}`), 0o600); err != nil {
		t.Fatalf("finish foreground child metadata: %v", err)
	}
	if _, err := monitor.checkSubagentProgress(); err != nil {
		t.Fatalf("read foreground child completion: %v", err)
	}
	finishedEvents := takeHarnessEvents(events)
	if len(finishedEvents) != 1 || finishedEvents[0].Type != harness.EventSubagentFinished || finishedEvents[0].SubagentID != "toolu-foreground" {
		t.Fatalf("foreground finish events = %#v, want finish linked from .meta.json", finishedEvents)
	}
}

func readClaudeFixture(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	return contents
}

func takeHarnessEvents(events <-chan harness.Event) []harness.Event {
	var values []harness.Event
	for {
		select {
		case event := <-events:
			values = append(values, event)
		default:
			return values
		}
	}
}
