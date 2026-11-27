package usage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	claudetranscript "github.com/igorrochap/syl/internal/harness/claude/transcript"
)

func TestCollectClaudeKeepsLastSnapshotIncludesSubagentsAndSplitsWindows(t *testing.T) {
	projectRoot := t.TempDir()
	home := t.TempDir()
	sessionID := "review-session"
	transcriptPath := claudeTranscriptPath(t, home, projectRoot, sessionID)
	projectDir := filepath.Dir(transcriptPath)
	if err := os.MkdirAll(filepath.Join(projectDir, sessionID, "subagents"), 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := "" +
		`{"type":"assistant","timestamp":"2026-08-20T12:00:05Z","message":{"id":"message-1","role":"assistant","usage":{"input_tokens":10,"output_tokens":2,"cache_creation_input_tokens":4,"cache_read_input_tokens":5}}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-08-20T12:00:06Z","message":{"id":"message-1","role":"assistant","usage":{"input_tokens":20,"output_tokens":3,"cache_creation_input_tokens":8,"cache_read_input_tokens":10}}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-08-20T12:00:15Z","message":{"id":"message-2","role":"assistant","usage":{"input_tokens":4,"output_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":2}}}` + "\n"
	if err := os.WriteFile(transcriptPath, []byte(transcript), 0o644); err != nil {
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
	transcriptPath := claudeTranscriptPath(t, home, projectRoot, "malformed")
	projectDir := filepath.Dir(transcriptPath)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcriptPath, []byte("not json\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := CollectClaude(projectRoot, home, []string{"malformed"}, time.Now().Add(-time.Minute), time.Now())
	if !errors.Is(err, errTranscriptParse) {
		t.Fatalf("CollectClaude() error = %v, want transcript parse sentinel", err)
	}
}

func TestCollectClaudeReportsPhysicalLineNumberAfterBlankEntry(t *testing.T) {
	projectRoot := t.TempDir()
	home := t.TempDir()
	transcriptPath := claudeTranscriptPath(t, home, projectRoot, "bad-timestamp")
	projectDir := filepath.Dir(transcriptPath)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := "\n" +
		`{"type":"assistant","timestamp":"invalid","message":{"id":"message-1","role":"assistant","usage":{"input_tokens":10,"output_tokens":2}}}` + "\n"
	if err := os.WriteFile(transcriptPath, []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := CollectClaude(
		projectRoot,
		home,
		[]string{"bad-timestamp"},
		time.Now().Add(-time.Minute),
		time.Now(),
	)
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("CollectClaude() error = %v, want physical transcript line 2", err)
	}
}

func claudeTranscriptPath(t *testing.T, home, projectRoot, sessionID string) string {
	t.Helper()
	path, err := claudetranscript.New(home).Find(projectRoot, sessionID)
	if err != nil {
		t.Fatalf("find Claude transcript path: %v", err)
	}
	return path
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

	got, err := CollectCodex(home, []string{sessionID}, time.Time{}, time.Time{})
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

	got, err := CollectCodex(home, []string{firstSessionID, secondSessionID}, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("CollectCodex() error = %v", err)
	}
	if got.InputTokens != 100 || got.CachedInputTokens != 80 || got.CacheWriteInputTokens != 5 ||
		got.OutputTokens != 10 || got.ReasoningOutputTokens != 4 || got.TotalTokens != 110 {
		t.Fatalf("Codex metrics = %#v, want only the first session rollout", got)
	}
}

func codexTokenCountLine(timestamp string, input, cached, cacheWrite, output, reasoning int64) string {
	return fmt.Sprintf(
		`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"cache_write_input_tokens":%d,"output_tokens":%d,"reasoning_output_tokens":%d,"total_tokens":%d}}}}`,
		timestamp, input, cached, cacheWrite, output, reasoning, input+output,
	) + "\n"
}

func writeCodexRollout(t *testing.T, home, rollout string) {
	const sessionID = "codex-session"
	t.Helper()
	rolloutDir := filepath.Join(home, ".codex", "sessions", "2026", "08", "19")
	if err := os.MkdirAll(rolloutDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rolloutDir, "rollout-20260820T010203-"+sessionID+".jsonl"), []byte(rollout), 0o644); err != nil {
		t.Fatal(err)
	}
}

// sharedCodexRollout simulates two iterations resuming one session: totals
// 100/10 by the end of iteration 1 and 350/40 by the end of iteration 2.
func sharedCodexRollout() string {
	return codexTokenCountLine("2026-08-20T10:01:00.000Z", 60, 50, 1, 5, 2) +
		codexTokenCountLine("2026-08-20T10:02:00.000Z", 100, 80, 2, 10, 4) +
		codexTokenCountLine("2026-08-20T11:01:00.000Z", 250, 200, 3, 30, 9) +
		codexTokenCountLine("2026-08-20T11:02:00.000Z", 350, 300, 5, 40, 12)
}

func codexInvocation(iteration int, start, end string) Invocation {
	return Invocation{
		Iteration: iteration, Role: "implement", Harness: "codex", Model: "gpt-5.6-luna",
		SessionIDs: []string{"codex-session"},
		StartedAt:  mustParseUsageTime(start), EndedAt: mustParseUsageTime(end),
	}
}

func mustParseUsageTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		panic(err)
	}
	return parsed
}

func TestCollectInvocationCountsOnlyCodexUsageInsideTheIterationWindow(t *testing.T) {
	home := t.TempDir()
	writeCodexRollout(t, home, sharedCodexRollout())

	entry := CollectInvocation(codexInvocation(2, "2026-08-20T11:00:00Z", "2026-08-20T11:05:00Z"), t.TempDir(), home)
	want := Metrics{InputTokens: 250, CachedInputTokens: 220, CacheWriteInputTokens: 3, OutputTokens: 30, ReasoningOutputTokens: 8, TotalTokens: 280}
	if !entry.Tracked || entry.Metrics == nil || *entry.Metrics != want {
		t.Fatalf("iteration 2 entry = %#v, want only tokens after iteration 2 starts %#v", entry, want)
	}
}

func TestCollectInvocationCountsFromZeroWhenCodexTotalsReset(t *testing.T) {
	home := t.TempDir()
	writeCodexRollout(t, home, codexTokenCountLine("2026-08-20T10:01:00.000Z", 100, 80, 2, 10, 4)+
		codexTokenCountLine("2026-08-20T11:01:00.000Z", 30, 20, 1, 5, 1))

	entry := CollectInvocation(codexInvocation(2, "2026-08-20T11:00:00Z", "2026-08-20T11:05:00Z"), t.TempDir(), home)
	if entry.Metrics == nil || entry.Metrics.TotalTokens != 35 || entry.Metrics.InputTokens != 30 {
		t.Fatalf("entry after reset = %#v, want the inside total 30/5", entry)
	}
}

func TestCollectInvocationKeepsFreshCodexSessionTotals(t *testing.T) {
	home := t.TempDir()
	writeCodexRollout(t, home,
		codexTokenCountLine("2026-08-20T10:01:00Z", 500, 400, 20, 80, 40)+
			codexTokenCountLine("2026-08-20T10:02:00Z", 1000, 900, 25, 80, 40))

	entry := CollectInvocation(codexInvocation(1, "2026-08-20T10:00:00Z", "2026-08-20T10:05:00Z"), t.TempDir(), home)
	want := Metrics{InputTokens: 1000, CachedInputTokens: 900, CacheWriteInputTokens: 25, OutputTokens: 80, ReasoningOutputTokens: 40, TotalTokens: 1080}
	if !entry.Tracked || entry.Metrics == nil || *entry.Metrics != want {
		t.Fatalf("fresh session entry = %#v, want last cumulative total %#v", entry, want)
	}
}

func TestCollectInvocationEntriesOfASharedCodexSessionSumToTheFinalTotal(t *testing.T) {
	home := t.TempDir()
	writeCodexRollout(t, home, sharedCodexRollout())

	var sum Metrics
	for _, invocation := range []Invocation{
		codexInvocation(1, "2026-08-20T10:00:00Z", "2026-08-20T10:05:00Z"),
		codexInvocation(2, "2026-08-20T11:00:00Z", "2026-08-20T11:05:00Z"),
	} {
		entry := CollectInvocation(invocation, t.TempDir(), home)
		if !entry.Tracked || entry.Metrics == nil {
			t.Fatalf("entry = %#v, want tracked", entry)
		}
		sum = addCodexMetrics(sum, *entry.Metrics)
	}
	final := Metrics{InputTokens: 350, CachedInputTokens: 300, CacheWriteInputTokens: 5, OutputTokens: 40, ReasoningOutputTokens: 12, TotalTokens: 390}
	if sum != final {
		t.Fatalf("sum of entries = %#v, want final total_token_usage %#v", sum, final)
	}
}

func TestCollectInvocationGivesZeroCodexUsageForAWindowWithoutTokenCounts(t *testing.T) {
	home := t.TempDir()
	writeCodexRollout(t, home, sharedCodexRollout())

	entry := CollectInvocation(codexInvocation(3, "2026-08-20T10:30:00Z", "2026-08-20T10:40:00Z"), t.TempDir(), home)
	if !entry.Tracked || entry.Metrics == nil || *entry.Metrics != (Metrics{}) {
		t.Fatalf("empty window entry = %#v, want tracked zero usage", entry)
	}
}

func TestCollectInvocationKeepsNoTokenCountErrorInsideAWindow(t *testing.T) {
	home := t.TempDir()
	writeCodexRollout(t, home, `{"timestamp":"2026-08-20T10:01:00Z","type":"turn.completed"}`+"\n")

	entry := CollectInvocation(codexInvocation(1, "2026-08-20T10:00:00Z", "2026-08-20T10:05:00Z"), t.TempDir(), home)
	if entry.Tracked || entry.Reason != "Codex rollout could not be parsed" {
		t.Fatalf("entry = %#v, want no-token_count parse failure", entry)
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

func TestCollectInvocationRejectsCodexTokenCountsWithoutUsableTimestampsInsideAWindow(t *testing.T) {
	tokenCount := `"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":0,"total_tokens":2}}}}`
	for name, rollout := range map[string]string{
		"missing timestamp":   `{` + tokenCount + "\n",
		"malformed timestamp": `{"timestamp":"yesterday",` + tokenCount + "\n",
		"missing token field": `{"timestamp":"2026-08-20T10:01:00Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1}}}}` + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			writeCodexRollout(t, home, rollout)

			entry := CollectInvocation(codexInvocation(1, "2026-08-20T10:00:00Z", "2026-08-20T10:05:00Z"), t.TempDir(), home)
			if entry.Tracked || entry.Reason != "Codex rollout could not be parsed" {
				t.Fatalf("entry = %#v, want parse failure", entry)
			}
		})
	}
}

func addCodexMetrics(total, metrics Metrics) Metrics {
	total.InputTokens += metrics.InputTokens
	total.CachedInputTokens += metrics.CachedInputTokens
	total.CacheWriteInputTokens += metrics.CacheWriteInputTokens
	total.OutputTokens += metrics.OutputTokens
	total.ReasoningOutputTokens += metrics.ReasoningOutputTokens
	total.TotalTokens += metrics.TotalTokens
	return total
}
