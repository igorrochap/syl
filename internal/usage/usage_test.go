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

func TestCollectCodexReadsLastCumulativeTokenCountFromAnyDateDirectory(t *testing.T) {
	home := t.TempDir()
	sessionID := "codex-session"
	rolloutDir := filepath.Join(home, ".codex", "sessions", "2026", "08", "19")
	if err := os.MkdirAll(rolloutDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rollout := "" +
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":90,"cache_write_input_tokens":5,"output_tokens":10,"reasoning_output_tokens":2,"total_tokens":110}}}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":2000000,"cached_input_tokens":1920000,"cache_write_input_tokens":10,"output_tokens":24200,"reasoning_output_tokens":13700,"total_tokens":2024200}}}}` + "\n" +
		`{"type":"turn.completed"}` + "\n"
	if err := os.WriteFile(filepath.Join(rolloutDir, "rollout-20260820T010203-"+sessionID+".jsonl"), []byte(rollout), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := CollectCodex(home, []string{sessionID})
	if err != nil {
		t.Fatalf("CollectCodex() error = %v", err)
	}
	if got.InputTokens != 2_000_000 || got.CachedInputTokens != 1_920_000 || got.CacheWriteInputTokens != 10 ||
		got.OutputTokens != 24_200 || got.ReasoningOutputTokens != 13_700 || got.TotalTokens != 2_024_200 {
		t.Fatalf("Codex metrics = %#v, want last cumulative token_count event", got)
	}
}

func TestCollectCodexUsesOneRolloutPerInvocation(t *testing.T) {
	home := t.TempDir()
	firstSessionID := "first-session"
	secondSessionID := "second-session"
	firstRolloutDir := filepath.Join(home, ".codex", "sessions", "2026", "08", "19")
	secondRolloutDir := filepath.Join(home, ".codex", "sessions", "2026", "08", "20")
	for _, dir := range []string{firstRolloutDir, secondRolloutDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	firstRollout := `{"payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":80,"cache_write_input_tokens":5,"output_tokens":10,"reasoning_output_tokens":4,"total_tokens":110}}}}` + "\n"
	secondRollout := `{"payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":200,"cached_input_tokens":160,"cache_write_input_tokens":7,"output_tokens":20,"reasoning_output_tokens":8,"total_tokens":220}}}}` + "\n"
	if err := os.WriteFile(filepath.Join(firstRolloutDir, "rollout-20260820T010203-"+firstSessionID+".jsonl"), []byte(firstRollout), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secondRolloutDir, "rollout-20260820T010204-"+secondSessionID+".jsonl"), []byte(secondRollout), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := CollectCodex(home, []string{firstSessionID, secondSessionID})
	if err != nil {
		t.Fatalf("CollectCodex() error = %v", err)
	}
	if got.InputTokens != 100 || got.CachedInputTokens != 80 || got.CacheWriteInputTokens != 5 ||
		got.OutputTokens != 10 || got.ReasoningOutputTokens != 4 || got.TotalTokens != 110 {
		t.Fatalf("Codex metrics = %#v, want only the first session rollout", got)
	}
}

func TestCollectInvocationTracksCodexUsageWithoutClaudeWindowRules(t *testing.T) {
	home := t.TempDir()
	sessionID := "codex-session"
	rolloutDir := filepath.Join(home, ".codex", "sessions", "2026", "08", "19")
	if err := os.MkdirAll(rolloutDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rollout := `{"payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1000,"cached_input_tokens":900,"cache_write_input_tokens":25,"output_tokens":80,"reasoning_output_tokens":40,"total_tokens":1080}}}}` + "\n"
	if err := os.WriteFile(filepath.Join(rolloutDir, "rollout-20260820T010203-"+sessionID+".jsonl"), []byte(rollout), 0o644); err != nil {
		t.Fatal(err)
	}

	entry := CollectInvocation(Invocation{
		Iteration:  2,
		Role:       "implement",
		Harness:    "codex",
		Model:      "gpt-5.6-luna",
		SessionIDs: []string{sessionID},
		StartedAt:  time.Now().Add(24 * time.Hour),
		EndedAt:    time.Now().Add(48 * time.Hour),
	}, t.TempDir(), home)
	if !entry.Tracked || entry.Metrics == nil || entry.Reason != "" {
		t.Fatalf("Codex entry = %#v, want tracked metrics", entry)
	}
	if entry.Metrics.WeightedEstimate != 0 || entry.Metrics.InputTokens != 1000 || entry.Metrics.CachedInputTokens != 900 ||
		entry.Metrics.ReasoningOutputTokens != 40 {
		t.Fatalf("Codex entry metrics = %#v, want raw usage without weighted estimate", *entry.Metrics)
	}
}

func TestCollectInvocationDegradesCodexUsageFailures(t *testing.T) {
	tests := []struct {
		name       string
		rollout    string
		wantReason string
	}{
		{name: "missing rollout", wantReason: "Codex rollout is missing"},
		{name: "malformed rollout", rollout: "not json\n", wantReason: "Codex rollout could not be parsed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			if tt.rollout != "" {
				rolloutDir := filepath.Join(home, ".codex", "sessions", "2026", "08", "19")
				if err := os.MkdirAll(rolloutDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(rolloutDir, "rollout-20260820T010203-codex-session.jsonl"), []byte(tt.rollout), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			entry := CollectInvocation(Invocation{
				Iteration:  1,
				Role:       "implement",
				Harness:    "codex",
				SessionIDs: []string{"codex-session"},
			}, t.TempDir(), home)
			if entry.Tracked || entry.Metrics != nil || entry.Reason != tt.wantReason {
				t.Fatalf("Codex entry = %#v, want untracked reason %q", entry, tt.wantReason)
			}
		})
	}
}

func TestCollectInvocationDegradesCodexUsageWhenRolloutMatchesMultipleFiles(t *testing.T) {
	home := t.TempDir()
	sessionID := "codex-session"
	for _, day := range []string{"19", "20"} {
		rolloutDir := filepath.Join(home, ".codex", "sessions", "2026", "08", day)
		if err := os.MkdirAll(rolloutDir, 0o755); err != nil {
			t.Fatal(err)
		}
		rollout := `{"payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":80,"cache_write_input_tokens":5,"output_tokens":10,"reasoning_output_tokens":4,"total_tokens":110}}}}` + "\n"
		path := filepath.Join(rolloutDir, "rollout-20260820T010203-"+sessionID+".jsonl")
		if err := os.WriteFile(path, []byte(rollout), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	entry := CollectInvocation(Invocation{
		Iteration:  1,
		Role:       "implement",
		Harness:    "codex",
		SessionIDs: []string{sessionID},
	}, t.TempDir(), home)
	if entry.Tracked || entry.Metrics != nil || entry.Reason != "Codex rollout has multiple matching files" {
		t.Fatalf("Codex entry = %#v, want untracked multiple-rollout reason", entry)
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
