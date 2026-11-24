package runrecord

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadActivitiesReadsRunDirectory(t *testing.T) {
	runDirectory := t.TempDir()
	startedAt := time.Date(2026, time.November, 23, 18, 0, 0, 0, time.UTC)
	writer := NewWriter(runDirectory)
	if err := writer.AppendActivityStart(ActivitySpanStart{
		SpanID: "span-001", Activity: Gating, Label: "1/1 — running Gates (attempt 1)", StartedAt: startedAt,
	}); err != nil {
		t.Fatal(err)
	}
	if err := writer.AppendActivityEnd(ActivitySpanEnd{
		SpanID: "span-001", EndedAt: startedAt.Add(time.Second), Outcome: "green",
	}); err != nil {
		t.Fatal(err)
	}

	events, err := ReadActivities(runDirectory, nil)
	if err != nil {
		t.Fatalf("ReadActivities() error = %v", err)
	}
	if len(events) != 2 || events[0].Start == nil || events[1].End == nil || events[0].Start.Activity != Gating || events[1].End.Outcome != "green" {
		t.Fatalf("ReadActivities() = %#v, want green gating span", events)
	}
}

func TestReadActivitiesReportsMissingLog(t *testing.T) {
	_, err := ReadActivities(t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "read Activity log") {
		t.Fatalf("ReadActivities() error = %v, want Activity log read failure", err)
	}
}

func TestActivityLogAppendsStartAndEndEvents(t *testing.T) {
	runDir := t.TempDir()
	writer := NewWriter(runDir)
	startedAt := time.Date(2026, time.November, 23, 18, 0, 0, 0, time.UTC)
	endedAt := startedAt.Add(3*time.Minute + 2*time.Second)
	if err := writer.AppendActivityStart(ActivitySpanStart{
		SpanID: "span-001", ParentSpanID: "", Activity: Implementing,
		Iteration: "1", Label: "1/2 — implementing", StartedAt: startedAt,
	}); err != nil {
		t.Fatalf("AppendActivityStart() error = %v", err)
	}
	contentsDuringRun, err := os.ReadFile(filepath.Join(runDir, "activities.jsonl"))
	if err != nil {
		t.Fatalf("read activity log during Run: %v", err)
	}
	if !strings.Contains(string(contentsDuringRun), `"type":"start"`) {
		t.Fatalf("activity log before Role turn ended = %q, want a start event", contentsDuringRun)
	}
	if err := writer.AppendActivityEnd(ActivitySpanEnd{SpanID: "span-001", EndedAt: endedAt, Outcome: "completed"}); err != nil {
		t.Fatalf("AppendActivityEnd() error = %v", err)
	}

	file, err := os.Open(filepath.Join(runDir, "activities.jsonl"))
	if err != nil {
		t.Fatalf("open activity log: %v", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Errorf("close activity log: %v", err)
		}
	}()
	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read activity log lines: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("activity log has %d lines, want 2: %q", len(lines), lines)
	}
	if !strings.Contains(lines[0], `"parent_span_id":""`) || !strings.Contains(lines[0], `"iteration":"1"`) {
		t.Fatalf("start event = %q, want explicit empty parent and iteration 1", lines[0])
	}
	if !strings.Contains(lines[1], `"outcome":"completed"`) {
		t.Fatalf("end event = %q, want completed outcome", lines[1])
	}
	record, err := NewReader(nil).Read(runDir)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if len(record.Activities) != 2 || record.Activities[0].Start == nil || record.Activities[1].End == nil {
		t.Fatalf("Read() activities = %#v, want one start and one end", record.Activities)
	}
}

func TestReaderAllowsRunWithoutActivityLog(t *testing.T) {
	_, runDir := newRunDirectory(t)
	record, err := NewReader(nil).Read(runDir)
	if err != nil {
		t.Fatalf("Read() error = %v, want legacy Run without activity log to load", err)
	}
	if len(record.Activities) != 0 {
		t.Fatalf("Read() activities = %#v, want none", record.Activities)
	}
}

func TestParseActivitiesRejectsMalformedAndUnknownEvents(t *testing.T) {
	for _, test := range []struct {
		name     string
		contents string
		want     string
	}{
		{name: "malformed JSON", contents: "{\"type\":\n", want: "line 1"},
		{name: "bad timestamp", contents: "{\"type\":\"start\",\"started_at\":\"yesterday\"}\n", want: "line 1"},
		{name: "unknown type", contents: "{\"type\":\"pause\"}\n", want: `unknown event type "pause"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseActivities([]byte(test.contents))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ParseActivities() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestReaderReturnsMalformedActivityLogError(t *testing.T) {
	_, runDir := newRunDirectory(t)
	path := filepath.Join(runDir, activitiesFile)
	if err := os.WriteFile(path, []byte("{\"type\":\"pause\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := NewReader(nil).Read(runDir)
	if err == nil || !strings.Contains(err.Error(), `unknown event type "pause"`) {
		t.Fatalf("Read() error = %v, want an error for the malformed Activity log", err)
	}
}
