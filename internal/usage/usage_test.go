package usage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCollectClaudeKeepsLastSnapshotIncludesSubagentsAndSplitsWindows(t *testing.T) {
	projectRoot := t.TempDir()
	home := t.TempDir()
	sessionID := "review-session"
	projectDir := filepath.Join(home, ".claude", "projects", projectSlug(projectRoot))
	if err := os.MkdirAll(filepath.Join(projectDir, sessionID, "subagents"), 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := "" +
		`{"type":"assistant","timestamp":"2026-08-20T12:00:05Z","message":{"id":"message-1","role":"assistant","usage":{"input_tokens":10,"output_tokens":2,"cache_creation_input_tokens":4,"cache_read_input_tokens":5}}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-08-20T12:00:06Z","message":{"id":"message-1","role":"assistant","usage":{"input_tokens":20,"output_tokens":3,"cache_creation_input_tokens":8,"cache_read_input_tokens":10}}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-08-20T12:00:15Z","message":{"id":"message-2","role":"assistant","usage":{"input_tokens":4,"output_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":2}}}` + "\n"
	if err := os.WriteFile(filepath.Join(projectDir, sessionID+".jsonl"), []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, sessionID, "subagents", "agent-1.jsonl"), []byte(
		`{"type":"assistant","timestamp":"2026-08-20T12:00:07Z","message":{"id":"sub-message-1","role":"assistant","usage":{"input_tokens":5,"output_tokens":2,"cache_creation_input_tokens":4,"cache_read_input_tokens":1}}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	firstWindow := mustTime(t, "2026-08-20T12:00:00Z")
	firstEnd := mustTime(t, "2026-08-20T12:00:10Z")
	got, err := CollectClaude(projectRoot, home, []string{sessionID}, firstWindow, firstEnd)
	if err != nil {
		t.Fatalf("CollectClaude() error = %v", err)
	}
	if got.InputTokens != 25 || got.OutputTokens != 5 || got.CacheWriteTokens != 12 || got.CacheReadTokens != 11 {
		t.Fatalf("metrics = %#v, want last first-window snapshot plus sub-agent usage", got)
	}
	if got.WeightedEstimate != 46.1 {
		t.Fatalf("weighted estimate = %v, want 46.1", got.WeightedEstimate)
	}

	secondWindow := mustTime(t, "2026-08-20T12:00:10Z")
	secondEnd := mustTime(t, "2026-08-20T12:00:20Z")
	got, err = CollectClaude(projectRoot, home, []string{sessionID}, secondWindow, secondEnd)
	if err != nil {
		t.Fatalf("CollectClaude() second window error = %v", err)
	}
	if got.InputTokens != 4 || got.OutputTokens != 1 || got.CacheWriteTokens != 0 || got.CacheReadTokens != 2 {
		t.Fatalf("second-window metrics = %#v, want only resumed-session usage", got)
	}
}

func TestCollectClaudeReportsMissingTranscript(t *testing.T) {
	_, err := CollectClaude(t.TempDir(), t.TempDir(), []string{"missing"}, time.Now().Add(-time.Minute), time.Now())
	if err == nil {
		t.Fatal("CollectClaude() error = nil, want missing transcript failure")
	}
}

func TestCollectClaudeMarksMalformedTranscript(t *testing.T) {
	projectRoot := t.TempDir()
	home := t.TempDir()
	projectDir := filepath.Join(home, ".claude", "projects", projectSlug(projectRoot))
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "malformed.jsonl"), []byte("not json\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := CollectClaude(projectRoot, home, []string{"malformed"}, time.Now().Add(-time.Minute), time.Now())
	if !errors.Is(err, errTranscriptParse) {
		t.Fatalf("CollectClaude() error = %v, want transcript parse sentinel", err)
	}
}

func TestShortReasonUsesTranscriptParseSentinel(t *testing.T) {
	err := errors.Join(errTranscriptParse, errors.New("transcript format wording changed"))
	if got := shortReason(err); got != "Claude transcript could not be parsed" {
		t.Fatalf("shortReason() = %q, want parse failure reason", got)
	}
}

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
