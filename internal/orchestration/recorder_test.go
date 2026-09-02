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

func TestRunImplementPassesContextToImplementer(t *testing.T) {
	const additionalContext = "Use the existing GitRunner seam.\nDo not add a new adapter."

	git := &implementRunGit{}
	implementer := &capturingImplementAdapter{response: "Implemented the ticket."}
	reviewer := &capturingImplementAdapter{response: conversationTestVerdict}
	var output strings.Builder
	err := RunImplement(context.Background(), ImplementOptions{
		OriginRoot: t.TempDir(),
		WorkRoot:   t.TempDir(),
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

func TestImplementRunRecorderRecordsRoleContexts(t *testing.T) {
	implementContext := "Keep the existing recorder.\nPreserve metadata ordering."
	reviewContext := "Check the metadata output."
	recorder, err := newImplementRunRecorder(
		t.TempDir(),
		42,
		"feat/record-role-context",
		"abc123",
		implementContext,
		reviewContext,
	)
	if err != nil {
		t.Fatalf("newImplementRunRecorder() error = %v", err)
	}

	metadata := readRunMetadataArtifact(t, recorder.Dir())
	want := "Branch: feat/record-role-context\n" +
		"Branch point: abc123\n" +
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
	recorder, err := newImplementRunRecorder(
		t.TempDir(),
		42,
		"feat/record-role-context",
		"abc123",
		"",
		"",
	)
	if err != nil {
		t.Fatalf("newImplementRunRecorder() error = %v", err)
	}

	const want = "Branch: feat/record-role-context\nBranch point: abc123\n"
	if metadata := readRunMetadataArtifact(t, recorder.Dir()); metadata != want {
		t.Fatalf("metadata = %q, want %q", metadata, want)
	}
}

func TestReviewRunRecorderRecordsReviewerContext(t *testing.T) {
	reviewContext := "Review only the parser.\nDo not modify files."
	recorder, err := newReviewRunRecorder(t.TempDir(), "#42", "abc123", reviewContext)
	if err != nil {
		t.Fatalf("newReviewRunRecorder() error = %v", err)
	}

	metadata := readRunMetadataArtifact(t, recorder.Dir())
	want := "Ticket: #42\n" +
		"Branch point: abc123\n" +
		"Reviewer context:\n" +
		"  Review only the parser.\n" +
		"  Do not modify files.\n"
	if metadata != want {
		t.Fatalf("metadata = %q, want %q", metadata, want)
	}
}

func TestRunRecorderKeepsMetadataKeysOutsideMultilineContext(t *testing.T) {
	implementContext := "first line\nBranch: something\nthird line"
	recorder, err := newImplementRunRecorder(
		t.TempDir(),
		42,
		"feat/record-role-context",
		"abc123",
		implementContext,
		"",
	)
	if err != nil {
		t.Fatalf("newImplementRunRecorder() error = %v", err)
	}

	metadata := readRunMetadataArtifact(t, recorder.Dir())
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
