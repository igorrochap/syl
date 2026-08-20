package orchestration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/tracker"
	"github.com/igorrochap/syl/internal/usage"
	"github.com/igorrochap/syl/internal/verdict"
)

type memoryRunRecorder struct {
	dir         string
	files       map[string]string
	sessions    []string
	sessionKeys map[sessionKey]struct{}
}

var _ RunRecorder = (*memoryRunRecorder)(nil)

func newMemoryRunRecorder() *memoryRunRecorder {
	return &memoryRunRecorder{
		dir:         "/memory/run",
		files:       make(map[string]string),
		sessionKeys: make(map[sessionKey]struct{}),
	}
}

func (r *memoryRunRecorder) Dir() string {
	return r.dir
}

func (r *memoryRunRecorder) RecordImplementTurn(iteration int, feed, transcript string) error {
	r.files[artifactFilename(implementFeedArtifact, iteration)] = feed
	r.files[artifactFilename(implementTranscriptArtifact, iteration)] = transcript
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

func (r *memoryRunRecorder) RecordSessions(iteration int, role string, sessionIDs []string) {
	recordSessions(&r.sessions, r.sessionKeys, iteration, role, sessionIDs)
}

func (r *memoryRunRecorder) WriteSummary(iterations int, final verdict.Verdict, nits []verdict.Finding, diffStat string) error {
	r.files[artifactFilename(summaryArtifact, 0)] = formatImplementSummary(iterations, final, nits, diffStat)
	return nil
}

func (r *memoryRunRecorder) WriteSessions() error {
	sessions := append([]string(nil), r.sessions...)
	sort.Strings(sessions)
	r.files[artifactFilename(sessionsArtifact, 0)] = strings.Join(sessions, "\n") + "\n"
	return nil
}

type failingUsageRecorder struct {
	*memoryRunRecorder
	usageCalls int
}

func (r *failingUsageRecorder) RecordUsage(usage.Entry) error {
	r.usageCalls++
	return errors.New("usage artifact is unavailable")
}

func TestRunImplementIterationsRecordsReviseThenApproveInMemory(t *testing.T) {
	recorder := newMemoryRunRecorder()
	implementer := &scriptedConversationAdapter{runs: [][]harness.Event{
		{
			{Type: harness.EventSession, SessionID: "implement-1"},
			{Type: harness.EventAssistantText, Text: "first implementation"},
		},
		{
			{Type: harness.EventSession, SessionID: "implement-2"},
			{Type: harness.EventAssistantText, Text: "revised implementation"},
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
}

func TestRunImplementIterationsContinuesWhenUsageRecordingFails(t *testing.T) {
	recorder := &failingUsageRecorder{memoryRunRecorder: newMemoryRunRecorder()}
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

func TestDiskRunRecorderDeduplicatesSessionIDs(t *testing.T) {
	recorder := &diskRunRecorder{
		dir:         t.TempDir(),
		sessionKeys: make(map[sessionKey]struct{}),
	}
	recorder.RecordSessions(2, "review", []string{"review-session", "review-session"})
	recorder.RecordSessions(1, "implement", []string{"review-session"})
	recorder.RecordSessions(1, "review", []string{"review-session", "review-session-2"})
	recorder.RecordSessions(2, "review", []string{"review-session", "review-session-2", ""})

	if got, want := len(recorder.sessions), 5; got != want {
		t.Fatalf("recorded sessions = %d, want %d: %v", got, want, recorder.sessions)
	}
	if err := recorder.WriteSessions(); err != nil {
		t.Fatalf("WriteSessions() error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(recorder.Dir(), artifactFilename(sessionsArtifact, 0)))
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

type staticImplementGit struct {
	branchPoint string
	diff        string
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
