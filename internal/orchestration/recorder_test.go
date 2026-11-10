package orchestration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	gitadapter "github.com/igorrochap/syl/internal/adapters/git"
	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/runrecord"
	"github.com/igorrochap/syl/internal/sylhome"
	"github.com/igorrochap/syl/internal/tracker"
	"github.com/igorrochap/syl/internal/usage"
	"github.com/igorrochap/syl/internal/verdict"
)

const (
	metadataArtifact            = runrecord.MetadataFile
	implementFeedArtifact       = runrecord.ImplementFeed
	implementTranscriptArtifact = runrecord.ImplementTranscript
	implementHandoffArtifact    = runrecord.ImplementHandoff
	reviewDiffArtifact          = runrecord.ReviewDiff
	reviewFeedArtifact          = runrecord.ReviewFeed
	reviewTranscriptArtifact    = runrecord.ReviewTranscript
	verdictArtifact             = runrecord.VerdictFile
	summaryArtifact             = runrecord.SummaryFile
	sessionsArtifact            = runrecord.SessionsFile
)

type sessionKey struct {
	iteration int
	role      string
	sessionID string
}

func artifactFilename(kind runrecord.ArtifactKind, iteration int) string {
	return runrecord.ArtifactName(kind, iteration)
}

func recordSessions(sessions *[]string, keys map[sessionKey]struct{}, iteration int, role string, sessionIDs []string) {
	for _, recordedID := range sessionIDs {
		sessionID, ok := normalizeSessionID(recordedID)
		if !ok {
			continue
		}
		key := sessionKey{iteration: iteration, role: role, sessionID: sessionID}
		if _, exists := keys[key]; exists {
			continue
		}
		keys[key] = struct{}{}
		*sessions = append(*sessions, fmt.Sprintf("iteration %d %s: %s", iteration, role, sessionID))
	}
}

func writeArtifact(path, contents string) error {
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		return fmt.Errorf("write run artifact %s: %w", path, err)
	}
	return nil
}

type memoryRunRecorder struct {
	dir         string
	files       map[string]string
	states      []runrecord.State
	usage       []usage.Entry
	sessions    []string
	sessionKeys map[sessionKey]struct{}
}

var _ RunRecorder = (*memoryRunRecorder)(nil)

func openDiskRecorder(originRoot string, spec RunSpec) (*diskRunRecorder, error) {
	return openDiskRun(originRoot, sylhome.Dir{}, nil, spec)
}

func newMemoryRunRecorder() *memoryRunRecorder {
	return &memoryRunRecorder{
		dir:         "/memory/run",
		files:       make(map[string]string),
		sessionKeys: make(map[sessionKey]struct{}),
	}
}

func (r *memoryRunRecorder) ImplementHandoffPath(iteration int) string {
	return filepath.Join(r.dir, artifactFilename(implementHandoffArtifact, iteration))
}

func (r *memoryRunRecorder) RecordState(state runrecord.State) {
	r.states = append(r.states, state)
}

func (r *memoryRunRecorder) RecordUsage(entry usage.Entry) error {
	r.usage = append(r.usage, entry)
	return nil
}

func (r *memoryRunRecorder) RecordImplementTurn(iteration int, feed, transcript string) error {
	r.files[artifactFilename(implementFeedArtifact, iteration)] = feed
	r.files[artifactFilename(implementTranscriptArtifact, iteration)] = transcript
	return nil
}

func (r *memoryRunRecorder) RecordFailure(iteration int, role, summary string) error {
	r.files[runrecord.FailureArtifactName(iteration, role)] = summary
	return nil
}

func (r *memoryRunRecorder) RecordReviewDiff(iteration int, diff string) (string, error) {
	name := artifactFilename(reviewDiffArtifact, iteration)
	r.files[name] = diff
	return filepath.Join(r.dir, name), nil
}

func (r *memoryRunRecorder) RecordReviewOutput(iteration int, review ReviewExecution) error {
	r.files[artifactFilename(reviewFeedArtifact, iteration)] = review.Feed
	r.files[artifactFilename(reviewTranscriptArtifact, iteration)] = review.Transcript
	return nil
}

func (r *memoryRunRecorder) RecordVerdict(iteration int, reviewVerdict verdict.Verdict) error {
	r.files[artifactFilename(verdictArtifact, iteration)] = formatVerdict(reviewVerdict)
	return nil
}

func (r *memoryRunRecorder) RecordSessions(iteration int, role string, sessionIDs []string) error {
	recordSessions(&r.sessions, r.sessionKeys, iteration, role, sessionIDs)
	return r.WriteSessions()
}

func (r *memoryRunRecorder) WriteSummary(summary implementSummary) error {
	r.files[artifactFilename(summaryArtifact, 0)] = formatImplementSummary(summary)
	return nil
}

func (r *memoryRunRecorder) WriteSessions() error {
	sessions := append([]string(nil), r.sessions...)
	sort.Strings(sessions)
	r.files[artifactFilename(sessionsArtifact, 0)] = strings.Join(sessions, "\n") + "\n"
	return nil
}

type failingUsageRunRecorder struct {
	*memoryRunRecorder
	usageCalls int
}

func (r *failingUsageRunRecorder) RecordUsage(usage.Entry) error {
	r.usageCalls++
	return errors.New("usage artifact is unavailable")
}

type failingSessionsRecorder struct {
	*memoryRunRecorder
	role string
	err  error
}

func (r *failingSessionsRecorder) RecordSessions(iteration int, role string, sessionIDs []string) error {
	recordSessions(&r.sessions, r.sessionKeys, iteration, role, sessionIDs)
	if role == r.role {
		return r.err
	}
	return r.WriteSessions()
}

type failingRoleArtifactRecorder struct {
	*memoryRunRecorder
	failAt string
	err    error
}

func (r *failingRoleArtifactRecorder) RecordImplementTurn(iteration int, feed, transcript string) error {
	if r.failAt == "implement-turn" {
		return r.err
	}
	return r.memoryRunRecorder.RecordImplementTurn(iteration, feed, transcript)
}

func (r *failingRoleArtifactRecorder) RecordReviewOutput(iteration int, review ReviewExecution) error {
	if r.failAt == "review-output" {
		return r.err
	}
	return r.memoryRunRecorder.RecordReviewOutput(iteration, review)
}

func (r *failingRoleArtifactRecorder) RecordFailure(iteration int, role, summary string) error {
	if r.failAt == "failure" {
		return r.err
	}
	return r.memoryRunRecorder.RecordFailure(iteration, role, summary)
}

func (r *failingRoleArtifactRecorder) RecordSessions(iteration int, role string, sessionIDs []string) error {
	if r.failAt == "sessions" {
		return r.err
	}
	return r.memoryRunRecorder.RecordSessions(iteration, role, sessionIDs)
}

func TestFailedRoleTurnReturnsArtifactWriteErrors(t *testing.T) {
	failure := errors.New("artifact write failed")
	tests := []struct {
		name   string
		failAt string
		record func(RunRecorder) error
	}{
		{
			name:   "implement feed and transcript",
			failAt: "implement-turn",
			record: func(recorder RunRecorder) error {
				return recordFailedImplementTurn(recorder, 1, implementExecution{}, failure)
			},
		},
		{
			name:   "implement failure summary",
			failAt: "failure",
			record: func(recorder RunRecorder) error {
				return recordFailedImplementTurn(recorder, 1, implementExecution{}, failure)
			},
		},
		{
			name:   "implement sessions",
			failAt: "sessions",
			record: func(recorder RunRecorder) error {
				return recordFailedImplementTurn(recorder, 1, implementExecution{}, failure)
			},
		},
		{
			name:   "review feed and transcript",
			failAt: "review-output",
			record: func(recorder RunRecorder) error {
				return recordFailedReviewTurn(recorder, 1, ReviewExecution{}, failure)
			},
		},
		{
			name:   "review failure summary",
			failAt: "failure",
			record: func(recorder RunRecorder) error {
				return recordFailedReviewTurn(recorder, 1, ReviewExecution{}, failure)
			},
		},
		{
			name:   "review sessions",
			failAt: "sessions",
			record: func(recorder RunRecorder) error {
				return recordFailedReviewTurn(recorder, 1, ReviewExecution{}, failure)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := &failingRoleArtifactRecorder{
				memoryRunRecorder: newMemoryRunRecorder(),
				failAt:            test.failAt,
				err:               failure,
			}
			if err := test.record(recorder); !errors.Is(err, failure) {
				t.Fatalf("record failed Role turn error = %v, want %v", err, failure)
			}
		})
	}
}

func TestRunImplementIterationsRecordsReviseThenApproveInMemory(t *testing.T) {
	recorder := newMemoryRunRecorder()
	recorder.dir = t.TempDir()
	implementer := &scriptedConversationAdapter{
		runs: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "implement-1"},
			{Type: harness.EventAssistantText, Text: "first implementation"},
		}},
		resumes: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "implement-2"},
			{Type: harness.EventAssistantText, Text: "revised implementation"},
		}},
	}
	reviewer := &scriptedConversationAdapter{
		runs: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "review-1"},
			{Type: harness.EventAssistantText, Text: "VERDICT: revise\nSUMMARY: Fix required\nFINDINGS:\n- [blocking] worker.go:10 — handle the error\n"},
		}},
		resumes: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "review-1"},
			{Type: harness.EventAssistantText, Text: "VERDICT: approve\nSUMMARY: Ready\nFINDINGS:\n"},
		}},
	}

	iterations, final, _, err := runImplementIterations(context.Background(), implementIterationsParams{
		git:           staticImplementGit{branchPoint: "branch-point", diff: "diff --git a/a b/a\n"},
		implementer:   implementer,
		reviewer:      reviewer,
		projectConfig: config.Config{Loop: config.LoopConfig{MaxIterations: 2}},
		ticket:        tracker.Ticket{Number: 42, Title: "Deepen recorder", Body: "Keep artifacts stable."},
		branchPoint:   "branch-point",
		recorder:      recorder,
		output:        io.Discard,
	})
	if err != nil {
		t.Fatalf("runImplementIterations() error = %v", err)
	}
	if iterations != 2 || final.Status != verdict.Approve {
		t.Fatalf("result = (%d, %q), want (2, approve)", iterations, final.Status)
	}
	for _, name := range []string{
		artifactFilename(implementTranscriptArtifact, 1),
		artifactFilename(reviewDiffArtifact, 1),
		artifactFilename(verdictArtifact, 1),
		artifactFilename(implementTranscriptArtifact, 2),
		artifactFilename(reviewDiffArtifact, 2),
		artifactFilename(verdictArtifact, 2),
	} {
		if _, ok := recorder.files[name]; !ok {
			t.Errorf("recorded files missing %q: %v", name, recorder.files)
		}
	}
	if len(reviewer.resumeCalls) != 1 {
		t.Fatalf("review resume calls = %d, want 1", len(reviewer.resumeCalls))
	}
	if implementer.runCalls != 1 {
		t.Fatalf("implement run calls = %d, want iteration 1 to be the only fresh run", implementer.runCalls)
	}
	if len(implementer.resumeCalls) != 1 {
		t.Fatalf("implement resume calls = %d, want 1", len(implementer.resumeCalls))
	}
	if got := implementer.resumeCalls[0].sessionID; got != "implement-1" {
		t.Fatalf("implement resume session = %q, want %q", got, "implement-1")
	}
	if got := implementer.resumeCalls[0].prompt; !strings.Contains(got, "worker.go:10") || !strings.Contains(got, "handle the error") {
		t.Fatalf("implement resume prompt = %q, want iteration blocking finding", got)
	}
	wantSessions := "iteration 1 implement: implement-1\n" +
		"iteration 1 review: review-1\n" +
		"iteration 2 implement: implement-1\n" +
		"iteration 2 implement: implement-2\n" +
		"iteration 2 review: review-1\n"
	if got := recorder.files[artifactFilename(sessionsArtifact, 0)]; got != wantSessions {
		t.Fatalf("recorded sessions = %q, want %q", got, wantSessions)
	}
}

func TestRunImplementIterationsUsesWorktreeHandoffToStartFreshSeededSession(t *testing.T) {
	recorder := newMemoryRunRecorder()
	recorder.dir = t.TempDir()
	worktreeRoot := t.TempDir()
	handoffPath := filepath.Join(
		worktreeRoot,
		".syl",
		"runs",
		filepath.Base(recorder.dir),
		"handoff-01.md",
	)
	ticket := tracker.Ticket{Number: 42, Title: "Preserve context", Body: "Keep the session decisions."}
	implementer := &scriptedConversationAdapter{
		runs: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement-1"}},
			{{Type: harness.EventSession, SessionID: "implement-2"}},
		},
		runHooks: []func(harness.Request){func(harness.Request) {
			if err := os.WriteFile(handoffPath, []byte("decisions and explored files"), 0o644); err != nil {
				t.Fatalf("write handoff artifact: %v", err)
			}
		}},
	}
	reviewer := &scriptedConversationAdapter{
		runs: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "review-1"},
			{Type: harness.EventAssistantText, Text: "VERDICT: revise\nSUMMARY: Fix required\nFINDINGS:\n- [blocking] worker.go:10 — handle the error\n"},
		}},
		resumes: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "review-1"},
			{Type: harness.EventAssistantText, Text: "VERDICT: approve\nSUMMARY: Ready\nFINDINGS:\n"},
		}},
	}

	iterations, final, _, err := runImplementIterations(context.Background(), implementIterationsParams{
		git:                  staticImplementGit{branchPoint: "branch-point", diff: "diff --git a/a b/a\n"},
		implementer:          implementer,
		reviewer:             reviewer,
		projectConfig:        config.Config{Loop: config.LoopConfig{MaxIterations: 2}},
		ticket:               ticket,
		branchPoint:          "branch-point",
		worktreeArtifactRoot: worktreeRoot,
		recorder:             recorder,
		output:               io.Discard,
	})
	if err != nil {
		t.Fatalf("runImplementIterations() error = %v", err)
	}
	if iterations != 2 || final.Status != verdict.Approve {
		t.Fatalf("result = (%d, %q), want (2, approve)", iterations, final.Status)
	}
	if implementer.runCalls != 2 || len(implementer.resumeCalls) != 0 {
		t.Fatalf("implement calls = (%d runs, %d resumes), want (2, 0)", implementer.runCalls, len(implementer.resumeCalls))
	}
	seededPrompt := implementer.runRequests[1].Prompt
	for _, expected := range []string{handoffPath, ticket.Title, ticket.Body, "worker.go:10", "handle the error"} {
		if !strings.Contains(seededPrompt, expected) {
			t.Errorf("seeded prompt = %q, want %q", seededPrompt, expected)
		}
	}
	contents, err := os.ReadFile(handoffPath)
	if err != nil {
		t.Fatalf("read handoff artifact: %v", err)
	}
	if string(contents) != "decisions and explored files" {
		t.Fatalf("handoff artifact = %q", contents)
	}
}

func TestRunImplementIterationsRollsOverAfterRecoveredImplementTurn(t *testing.T) {
	recorder := newMemoryRunRecorder()
	recorder.dir = t.TempDir()
	handoffPath := recorder.ImplementHandoffPath(1)
	ticket := tracker.Ticket{Number: 248, Title: "Recover implementation", Body: "Preserve the recovered decisions."}
	implementer := &scriptedConversationAdapter{
		runs: [][]harness.Event{
			{
				{Type: harness.EventSession, SessionID: "implement-stalled"},
				{Type: harness.EventAssistantText, Text: "Partial implementation."},
			},
			{
				{Type: harness.EventSession, SessionID: "implement-after-rollover"},
				{Type: harness.EventAssistantText, Text: "Implementation continues."},
			},
		},
		resumes: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "implement-stalled"},
			{Type: harness.EventAssistantText, Text: "Recovered implementation."},
		}},
		runWaitErrors: []error{&harness.SessionStalledError{
			SessionID:    "implement-stalled",
			Limit:        harness.StallWindowLimit,
			StateSummary: "finished implementation subagent report is queued",
		}},
		resumeHooks: []func(harness.Request){func(harness.Request) {
			if err := os.WriteFile(handoffPath, []byte("recovered decisions and explored files"), 0o644); err != nil {
				t.Fatalf("write recovered handoff: %v", err)
			}
		}},
	}
	reviewer := &scriptedConversationAdapter{
		runs: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "review-session"},
			{Type: harness.EventAssistantText, Text: "VERDICT: revise\nSUMMARY: Complete one finding\nFINDINGS:\n- [blocking] worker.go:10 — finish recovery handling\n"},
		}},
		resumes: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "review-session"},
			{Type: harness.EventAssistantText, Text: "VERDICT: approve\nSUMMARY: Ready\nFINDINGS:\n"},
		}},
	}

	iterations, final, _, err := runImplementIterations(context.Background(), implementIterationsParams{
		git:           staticImplementGit{branchPoint: "branch-point", diff: "diff --git a/worker.go b/worker.go\n"},
		implementer:   implementer,
		reviewer:      reviewer,
		projectConfig: config.Config{Loop: config.LoopConfig{MaxIterations: 2}},
		ticket:        ticket,
		branchPoint:   "branch-point",
		recorder:      recorder,
		output:        io.Discard,
	})
	if err != nil {
		t.Fatalf("runImplementIterations() error = %v", err)
	}
	if iterations != 2 || final.Status != verdict.Approve {
		t.Fatalf("result = (%d, %q), want two iterations ending in approve", iterations, final.Status)
	}
	if implementer.runCalls != 2 || len(implementer.resumeCalls) != 1 ||
		implementer.resumeCalls[0].sessionID != "implement-stalled" {
		t.Fatalf("implementer calls = (%d runs, %#v resumes), want initial run, one same-session recovery, and fresh rollover run", implementer.runCalls, implementer.resumeCalls)
	}
	seededPrompt := implementer.runRequests[1].Prompt
	for _, want := range []string{
		handoffPath,
		ticket.Title,
		ticket.Body,
		"worker.go:10",
		"finish recovery handling",
	} {
		if !strings.Contains(seededPrompt, want) {
			t.Errorf("rollover prompt = %q, want %q", seededPrompt, want)
		}
	}
	wantSessions := "iteration 1 implement: implement-stalled\n" +
		"iteration 1 review: review-session\n" +
		"iteration 2 implement: implement-after-rollover\n" +
		"iteration 2 review: review-session\n"
	if got := recorder.files[artifactFilename(sessionsArtifact, 0)]; got != wantSessions {
		t.Fatalf("recorded sessions = %q, want %q", got, wantSessions)
	}
	if got := recorder.files[artifactFilename(implementTranscriptArtifact, 1)]; !strings.Contains(got, "Partial implementation.") || !strings.Contains(got, "Recovered implementation.") {
		t.Fatalf("iteration 1 implement transcript = %q, want stalled and recovered turns", got)
	}
}

func TestRunImplementIterationsAllowsRolloverOnFinalIteration(t *testing.T) {
	recorder := newMemoryRunRecorder()
	recorder.dir = t.TempDir()
	handoffPath := filepath.Join(recorder.dir, "handoff-01.md")
	implementer := &scriptedConversationAdapter{
		runs: [][]harness.Event{{{Type: harness.EventSession, SessionID: "implement-1"}}},
		runHooks: []func(harness.Request){func(harness.Request) {
			if err := os.WriteFile(handoffPath, []byte("final handoff"), 0o644); err != nil {
				t.Fatalf("write final handoff: %v", err)
			}
		}},
	}
	reviewer := &scriptedConversationAdapter{runs: [][]harness.Event{{
		{Type: harness.EventSession, SessionID: "review-1"},
		{Type: harness.EventAssistantText, Text: "VERDICT: approve\nSUMMARY: Ready\nFINDINGS:\n"},
	}}}

	iterations, final, _, err := runImplementIterations(context.Background(), implementIterationsParams{
		git:           staticImplementGit{branchPoint: "branch-point", diff: "diff --git a/a b/a\n"},
		implementer:   implementer,
		reviewer:      reviewer,
		projectConfig: config.Config{Loop: config.LoopConfig{MaxIterations: 1}},
		ticket:        tracker.Ticket{Number: 42},
		branchPoint:   "branch-point",
		recorder:      recorder,
		output:        io.Discard,
	})
	if err != nil {
		t.Fatalf("runImplementIterations() error = %v", err)
	}
	if iterations != 1 || final.Status != verdict.Approve {
		t.Fatalf("result = (%d, %q), want (1, approve)", iterations, final.Status)
	}
	if _, err := os.Stat(handoffPath); err != nil {
		t.Fatalf("stat final handoff artifact: %v", err)
	}
}

func TestRunImplementIterationsFallsBackToFreshRunWhenImplementResumeFails(t *testing.T) {
	recorder := newMemoryRunRecorder()
	ticket := tracker.Ticket{Number: 42, Title: "Resume implementation", Body: "Keep context between iterations."}
	implementer := &scriptedConversationAdapter{runs: [][]harness.Event{
		{
			{Type: harness.EventSession, SessionID: "implement-1"},
			{Type: harness.EventAssistantText, Text: "first implementation"},
		},
		{
			{Type: harness.EventSession, SessionID: "implement-fallback"},
			{Type: harness.EventAssistantText, Text: "fresh revised implementation"},
		},
	}}
	reviewer := &scriptedConversationAdapter{
		runs: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "review-1"},
			{Type: harness.EventAssistantText, Text: "VERDICT: revise\nSUMMARY: Fix required\nFINDINGS:\n- [blocking] worker.go:10 — handle the error\n"},
		}},
		resumes: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "review-1"},
			{Type: harness.EventAssistantText, Text: "VERDICT: approve\nSUMMARY: Ready\nFINDINGS:\n"},
		}},
	}

	iterations, final, _, err := runImplementIterations(context.Background(), implementIterationsParams{
		git:           staticImplementGit{branchPoint: "branch-point", diff: "diff --git a/a b/a\n"},
		implementer:   implementer,
		reviewer:      reviewer,
		projectConfig: config.Config{Loop: config.LoopConfig{MaxIterations: 2}},
		ticket:        ticket,
		branchPoint:   "branch-point",
		recorder:      recorder,
		output:        io.Discard,
	})
	if err != nil {
		t.Fatalf("runImplementIterations() error = %v", err)
	}
	if iterations != 2 || final.Status != verdict.Approve {
		t.Fatalf("result = (%d, %q), want (2, approve)", iterations, final.Status)
	}
	if len(implementer.resumeCalls) != 1 || implementer.resumeCalls[0].sessionID != "implement-1" {
		t.Fatalf("implement resume calls = %#v, want one attempt for implement-1", implementer.resumeCalls)
	}
	if implementer.runCalls != 2 {
		t.Fatalf("implement run calls = %d, want initial run and fallback run", implementer.runCalls)
	}
	wantPrompt := composeImplementPrompt(ticket, []verdict.Finding{{
		Kind: verdict.Blocking, Location: "worker.go:10", Issue: "handle the error",
	}}, 2, recorder.ImplementHandoffPath(2), "", "")
	if got := implementer.resumeCalls[0].prompt; got != wantPrompt {
		t.Fatalf("implement resume prompt = %q, want %q", got, wantPrompt)
	}
	if got := implementer.runRequests[1].Prompt; got != wantPrompt {
		t.Fatalf("implement fallback prompt = %q, want %q", got, wantPrompt)
	}
}

func TestRunImplementIterationsStopsFailedResumeBeforeFreshRun(t *testing.T) {
	implementer := &contextCapturingAdapter{scriptedConversationAdapter: &scriptedConversationAdapter{
		runs: [][]harness.Event{
			{
				{Type: harness.EventSession, SessionID: "implement-1"},
				{Type: harness.EventAssistantText, Text: "first implementation"},
			},
			{
				{Type: harness.EventSession, SessionID: "implement-fallback"},
				{Type: harness.EventAssistantText, Text: "fresh revised implementation"},
			},
		},
		resumes: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "implement-1"},
			{Type: harness.EventResult, Text: "resume failed mid-stream", IsError: true},
		}},
	}}
	var resumeStoppedBeforeFallback bool
	implementer.runHooks = []func(harness.Request){nil, func(harness.Request) {
		resumeStoppedBeforeFallback = len(implementer.resumeContexts) == 1 && implementer.resumeContexts[0].Err() != nil
	}}
	reviewer := &scriptedConversationAdapter{
		runs: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "review-1"},
			{Type: harness.EventAssistantText, Text: "VERDICT: revise\nSUMMARY: Fix required\nFINDINGS:\n- [blocking] worker.go:10 — handle the error\n"},
		}},
		resumes: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "review-1"},
			{Type: harness.EventAssistantText, Text: "VERDICT: approve\nSUMMARY: Ready\nFINDINGS:\n"},
		}},
	}

	_, final, _, err := runImplementIterations(context.Background(), implementIterationsParams{
		git:           staticImplementGit{branchPoint: "branch-point", diff: "diff --git a/a b/a\n"},
		implementer:   implementer,
		reviewer:      reviewer,
		projectConfig: config.Config{Loop: config.LoopConfig{MaxIterations: 2}},
		ticket:        tracker.Ticket{Number: 42, Title: "Resume implementation"},
		branchPoint:   "branch-point",
		recorder:      newMemoryRunRecorder(),
		output:        io.Discard,
	})
	if err != nil {
		t.Fatalf("runImplementIterations() error = %v", err)
	}
	if final.Status != verdict.Approve || implementer.runCalls != 2 {
		t.Fatalf("result = (%q, %d run calls), want approve after the fallback run", final.Status, implementer.runCalls)
	}
	if !resumeStoppedBeforeFallback {
		t.Fatal("failed resume harness was still running when the fallback run started")
	}
}

// contextCapturingAdapter records the context each resumed harness runs under,
// so tests can observe whether syl stopped it.
type contextCapturingAdapter struct {
	*scriptedConversationAdapter
	resumeContexts []context.Context
}

func (a *contextCapturingAdapter) Resume(ctx context.Context, sessionID string, request harness.Request) (harness.Stream, error) {
	a.resumeContexts = append(a.resumeContexts, ctx)
	return a.scriptedConversationAdapter.Resume(ctx, sessionID, request)
}

func TestRunImplementIterationsStartsVerdictOnANewLineAfterReviewProse(t *testing.T) {
	recorder := newMemoryRunRecorder()
	implementer := &scriptedConversationAdapter{runs: [][]harness.Event{{
		{Type: harness.EventSession, SessionID: "implement-1"},
		{Type: harness.EventAssistantText, Text: "implementation"},
	}}}
	reviewer := &scriptedConversationAdapter{
		runs: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "review-1"},
			{Type: harness.EventAssistantText, Text: "Review prose"},
		}},
		resumes: [][]harness.Event{{
			{Type: harness.EventAssistantText, Text: "VERDICT: approve\nSUMMARY: Ready\nFINDINGS:"},
		}},
	}
	var output strings.Builder

	_, _, _, err := runImplementIterations(context.Background(), implementIterationsParams{
		git:           staticImplementGit{branchPoint: "branch-point", diff: "diff --git a/a b/a\n"},
		implementer:   implementer,
		reviewer:      reviewer,
		projectConfig: config.Config{Loop: config.LoopConfig{MaxIterations: 1}},
		ticket:        tracker.Ticket{Number: 42, Title: "Separate verdict"},
		branchPoint:   "branch-point",
		recorder:      recorder,
		output:        &output,
	})
	if err != nil {
		t.Fatalf("runImplementIterations() error = %v", err)
	}

	verdictIndex := strings.Index(output.String(), "VERDICT:")
	if verdictIndex == 0 || output.String()[verdictIndex-1] != '\n' {
		t.Fatalf("output = %q, want VERDICT at the start of a line", output.String())
	}
}

func TestRunImplementIterationsReportsReviewSeparatorWriteError(t *testing.T) {
	recorder := newMemoryRunRecorder()
	implementer := &scriptedConversationAdapter{runs: [][]harness.Event{{
		{Type: harness.EventAssistantText, Text: "implementation"},
	}}}
	reviewer := &scriptedConversationAdapter{
		runs: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "review-1"},
			{Type: harness.EventAssistantText, Text: "Review prose"},
		}},
		resumes: [][]harness.Event{{
			{Type: harness.EventAssistantText, Text: "VERDICT: approve\nSUMMARY: Ready\nFINDINGS:"},
		}},
	}

	_, _, _, err := runImplementIterations(context.Background(), implementIterationsParams{
		git:           staticImplementGit{branchPoint: "branch-point", diff: "diff --git a/a b/a\n"},
		implementer:   implementer,
		reviewer:      reviewer,
		projectConfig: config.Config{Loop: config.LoopConfig{MaxIterations: 1}},
		ticket:        tracker.Ticket{Number: 42, Title: "Separate verdict"},
		branchPoint:   "branch-point",
		recorder:      recorder,
		output:        &separatorFailingWriter{failOnVerdict: true},
	})
	if err == nil || !strings.Contains(err.Error(), "write review verdict") {
		t.Fatalf("runImplementIterations() error = %v, want rendered verdict write failure", err)
	}
}

func TestFormatImplementSummaryPreservesDiffStatIndentation(t *testing.T) {
	got := formatImplementSummary(implementSummary{
		final:    verdict.Verdict{Status: verdict.Approve, Summary: "Ready"},
		diffStat: " change.go | 1 +\n other.go  | 2 ++\n",
	})

	want := "Diff stat:\n change.go | 1 +\n other.go  | 2 ++\n"
	if !strings.Contains(got, want) {
		t.Fatalf("formatImplementSummary() = %q, want diff stat rows with leading spaces", got)
	}
}

func TestRunImplementIterationsContinuesWhenUsageRecordingFails(t *testing.T) {
	recorder := &failingUsageRunRecorder{memoryRunRecorder: newMemoryRunRecorder()}
	implementer := &scriptedConversationAdapter{runs: [][]harness.Event{{
		{Type: harness.EventSession, SessionID: "implement-1"},
		{Type: harness.EventAssistantText, Text: "implementation"},
	}}}
	reviewer := &scriptedConversationAdapter{runs: [][]harness.Event{{
		{Type: harness.EventSession, SessionID: "review-1"},
		{Type: harness.EventAssistantText, Text: "VERDICT: approve\nSUMMARY: Ready\nFINDINGS:\n"},
	}}}

	iterations, final, _, err := runImplementIterations(context.Background(), implementIterationsParams{
		git:           staticImplementGit{branchPoint: "branch-point", diff: "diff --git a/a b/a\n"},
		implementer:   implementer,
		reviewer:      reviewer,
		projectConfig: config.Config{Loop: config.LoopConfig{MaxIterations: 1}},
		ticket:        tracker.Ticket{Number: 42, Title: "Record usage", Body: "Keep the run alive."},
		branchPoint:   "branch-point",
		recorder:      recorder,
		output:        io.Discard,
	})
	if err != nil {
		t.Fatalf("runImplementIterations() error = %v", err)
	}
	if iterations != 1 || final.Status != verdict.Approve {
		t.Fatalf("result = (%d, %q), want (1, approve)", iterations, final.Status)
	}
	if recorder.usageCalls != 2 {
		t.Fatalf("usage recording calls = %d, want implement and review", recorder.usageCalls)
	}
}

func TestRunImplementIterationsReportsImplementSessionWriteError(t *testing.T) {
	sessionWriteError := errors.New("sessions artifact is unavailable")
	recorder := &failingSessionsRecorder{
		memoryRunRecorder: newMemoryRunRecorder(),
		role:              "implement",
		err:               sessionWriteError,
	}
	implementer := &scriptedConversationAdapter{runs: [][]harness.Event{{
		{Type: harness.EventSession, SessionID: "implement-1"},
		{Type: harness.EventAssistantText, Text: "implementation"},
	}}}
	reviewer := &scriptedConversationAdapter{runs: [][]harness.Event{{
		{Type: harness.EventSession, SessionID: "review-1"},
		{Type: harness.EventAssistantText, Text: "VERDICT: approve\nSUMMARY: Ready\nFINDINGS:\n"},
	}}}

	_, _, _, err := runImplementIterations(context.Background(), implementIterationsParams{
		git:           staticImplementGit{branchPoint: "branch-point", diff: "diff --git a/a b/a\n"},
		implementer:   implementer,
		reviewer:      reviewer,
		projectConfig: config.Config{Loop: config.LoopConfig{MaxIterations: 1}},
		ticket:        tracker.Ticket{Number: 42, Title: "Record implement sessions"},
		branchPoint:   "branch-point",
		recorder:      recorder,
		output:        io.Discard,
	})
	if err == nil || !strings.Contains(err.Error(), "record implement sessions") || !errors.Is(err, sessionWriteError) {
		t.Fatalf("runImplementIterations() error = %v, want implement session write failure", err)
	}
}

func TestRunImplementIterationsReportsReviewSessionWriteError(t *testing.T) {
	sessionWriteError := errors.New("sessions artifact is unavailable")
	recorder := &failingSessionsRecorder{
		memoryRunRecorder: newMemoryRunRecorder(),
		role:              "review",
		err:               sessionWriteError,
	}
	implementer := &scriptedConversationAdapter{runs: [][]harness.Event{{
		{Type: harness.EventSession, SessionID: "implement-1"},
		{Type: harness.EventAssistantText, Text: "implementation"},
	}}}
	reviewer := &scriptedConversationAdapter{runs: [][]harness.Event{{
		{Type: harness.EventSession, SessionID: "review-1"},
		{Type: harness.EventAssistantText, Text: "VERDICT: approve\nSUMMARY: Ready\nFINDINGS:\n"},
	}}}

	_, _, _, err := runImplementIterations(context.Background(), implementIterationsParams{
		git:           staticImplementGit{branchPoint: "branch-point", diff: "diff --git a/a b/a\n"},
		implementer:   implementer,
		reviewer:      reviewer,
		projectConfig: config.Config{Loop: config.LoopConfig{MaxIterations: 1}},
		ticket:        tracker.Ticket{Number: 42, Title: "Record review sessions"},
		branchPoint:   "branch-point",
		recorder:      recorder,
		output:        io.Discard,
	})
	if err == nil || !strings.Contains(err.Error(), "record review sessions") || !errors.Is(err, sessionWriteError) {
		t.Fatalf("runImplementIterations() error = %v, want review session write failure", err)
	}
}

func TestRunImplementPassesContextToImplementer(t *testing.T) {
	const additionalContext = "Use the existing GitRunner seam.\nDo not add a new adapter."

	git := &implementRunGit{}
	implementer := &capturingImplementAdapter{response: "Implemented the ticket."}
	reviewer := &capturingImplementAdapter{response: conversationTestVerdict}
	var output strings.Builder
	err := RunImplement(context.Background(), ImplementOptions{
		OriginRoot: t.TempDir(),
		WorkRoot:   t.TempDir(),
		OpenRun:    func(RunSpec) (RunRecorder, error) { return newMemoryRunRecorder(), nil },
		ProjectConfig: config.Config{
			Roles: config.RolesConfig{
				Implement: config.RoleConfig{Harness: config.HarnessCodex},
				Review:    config.RoleConfig{Harness: config.HarnessClaude},
			},
			Loop: config.LoopConfig{MaxIterations: 1},
		},
		IssueTracker: branchSetupTracker{},
		Ticket:       tracker.Ticket{Number: 42, Title: "Implement context"},
		Implementer:  implementer,
		Reviewer:     reviewer,
		Git:          git,
		OriginGit:    git,
		Input:        strings.NewReader(""),
		Output:       &output,
		Context:      additionalContext,
	})
	if err != nil {
		t.Fatalf("RunImplement() error = %v", err)
	}
	if !strings.Contains(implementer.request.Prompt, additionalContext) {
		t.Fatalf("implementer prompt = %q, want context %q", implementer.request.Prompt, additionalContext)
	}
	if strings.Contains(reviewer.request.Prompt, additionalContext) {
		t.Fatalf("reviewer prompt = %q, want no implementer context %q", reviewer.request.Prompt, additionalContext)
	}
}

func TestRunImplementPublishesApprovedDiffAndSkipsEmptyDiff(t *testing.T) {
	for _, test := range []struct {
		name      string
		changed   bool
		wantCalls []string
	}{
		{
			name:    "non-empty diff",
			changed: true,
			wantCalls: []string{
				"add -A", "diff --cached --name-only branch-point", "commit -m feat: Add resilient workflow (#42)",
				"push -u origin feat/add-resilient-workflow",
			},
		},
		{
			name:      "empty diff",
			wantCalls: []string{"add -A", "diff --cached --name-only branch-point"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			git := &publishingTestGit{branchPoint: "branch-point", changed: test.changed}
			publisher := &recordingPullRequestTracker{}
			notifier := &recordingImplementNotifier{}
			recorder := newMemoryRunRecorder()
			var spec RunSpec
			var output strings.Builder
			err := RunImplement(context.Background(), ImplementOptions{
				OriginRoot: t.TempDir(), WorkRoot: t.TempDir(),
				OpenRun: func(value RunSpec) (RunRecorder, error) {
					spec = value
					return recorder, nil
				},
				ProjectConfig: config.Config{
					Tracker: config.TrackerConfig{Issues: config.TrackerGitHub},
					Roles: config.RolesConfig{
						Implement: config.RoleConfig{Harness: config.HarnessCodex},
						Review:    config.RoleConfig{Harness: config.HarnessClaude},
					},
					Loop:          config.LoopConfig{MaxIterations: 1},
					Notifications: config.NotificationsConfig{Enabled: true},
				},
				IssueTracker: publisher,
				Ticket:       tracker.Ticket{Number: 42, Title: "Add resilient workflow"},
				Implementer:  &capturingImplementAdapter{response: "Implemented the ticket."},
				Reviewer: &capturingImplementAdapter{response: "VERDICT: approve\nSUMMARY: Reviewed implementation.\n" +
					"FINDINGS:\n- [nit] internal/example.go:9 — Rename this helper.\n"},
				Git: git, OriginGit: git, Notifier: notifier, Input: strings.NewReader(""),
				Output: &output, PublishOnApprove: true,
			})
			if err != nil {
				t.Fatalf("RunImplement() error = %v", err)
			}
			if !spec.PublishOnApprove {
				t.Fatal("RunSpec.PublishOnApprove = false, want true")
			}
			if test.changed {
				publication := runrecord.ParseSummary([]byte(recorder.files[artifactFilename(summaryArtifact, 0)])).Publication
				if publication.Status != runrecord.PublicationPublished || publication.URL != "https://github.com/example/project/pull/23" {
					t.Fatalf("recorded publication = %#v, want published with pull request URL", publication)
				}
				if publisher.request == nil || publisher.request.Head != "feat/add-resilient-workflow" || publisher.request.Base != "main" {
					t.Fatalf("pull request = %#v, want branch head and start branch base", publisher.request)
				}
				if publisher.request.Body != "Closes #42\n\nReviewed implementation.\n\n- [ ] internal/example.go:9 — Rename this helper." {
					t.Fatalf("pull request body = %q, want summary and nit task", publisher.request.Body)
				}
				if !strings.Contains(output.String(), "Pull request: https://github.com/example/project/pull/23") {
					t.Fatalf("summary output = %q, want pull request URL", output.String())
				}
				if !strings.Contains(recorder.files[artifactFilename(summaryArtifact, 0)], "Pull request: https://github.com/example/project/pull/23") {
					t.Fatalf("summary artifact = %q, want pull request URL", recorder.files[artifactFilename(summaryArtifact, 0)])
				}
				if len(notifier.messages) != 1 || !strings.Contains(notifier.messages[0], "https://github.com/example/project/pull/23") {
					t.Fatalf("notifications = %v, want pull request URL", notifier.messages)
				}
			} else {
				publication := runrecord.ParseSummary([]byte(recorder.files[artifactFilename(summaryArtifact, 0)])).Publication
				if publication.Status != runrecord.PublicationSkipped {
					t.Fatalf("recorded publication = %#v, want skipped", publication)
				}
				if publisher.request != nil {
					t.Fatalf("pull request = %#v, want none for an empty diff", publisher.request)
				}
				if !strings.Contains(output.String(), "No changes to publish") {
					t.Fatalf("summary output = %q, want empty-diff notice", output.String())
				}
			}
			for _, expected := range test.wantCalls {
				if !slices.Contains(git.calls, expected) {
					t.Errorf("git calls = %v, want %q", git.calls, expected)
				}
			}
			if !test.changed && (slices.Contains(git.calls, "commit -m feat: Add resilient workflow (#42)") || slices.Contains(git.calls, "push -u origin feat/add-resilient-workflow")) {
				t.Fatalf("git calls = %v, want no commit or push for an empty diff", git.calls)
			}
		})
	}
}

func TestRunImplementRecordsPublicationNotRequested(t *testing.T) {
	git := &publishingTestGit{branchPoint: "branch-point", changed: true}
	recorder := newMemoryRunRecorder()
	options := approvedPublicationOptions(t, git, &recordingPullRequestTracker{}, recorder, io.Discard)
	options.PublishOnApprove = false
	if err := RunImplement(context.Background(), options); err != nil {
		t.Fatalf("RunImplement() error = %v", err)
	}
	publication := runrecord.ParseSummary([]byte(recorder.files[artifactFilename(summaryArtifact, 0)])).Publication
	if publication.Status != runrecord.PublicationNotRequested {
		t.Fatalf("recorded publication = %#v, want not requested", publication)
	}
	if slices.Contains(git.calls, "add -A") || slices.Contains(git.calls, "push -u origin feat/add-resilient-workflow") {
		t.Fatalf("git calls = %v, want no publication steps", git.calls)
	}
}

func TestRunImplementTreatsExistingPullRequestAsPublished(t *testing.T) {
	const existingURL = "https://github.com/example/project/pull/23"
	ghRunner := &existingPullRequestGitHubRunner{output: `[{"url":"` + existingURL + `"}]`}
	githubTracker, err := tracker.NewGitHub(ghRunner)
	if err != nil {
		t.Fatalf("NewGitHub() error = %v", err)
	}
	git := &publishingTestGit{branchPoint: "branch-point", changed: true}
	recorder := newMemoryRunRecorder()
	var output strings.Builder
	options := approvedPublicationOptions(t, git, &existingPullRequestTracker{creator: githubTracker}, recorder, &output)
	if err := RunImplement(context.Background(), options); err != nil {
		t.Fatalf("RunImplement() error = %v, want existing pull request to count as success", err)
	}
	if !strings.Contains(output.String(), existingURL) {
		t.Fatalf("implement output = %q, want existing pull request URL", output.String())
	}
	publication := runrecord.ParseSummary([]byte(recorder.files[artifactFilename(summaryArtifact, 0)])).Publication
	if publication.Status != runrecord.PublicationPublished || publication.URL != existingURL {
		t.Fatalf("recorded publication = %#v, want published with existing URL", publication)
	}
	if len(ghRunner.calls) != 1 || ghRunner.calls[0] != "pr list --head feat/add-resilient-workflow --state open --json url --limit 1" {
		t.Fatalf("gh calls = %v, want existing pull request lookup only", ghRunner.calls)
	}
}

func TestRunImplementReportsCommitFailureAndRemainingCommands(t *testing.T) {
	git := &publishingTestGit{
		branchPoint: "branch-point", changed: true,
		failCommand: "commit -m feat: Add resilient workflow (#42)", failure: errors.New("hook rejected commit"),
	}
	recorder := newMemoryRunRecorder()
	var output strings.Builder
	err := RunImplement(context.Background(), approvedPublicationOptions(t, git, &recordingPullRequestTracker{}, recorder, &output))
	if err == nil || !strings.Contains(err.Error(), "publication failed during commit:") || !strings.Contains(err.Error(), "hook rejected commit") {
		t.Fatalf("RunImplement() error = %v, want named commit failure", err)
	}
	for _, command := range []string{
		"git add -A", "git commit -m 'feat: Add resilient workflow (#42)'",
		"git push -u origin feat/add-resilient-workflow", "gh pr create --draft",
	} {
		if !strings.Contains(output.String(), command) {
			t.Errorf("publication output = %q, want command %q", output.String(), command)
		}
	}
	assertPublicationFailureRun(t, recorder, runrecord.PublicationStepCommit)
}

func TestRunImplementReportsPullRequestFailureCommandOnly(t *testing.T) {
	git := &publishingTestGit{branchPoint: "branch-point", changed: true}
	trackerFailure := &failingPullRequestTracker{err: errors.New("GitHub is unavailable")}
	recorder := newMemoryRunRecorder()
	var output strings.Builder
	err := RunImplement(context.Background(), approvedPublicationOptions(t, git, trackerFailure, recorder, &output))
	if err == nil || !strings.Contains(err.Error(), "publication failed during pull request:") || !strings.Contains(err.Error(), "GitHub is unavailable") {
		t.Fatalf("RunImplement() error = %v, want named pull request failure", err)
	}
	for _, command := range []string{"gh pr create --draft", "--head feat/add-resilient-workflow", "--base main"} {
		if !strings.Contains(output.String(), command) {
			t.Errorf("publication output = %q, want pull request command fragment %q", output.String(), command)
		}
	}
	if strings.Contains(output.String(), "git push -u origin") || strings.Contains(output.String(), "git commit -m") {
		t.Fatalf("publication output = %q, want only the failed pull request command", output.String())
	}
	assertPublicationFailureRun(t, recorder, runrecord.PublicationStepPullRequest)
}

func TestRunImplementPushFailureKeepsApprovedCommitAndPrintsRemainingCommands(t *testing.T) {
	root, branchPoint, git := newPublicationTestRepository(t)
	implementation := &writingImplementAdapter{
		capturingImplementAdapter: &capturingImplementAdapter{response: "Implemented the ticket."},
		root:                      root,
	}
	var output strings.Builder
	err := RunImplement(context.Background(), ImplementOptions{
		OriginRoot: root, WorkRoot: root, OpenRun: NewDiskRunOpener(root, sylhome.Dir{}, io.Discard),
		ProjectConfig: config.Config{
			Tracker: config.TrackerConfig{Issues: config.TrackerGitHub},
			Roles: config.RolesConfig{
				Implement: config.RoleConfig{Harness: config.HarnessCodex},
				Review:    config.RoleConfig{Harness: config.HarnessClaude},
			},
			Loop: config.LoopConfig{MaxIterations: 1},
		},
		IssueTracker: &recordingPullRequestTracker{},
		Ticket:       tracker.Ticket{Number: 42, Title: "Add resilient workflow"},
		Implementer:  implementation,
		Reviewer:     &capturingImplementAdapter{response: "VERDICT: approve\nSUMMARY: Reviewed implementation.\nFINDINGS:\n"},
		Git:          git, OriginGit: git, Input: strings.NewReader(""), Output: &output, PublishOnApprove: true,
	})
	if err == nil || !strings.Contains(err.Error(), "publication failed during push:") {
		t.Fatalf("RunImplement() error = %v, want push publication failure", err)
	}
	for _, want := range []string{"git push -u origin feat/add-resilient-workflow", "gh pr create --draft"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("publication output = %q, want %q", output.String(), want)
		}
	}
	if strings.Contains(output.String(), "git commit -m") {
		t.Fatalf("publication output = %q, want only push and pull request commands", output.String())
	}

	commits := gitOutput(t, git, "rev-list", "--count", branchPoint+"..HEAD")
	if commits != "1" {
		t.Fatalf("commits after branch point = %q, want 1; push failure must keep the commit", commits)
	}
	runDirs, err := runrecord.NewReader(nil).Directories(root)
	if err != nil || len(runDirs) != 1 {
		t.Fatalf("Run directories = %v, error = %v, want one recorded Run", runDirs, err)
	}
	state, err := runrecord.Read(runrecord.Path(runDirs[0]))
	if err != nil || state.Status != runrecord.Approved {
		t.Fatalf("Run state = %#v, error = %v, want approved after push failure", state, err)
	}
	summary, err := runrecord.NewReader(nil).ReadSummary(runDirs[0])
	if err != nil || summary.Publication.Status != runrecord.PublicationFailed ||
		summary.Publication.Step != runrecord.PublicationStepPush || !strings.Contains(summary.Publication.Error, "push implementation branch") {
		t.Fatalf("recorded publication = %#v, error = %v, want push failure details", summary.Publication, err)
	}
}

func newPublicationTestRepository(t *testing.T) (string, string, gitadapter.ExecGitRunner) {
	t.Helper()
	root := t.TempDir()
	git := gitadapter.ExecGitRunner{Dir: root}
	for _, args := range [][]string{
		{"init"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Syl Test"},
	} {
		if _, err := git.Run(context.Background(), args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "base.txt"}, {"commit", "-m", "base"}, {"branch", "-M", "main"}} {
		if _, err := git.Run(context.Background(), args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	branchPoint := strings.TrimSpace(gitOutput(t, git, "rev-parse", "HEAD"))
	return root, branchPoint, git
}

func gitOutput(t *testing.T, git GitRunner, args ...string) string {
	t.Helper()
	output, err := git.Run(context.Background(), args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(output)
}

type writingImplementAdapter struct {
	*capturingImplementAdapter
	root string
}

func (adapter *writingImplementAdapter) Run(ctx context.Context, request harness.Request) (harness.Stream, error) {
	if err := os.WriteFile(filepath.Join(adapter.root, "change.txt"), []byte("implementation\n"), 0o600); err != nil {
		return nil, err
	}
	return adapter.capturingImplementAdapter.Run(ctx, request)
}

func approvedPublicationOptions(
	t *testing.T,
	git *publishingTestGit,
	issueTracker tracker.Tracker,
	recorder *memoryRunRecorder,
	output io.Writer,
) ImplementOptions {
	t.Helper()
	return ImplementOptions{
		OriginRoot: t.TempDir(), WorkRoot: t.TempDir(),
		OpenRun: func(RunSpec) (RunRecorder, error) { return recorder, nil },
		ProjectConfig: config.Config{
			Tracker: config.TrackerConfig{Issues: config.TrackerGitHub},
			Roles: config.RolesConfig{
				Implement: config.RoleConfig{Harness: config.HarnessCodex},
				Review:    config.RoleConfig{Harness: config.HarnessClaude},
			},
			Loop: config.LoopConfig{MaxIterations: 1},
		},
		IssueTracker: issueTracker, Ticket: tracker.Ticket{Number: 42, Title: "Add resilient workflow"},
		Implementer: &capturingImplementAdapter{response: "Implemented the ticket."},
		Reviewer:    &capturingImplementAdapter{response: "VERDICT: approve\nSUMMARY: Reviewed implementation.\nFINDINGS:\n"},
		Git:         git, OriginGit: git, Input: strings.NewReader(""), Output: output, PublishOnApprove: true,
	}
}

func assertPublicationFailureRun(t *testing.T, recorder *memoryRunRecorder, step runrecord.PublicationStep) {
	t.Helper()
	if len(recorder.states) == 0 || recorder.states[len(recorder.states)-1].Status != runrecord.Approved {
		t.Fatalf("final Run state = %#v, want approved after publication failure", recorder.states)
	}
	publication := runrecord.ParseSummary([]byte(recorder.files[artifactFilename(summaryArtifact, 0)])).Publication
	if publication.Status != runrecord.PublicationFailed || publication.Step != step || publication.Error == "" {
		t.Fatalf("recorded publication = %#v, want failed at %q with an error", publication, step)
	}
}

type publishingTestGit struct {
	branchPoint string
	changed     bool
	calls       []string
	failCommand string
	failure     error
}

func (g *publishingTestGit) Run(_ context.Context, args ...string) (string, error) {
	command := strings.Join(args, " ")
	g.calls = append(g.calls, command)
	if command == g.failCommand {
		return "", g.failure
	}
	switch command {
	case "status --porcelain --untracked-files=all":
		return "", nil
	case "rev-parse HEAD":
		return g.branchPoint, nil
	case "symbolic-ref --quiet --short HEAD":
		return "main\n", nil
	case "diff " + g.branchPoint:
		if g.changed {
			return "diff --git a/change.go b/change.go\n+implemented\n", nil
		}
		return "", nil
	case "ls-files --others --exclude-standard -z":
		return "", nil
	case "diff --stat " + g.branchPoint:
		return " change.go | 1 +\n", nil
	case "add -A", "diff --cached --name-only " + g.branchPoint,
		"commit -m feat: Add resilient workflow (#42)", "push -u origin feat/add-resilient-workflow":
		if strings.HasPrefix(command, "diff --cached") && g.changed {
			return "change.go\n", nil
		}
		return "", nil
	default:
		if strings.HasPrefix(command, "switch -c ") {
			return "", nil
		}
		return "", fmt.Errorf("unexpected git command %q", command)
	}
}

type recordingPullRequestTracker struct {
	branchSetupTracker
	request *tracker.PullRequest
}

func (tracker *recordingPullRequestTracker) CreatePullRequest(_ context.Context, request tracker.PullRequest) (string, error) {
	tracker.request = &request
	return "https://github.com/example/project/pull/23", nil
}

type failingPullRequestTracker struct {
	branchSetupTracker
	err error
}

func (publisher *failingPullRequestTracker) CreatePullRequest(context.Context, tracker.PullRequest) (string, error) {
	return "", publisher.err
}

type existingPullRequestTracker struct {
	branchSetupTracker
	creator tracker.PullRequestCreator
}

func (publisher *existingPullRequestTracker) CreatePullRequest(ctx context.Context, request tracker.PullRequest) (string, error) {
	return publisher.creator.CreatePullRequest(ctx, request)
}

type existingPullRequestGitHubRunner struct {
	calls  []string
	output string
}

func (runner *existingPullRequestGitHubRunner) Run(_ context.Context, args ...string) (string, error) {
	command := strings.Join(args, " ")
	runner.calls = append(runner.calls, command)
	if command == "pr list --head feat/add-resilient-workflow --state open --json url --limit 1" {
		return runner.output, nil
	}
	return "", fmt.Errorf("unexpected gh command %q", command)
}

type recordingImplementNotifier struct {
	messages []string
}

func (notifier *recordingImplementNotifier) Notify(_ context.Context, message string) error {
	notifier.messages = append(notifier.messages, message)
	return nil
}

func TestImplementRunRecorderRecordsRoleContexts(t *testing.T) {
	originRoot := t.TempDir()
	workRoot := t.TempDir()
	implementContext := "Keep the existing recorder.\nPreserve metadata ordering."
	reviewContext := "Check the metadata output."
	recorder, err := openDiskRecorder(originRoot, RunSpec{
		Kind:               runrecord.Implement,
		TicketRef:          "#42",
		Branch:             "feat/record-role-context",
		BranchPoint:        "abc123",
		WorkRoot:           workRoot,
		ImplementerHarness: "codex",
		ReviewerHarness:    "claude",
		ImplementContext:   implementContext,
		ReviewContext:      reviewContext,
	})
	if err != nil {
		t.Fatalf("newImplementRunRecorder() error = %v", err)
	}

	metadata := readRunMetadataArtifact(t, recorder.dir)
	want := "Branch: feat/record-role-context\n" +
		"Branch point: abc123\n" +
		"Work root: " + workRoot + "\n" +
		"Implementer harness: codex\n" +
		"Reviewer harness: claude\n" +
		"Implementer context:\n" +
		"  Keep the existing recorder.\n" +
		"  Preserve metadata ordering.\n" +
		"Reviewer context:\n" +
		"  Check the metadata output.\n"
	if metadata != want {
		t.Fatalf("metadata = %q, want %q", metadata, want)
	}
}

func TestImplementRunRecorderPreservesMetadataWithoutContexts(t *testing.T) {
	workRoot := t.TempDir()
	recorder, err := openDiskRecorder(t.TempDir(), RunSpec{
		Kind:               runrecord.Implement,
		TicketRef:          "#42",
		Branch:             "feat/record-role-context",
		BranchPoint:        "abc123",
		WorkRoot:           workRoot,
		ImplementerHarness: "codex",
		ReviewerHarness:    "claude",
	})
	if err != nil {
		t.Fatalf("newImplementRunRecorder() error = %v", err)
	}

	want := "Branch: feat/record-role-context\n" +
		"Branch point: abc123\n" +
		"Work root: " + workRoot + "\n" +
		"Implementer harness: codex\n" +
		"Reviewer harness: claude\n"
	if metadata := readRunMetadataArtifact(t, recorder.dir); metadata != want {
		t.Fatalf("metadata = %q, want %q", metadata, want)
	}
}

func TestReviewRunRecorderRecordsReviewerContext(t *testing.T) {
	originRoot := t.TempDir()
	workRoot := t.TempDir()
	reviewContext := "Review only the parser.\nDo not modify files."
	recorder, err := openDiskRecorder(originRoot, RunSpec{
		Kind:            runrecord.Review,
		TicketRef:       "#42",
		MaxIterations:   1,
		BranchPoint:     "abc123",
		WorkRoot:        workRoot,
		ReviewerHarness: "claude",
		ReviewContext:   reviewContext,
	})
	if err != nil {
		t.Fatalf("newReviewRunRecorder() error = %v", err)
	}

	metadata := readRunMetadataArtifact(t, recorder.dir)
	want := "Ticket: #42\n" +
		"Branch point: abc123\n" +
		"Work root: " + workRoot + "\n" +
		"Reviewer harness: claude\n" +
		"Reviewer context:\n" +
		"  Review only the parser.\n" +
		"  Do not modify files.\n"
	if metadata != want {
		t.Fatalf("metadata = %q, want %q", metadata, want)
	}
}

func TestRunRecorderKeepsMetadataKeysOutsideMultilineContext(t *testing.T) {
	implementContext := "first line\nBranch: something\nthird line"
	recorder, err := openDiskRecorder(t.TempDir(), RunSpec{
		Kind:               runrecord.Implement,
		TicketRef:          "#42",
		Branch:             "feat/record-role-context",
		BranchPoint:        "abc123",
		WorkRoot:           t.TempDir(),
		ImplementerHarness: "codex",
		ReviewerHarness:    "claude",
		ImplementContext:   implementContext,
	})
	if err != nil {
		t.Fatalf("newImplementRunRecorder() error = %v", err)
	}

	metadata := readRunMetadataArtifact(t, recorder.dir)
	for _, expected := range []string{
		"Implementer context:\n",
		"  first line\n",
		"  Branch: something\n",
		"  third line\n",
	} {
		if !strings.Contains(metadata, expected) {
			t.Errorf("metadata = %q, want %q", metadata, expected)
		}
	}
	if got := countUnindentedMetadataKey(metadata, "Branch"); got != 1 {
		t.Errorf("unindented Branch keys = %d, want 1", got)
	}
}

func TestDiskRunRecorderFlushesSessionsAfterEachRole(t *testing.T) {
	runDir := t.TempDir()
	recorder := &diskRunRecorder{
		dir:    runDir,
		writer: runrecord.NewWriter(runDir),
	}

	if err := recorder.RecordSessions(1, "implement", []string{"implement-1"}); err != nil {
		t.Fatalf("record iteration 1 implement sessions: %v", err)
	}
	assertSessionsArtifact(t, recorder.dir, "iteration 1 implement: implement-1\n")

	if err := recorder.RecordSessions(1, "review", []string{"review-1"}); err != nil {
		t.Fatalf("record iteration 1 review sessions: %v", err)
	}
	assertSessionsArtifact(t, recorder.dir,
		"iteration 1 implement: implement-1\n"+
			"iteration 1 review: review-1\n",
	)

	if err := recorder.RecordSessions(2, "implement", []string{"implement-2"}); err != nil {
		t.Fatalf("record iteration 2 implement sessions: %v", err)
	}
	assertSessionsArtifact(t, recorder.dir,
		"iteration 1 implement: implement-1\n"+
			"iteration 1 review: review-1\n"+
			"iteration 2 implement: implement-2\n",
	)
}

func assertSessionsArtifact(t *testing.T, runDir, want string) {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(runDir, artifactFilename(sessionsArtifact, 0)))
	if err != nil {
		t.Fatalf("read sessions artifact: %v", err)
	}
	if string(contents) != want {
		t.Fatalf("sessions artifact = %q, want %q", contents, want)
	}
}

func readRunMetadataArtifact(t *testing.T, runDir string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(runDir, artifactFilename(metadataArtifact, 0)))
	if err != nil {
		t.Fatalf("read metadata artifact: %v", err)
	}
	return string(contents)
}

func countUnindentedMetadataKey(metadata, wantedKey string) int {
	count := 0
	for _, line := range strings.Split(metadata, "\n") {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		key, _, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(key) == wantedKey {
			count++
		}
	}
	return count
}

func TestDiskRunRecorderDeduplicatesSessionIDs(t *testing.T) {
	runDir := t.TempDir()
	recorder := &diskRunRecorder{
		dir:    runDir,
		writer: runrecord.NewWriter(runDir),
	}
	if err := recorder.RecordSessions(2, "review", []string{"review-session", "review-session"}); err != nil {
		t.Fatalf("record iteration 2 review sessions: %v", err)
	}
	if err := recorder.RecordSessions(1, "implement", []string{"review-session"}); err != nil {
		t.Fatalf("record iteration 1 implement sessions: %v", err)
	}
	if err := recorder.RecordSessions(1, "review", []string{"review-session", "review-session-2"}); err != nil {
		t.Fatalf("record iteration 1 review sessions: %v", err)
	}
	if err := recorder.RecordSessions(2, "review", []string{"review-session", "review-session-2", ""}); err != nil {
		t.Fatalf("record iteration 2 review sessions: %v", err)
	}

	recorded, err := runrecord.NewReader(nil).ReadSessions(recorder.dir)
	if err != nil {
		t.Fatalf("read recorded sessions: %v", err)
	}
	if got, want := len(recorded), 5; got != want {
		t.Fatalf("recorded sessions = %d, want %d: %v", got, want, recorded)
	}
	if err := recorder.WriteSessions(); err != nil {
		t.Fatalf("WriteSessions() error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(recorder.dir, artifactFilename(sessionsArtifact, 0)))
	if err != nil {
		t.Fatalf("read sessions artifact: %v", err)
	}
	want := "iteration 1 implement: review-session\n" +
		"iteration 1 review: review-session\n" +
		"iteration 1 review: review-session-2\n" +
		"iteration 2 review: review-session\n" +
		"iteration 2 review: review-session-2\n"
	if string(got) != want {
		t.Fatalf("sessions artifact = %q, want %q", got, want)
	}
}

func TestDiskRunRecorderReportsSessionWriteFailure(t *testing.T) {
	runDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(runDir, artifactFilename(sessionsArtifact, 0)), 0o755); err != nil {
		t.Fatalf("create sessions artifact directory: %v", err)
	}
	recorder := &diskRunRecorder{
		dir:    runDir,
		writer: runrecord.NewWriter(runDir),
	}

	err := recorder.RecordSessions(1, "review", []string{"review-session"})
	if err == nil || !strings.Contains(err.Error(), "write run artifact") {
		t.Fatalf("RecordSessions() error = %v, want sessions artifact write failure", err)
	}
}

func TestDiskRunRecorderFailOpenRecordsFailedStateAndRemovesMarker(t *testing.T) {
	projectRoot := t.TempDir()
	runDir := filepath.Join(projectRoot, ".syl", "runs", "run-203")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("create run directory: %v", err)
	}
	metadataPath := filepath.Join(runDir, artifactFilename(metadataArtifact, 0))
	if err := os.Mkdir(metadataPath, 0o755); err != nil {
		t.Fatalf("create metadata artifact directory: %v", err)
	}
	metadataErr := writeArtifact(metadataPath, "metadata")
	if metadataErr == nil {
		t.Fatal("writeArtifact() succeeded for metadata directory")
	}

	spec := RunSpec{
		Kind:          runrecord.Implement,
		TicketRef:     "#203",
		MaxIterations: 1,
	}
	sylHome := testSylHome(t, t.TempDir())
	marker, err := sylHome.MarkLive(sylhome.LiveRun{
		ProjectPath: projectRoot,
		RunDir:      runDir,
		TicketRef:   spec.TicketRef,
	})
	if err != nil {
		t.Fatalf("MarkLive() error = %v", err)
	}
	recorder := &diskRunRecorder{
		dir:      runDir,
		writer:   runrecord.NewWriter(runDir),
		runState: newRunState(spec),
		marker:   &marker,
	}

	if got := recorder.failOpen(metadataErr); !errors.Is(got, metadataErr) {
		t.Fatalf("failOpen() error = %v, want %v", got, metadataErr)
	}
	state, err := runrecord.Read(runrecord.Path(runDir))
	if err != nil {
		t.Fatalf("read failed run state: %v", err)
	}
	if state.Status != runrecord.Failed || state.Activity != "" || state.Question != "" || state.EndedAt == nil {
		t.Fatalf("failed run state = %+v, want failed with no activity/question and an end time", state)
	}
	runs, err := sylHome.LiveRuns()
	if err != nil {
		t.Fatalf("LiveRuns() error = %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("live runs after failOpen() = %d, want 0", len(runs))
	}
}

type staticImplementGit struct {
	branchPoint string
	diff        string
}

type implementRunGit struct {
	branchSetupGit
}

func (g *implementRunGit) Run(ctx context.Context, args ...string) (string, error) {
	switch strings.Join(args, " ") {
	case "diff abc123":
		return "diff --git a/change.txt b/change.txt\n+implemented\n", nil
	case "diff --stat abc123":
		return " change.txt | 1 +\n", nil
	default:
		return g.branchSetupGit.Run(ctx, args...)
	}
}

type capturingImplementAdapter struct {
	request  harness.Request
	response string
}

func (a *capturingImplementAdapter) Run(_ context.Context, request harness.Request) (harness.Stream, error) {
	a.request = request
	return scriptedConversationStream{events: []harness.Event{
		{Type: harness.EventSession, SessionID: "implement-session"},
		{Type: harness.EventAssistantText, Text: a.response},
	}}, nil
}

func (*capturingImplementAdapter) Resume(context.Context, string, harness.Request) (harness.Stream, error) {
	return nil, errors.New("unexpected harness resume")
}

func (*capturingImplementAdapter) Attach(context.Context, harness.Request) error {
	return errors.New("unexpected harness attach")
}

func (*capturingImplementAdapter) AttachSession(context.Context, string, harness.Request) error {
	return errors.New("unexpected harness session attach")
}

func (g staticImplementGit) Run(_ context.Context, args ...string) (string, error) {
	switch strings.Join(args, " ") {
	case "rev-parse HEAD":
		return g.branchPoint, nil
	case "diff " + g.branchPoint:
		return g.diff, nil
	case "ls-files --others --exclude-standard -z":
		return "", nil
	default:
		return "", fmt.Errorf("unexpected git command %q", strings.Join(args, " "))
	}
}
