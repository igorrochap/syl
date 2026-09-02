package orchestration

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/tracker"
	"github.com/igorrochap/syl/internal/verdict"
)

func TestFormatVerdictRoundTripsWithAndWithoutFindings(t *testing.T) {
	tests := []struct {
		name         string
		verdict      verdict.Verdict
		wantSentinel bool
	}{
		{
			name:         "without findings",
			wantSentinel: true,
			verdict: verdict.Verdict{
				Status:  verdict.Approve,
				Summary: "Ready",
			},
		},
		{
			name: "with findings",
			verdict: verdict.Verdict{
				Status:  verdict.Revise,
				Summary: "Fix the remaining issue",
				Findings: []verdict.Finding{
					{Kind: verdict.Blocking, Location: "review.go:42", Issue: "Handle the error."},
					{Kind: verdict.Nit, Location: "review_test.go:10", Issue: "Keep the test focused."},
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			formatted := formatVerdict(test.verdict)
			if test.wantSentinel && !strings.Contains(formatted, "- (none)") {
				t.Fatalf("formatVerdict() = %q, want the no-findings sentinel", formatted)
			}
			parsed, err := verdict.Parse(formatted)
			if err != nil {
				t.Fatalf("verdict.Parse() error = %v", err)
			}
			if !reflect.DeepEqual(parsed, test.verdict) {
				t.Fatalf("parsed verdict = %#v, want %#v", parsed, test.verdict)
			}
		})
	}
}

func TestWriteParsedEventReportsToolSeparatorErrors(t *testing.T) {
	output := &separatorFailingWriter{}
	if err := writeParsedEvent(output, harness.Event{Type: harness.EventAssistantText, Text: "prose"}); err != nil {
		t.Fatalf("writeParsedEvent(assistant) error = %v", err)
	}

	err := writeParsedEvent(output, harness.Event{Type: harness.EventToolUse, ToolName: "Bash"})
	if err == nil || !strings.Contains(err.Error(), "separate tool output") {
		t.Fatalf("writeParsedEvent(tool) error = %v, want separator failure", err)
	}
}

func TestOutputAtLineStartHandlesWriterShapes(t *testing.T) {
	var buffer bytes.Buffer
	if !outputAtLineStart(&buffer) {
		t.Fatal("outputAtLineStart(empty bytes.Buffer) = false, want true")
	}
	if !outputAtLineStart(io.Discard) {
		t.Fatal("outputAtLineStart(io.Discard) = false, want true")
	}
	if !bytesAtLineStart(nil) {
		t.Fatal("bytesAtLineStart(nil) = false, want true")
	}
}

func TestCompleteStandaloneReviewSeparatesRenderedVerdictFromStreamedProse(t *testing.T) {
	var output strings.Builder
	output.WriteString("Reviewer prose.")

	err := completeStandaloneReview(context.Background(), ReviewOptions{
		OriginRoot: t.TempDir(),
		ProjectConfig: config.Config{
			Tracker: config.TrackerConfig{Reviews: config.TrackerLocal},
		},
		Output: &output,
	}, reviewPreparation{
		branchPoint: "HEAD",
		recorder:    newMemoryRunRecorder(),
	}, standaloneReviewRun{
		review: ReviewExecution{
			Verdict: verdict.Verdict{Status: verdict.Approve, Summary: "Ready"},
		},
	})
	if err != nil {
		t.Fatalf("completeStandaloneReview() error = %v", err)
	}

	want := "Reviewer prose.\nVERDICT: approve\nSUMMARY: Ready\nFINDINGS:\n- (none)\n"
	if output.String() != want {
		t.Fatalf("standalone review output = %q, want %q", output.String(), want)
	}
}

func TestRunReviewSeparatesRenderedVerdictForOpaqueOutputWriter(t *testing.T) {
	root := t.TempDir()
	git := &reviewDiffGit{responses: map[string]reviewDiffResponse{
		"rev-parse HEAD":                          {output: "branch-point\n"},
		"diff branch-point":                       {output: "diff --git a/change.txt b/change.txt\n+reviewed\n"},
		"ls-files --others --exclude-standard -z": {},
	}}
	output := &opaqueReviewWriter{}

	err := RunReview(context.Background(), ReviewOptions{
		OriginRoot: root,
		WorkRoot:   root,
		ProjectConfig: config.Config{
			Tracker: config.TrackerConfig{Reviews: config.TrackerLocal},
			Roles: config.RolesConfig{Review: config.RoleConfig{
				Harness: config.HarnessClaude,
			}},
		},
		Adapter: &capturingReviewAdapter{events: []harness.Event{
			{Type: harness.EventSession, SessionID: "review-session"},
			{Type: harness.EventAssistantText, Text: "Reviewer prose.\nVERDICT: approve\nSUMMARY: Ready\nFINDINGS:"},
		}},
		Input:   strings.NewReader(""),
		Output:  output,
		Verbose: true,
		Git:     git,
	})
	if err != nil {
		t.Fatalf("RunReview() error = %v, want nil", err)
	}

	want := "Reviewer prose.\nVERDICT: approve\nSUMMARY: Ready\nFINDINGS:\nVERDICT: approve\nSUMMARY: Ready\nFINDINGS:\n- (none)\n"
	if output.value.String() != want {
		t.Fatalf("standalone review output = %q, want %q", output.value.String(), want)
	}
}

type opaqueReviewWriter struct {
	value bytes.Buffer
}

func (w *opaqueReviewWriter) Write(p []byte) (int, error) {
	return w.value.Write(p)
}

type separatorFailingWriter struct {
	value bytes.Buffer
}

func (w *separatorFailingWriter) Write(p []byte) (int, error) {
	if string(p) == "\n" {
		return 0, errors.New("separator write failed")
	}
	return w.value.Write(p)
}

func (w *separatorFailingWriter) String() string {
	return w.value.String()
}

func TestFormatRemoteReviewCommentPreservesBody(t *testing.T) {
	got := formatRemoteReviewComment(verdict.Verdict{
		Status:  verdict.Approve,
		Summary: "Ready",
		Findings: []verdict.Finding{
			{Kind: verdict.Blocking, Location: "internal/config/config.go:42", Issue: "Use the remote predicate."},
		},
	})
	want := "## Review verdict\n\n```text\nVERDICT: approve\nSUMMARY: Ready\nFINDINGS:\n- [blocking] internal/config/config.go:42 — Use the remote predicate.\n```\n"
	if got != want {
		t.Fatalf("formatRemoteReviewComment() = %q, want %q", got, want)
	}
}

func TestRunReviewPostsRemoteReviewComment(t *testing.T) {
	root := t.TempDir()
	git := &reviewDiffGit{responses: map[string]reviewDiffResponse{
		"rev-parse HEAD":                          {output: "branch-point\n"},
		"diff branch-point":                       {output: "diff --git a/change.txt b/change.txt\n+reviewed\n"},
		"ls-files --others --exclude-standard -z": {},
	}}
	remote := &recordingReviewTracker{}
	ticket := tracker.Ticket{Number: 42}

	err := RunReview(context.Background(), ReviewOptions{
		OriginRoot: root,
		ProjectConfig: config.Config{
			Tracker: config.TrackerConfig{Reviews: config.TrackerGitHub},
			Roles:   config.RolesConfig{Review: config.RoleConfig{Harness: config.HarnessClaude}},
		},
		IssueTracker: remote,
		Ticket:       &ticket,
		TicketRef:    "#42",
		Adapter:      &capturingReviewAdapter{},
		Input:        strings.NewReader(""),
		Output:       &strings.Builder{},
		Git:          git,
	})
	if err != nil {
		t.Fatalf("RunReview() error = %v, want nil", err)
	}
	want := "## Review verdict\n\n```text\nVERDICT: approve\nSUMMARY: Ready\nFINDINGS:\n- (none)\n```\n"
	if remote.comment != want {
		t.Fatalf("remote review comment = %q, want %q", remote.comment, want)
	}
}

func TestRunReviewPassesAdditionalContextToHarness(t *testing.T) {
	root := t.TempDir()
	adapter := &capturingReviewAdapter{}
	git := &reviewDiffGit{responses: map[string]reviewDiffResponse{
		"rev-parse HEAD":                          {output: "branch-point\n"},
		"diff branch-point":                       {output: "diff --git a/change.txt b/change.txt\n+reviewed\n"},
		"ls-files --others --exclude-standard -z": {},
	}}
	ticket := tracker.Ticket{Number: 42, Title: "Parser review", Body: "Only review the parser behavior."}
	var output strings.Builder

	err := RunReview(context.Background(), ReviewOptions{
		OriginRoot: root,
		WorkRoot:   root,
		ProjectConfig: config.Config{
			Tracker: config.TrackerConfig{Reviews: config.TrackerLocal},
			Roles: config.RolesConfig{Review: config.RoleConfig{
				Harness: config.HarnessClaude,
				Model:   "claude-sonnet-5",
				Effort:  config.EffortMedium,
			}},
		},
		Ticket:    &ticket,
		TicketRef: "#42",
		Context:   "  only the parser changes matter  ",
		Adapter:   adapter,
		Input:     strings.NewReader(""),
		Output:    &output,
		Git:       git,
		IdentificationBanner: func() error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("RunReview() error = %v, want nil", err)
	}
	if !strings.Contains(adapter.request.Prompt, "## Additional context supplied by the user for this run\n\nonly the parser changes matter") {
		t.Fatalf("harness prompt = %q, want trimmed additional context", adapter.request.Prompt)
	}
}

func TestRunReviewValidatesOptions(t *testing.T) {
	tests := []struct {
		name    string
		options ReviewOptions
		wantErr string
	}{
		{
			name: "raw and verbose",
			options: ReviewOptions{
				Raw: true, Verbose: true, Adapter: &capturingReviewAdapter{},
			},
			wantErr: "review: --raw and --verbose are mutually exclusive",
		},
		{
			name: "missing adapter",
			options: ReviewOptions{
				ProjectConfig: config.Config{Roles: config.RolesConfig{Review: config.RoleConfig{Harness: config.HarnessClaude}}},
			},
			wantErr: `review harness "claude" is not configured`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := RunReview(context.Background(), test.options)
			if err == nil || err.Error() != test.wantErr {
				t.Fatalf("RunReview() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestRunReviewSavesUnparseableReviewArtifacts(t *testing.T) {
	root := t.TempDir()
	git := &reviewDiffGit{responses: map[string]reviewDiffResponse{
		"rev-parse HEAD":                          {output: "branch-point\n"},
		"diff branch-point":                       {output: "diff --git a/change.txt b/change.txt\n+reviewed\n"},
		"ls-files --others --exclude-standard -z": {},
	}}
	var output strings.Builder

	err := RunReview(context.Background(), ReviewOptions{
		OriginRoot: root,
		WorkRoot:   root,
		ProjectConfig: config.Config{
			Tracker: config.TrackerConfig{Reviews: config.TrackerLocal},
			Roles:   config.RolesConfig{Review: config.RoleConfig{Harness: config.HarnessClaude}},
		},
		Adapter: &unparseableReviewAdapter{
			first: []harness.Event{
				{Type: harness.EventSession, SessionID: "review-session"},
				{Type: harness.EventAssistantText, Text: "missing verdict"},
			},
			retry: []harness.Event{
				{Type: harness.EventSession, SessionID: "review-session"},
				{Type: harness.EventAssistantText, Text: "still missing verdict"},
			},
		},
		Input:  strings.NewReader(""),
		Output: &output,
		Git:    git,
	})
	if err == nil || !strings.Contains(err.Error(), "reviewer produced no parseable verdict") {
		t.Fatalf("RunReview() error = %v, want unparseable-verdict failure", err)
	}
}

type capturingReviewAdapter struct {
	request harness.Request
	events  []harness.Event
}

func (a *capturingReviewAdapter) Run(_ context.Context, request harness.Request) (harness.Stream, error) {
	a.request = request
	events := a.events
	if events == nil {
		events = []harness.Event{
			{Type: harness.EventSession, SessionID: "review-session"},
			{Type: harness.EventAssistantText, Text: "VERDICT: approve\nSUMMARY: Ready\nFINDINGS:\n"},
		}
	}
	return scriptedConversationStream{events: events}, nil
}

func (*capturingReviewAdapter) Resume(context.Context, string, harness.Request) (harness.Stream, error) {
	return nil, nil
}

func (*capturingReviewAdapter) Attach(context.Context, harness.Request) error { return nil }

type unparseableReviewAdapter struct {
	first []harness.Event
	retry []harness.Event
}

func (a *unparseableReviewAdapter) Run(context.Context, harness.Request) (harness.Stream, error) {
	return scriptedConversationStream{events: a.first}, nil
}

func (a *unparseableReviewAdapter) Resume(context.Context, string, harness.Request) (harness.Stream, error) {
	return scriptedConversationStream{events: a.retry}, nil
}

func (*unparseableReviewAdapter) Attach(context.Context, harness.Request) error { return nil }

type recordingReviewTracker struct {
	comment string
}

func (*recordingReviewTracker) Resolve(context.Context, string) (tracker.Ticket, error) {
	return tracker.Ticket{}, nil
}

func (*recordingReviewTracker) List(context.Context) ([]tracker.Ticket, error) {
	return nil, nil
}

func (*recordingReviewTracker) UpdateStatus(context.Context, int, string) error {
	return nil
}

func (t *recordingReviewTracker) AddComment(_ context.Context, _ int, note string) error {
	t.comment = note
	return nil
}

func (*recordingReviewTracker) Create(context.Context, string, string) (tracker.Ticket, error) {
	return tracker.Ticket{}, nil
}
