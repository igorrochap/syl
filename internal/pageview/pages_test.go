package pageview_test

import (
	"os"
	"testing"
	"time"

	"github.com/igorrochap/syl/internal/pageview"
	"github.com/igorrochap/syl/internal/readmodel"
	"github.com/igorrochap/syl/internal/runrecord"
	"github.com/igorrochap/syl/internal/verdict"
)

func TestFormattersCoverPageLabels(t *testing.T) {
	for _, test := range []struct {
		name string
		in   time.Duration
		want string
	}{
		{name: "fractional second", in: 500 * time.Millisecond, want: "0s"},
		{name: "seconds", in: 59 * time.Second, want: "59s"},
		{name: "minutes discard seconds", in: 61 * time.Second, want: "1m"},
		{name: "hours retain minutes", in: time.Hour + 4*time.Minute, want: "1h 4m"},
	} {
		t.Run("duration/"+test.name, func(t *testing.T) {
			if got := pageview.FormatDuration(test.in); got != test.want {
				t.Fatalf("FormatDuration(%s) = %q, want %q", test.in, got, test.want)
			}
		})
	}

	for _, test := range []struct {
		in   int64
		want string
	}{
		{in: 0, want: "0"},
		{in: 999, want: "999"},
		{in: 1_000, want: "1.0k"},
		{in: 2_200_000, want: "2.2M"},
		{in: -3, want: "-3"},
	} {
		if got := pageview.FormatTokenCount(test.in); got != test.want {
			t.Fatalf("FormatTokenCount(%d) = %q, want %q", test.in, got, test.want)
		}
	}

	now := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name string
		at   time.Time
		want string
	}{
		{name: "unknown", want: "unknown"},
		{name: "just now", at: now.Add(-30 * time.Second), want: "just now"},
		{name: "minutes", at: now.Add(-2 * time.Minute), want: "2 min ago"},
		{name: "hours", at: now.Add(-2 * time.Hour), want: "2 h ago"},
		{name: "absolute", at: now.Add(-48 * time.Hour), want: pageview.FormatAbsoluteTime(now.Add(-48*time.Hour), pageview.AbsoluteDateTime)},
	} {
		t.Run("relative time/"+test.name, func(t *testing.T) {
			if got := pageview.FormatRelativeStartTime(test.at, now); got != test.want {
				t.Fatalf("FormatRelativeStartTime() = %q, want %q", got, test.want)
			}
		})
	}

	firstSeen := time.Date(2026, time.September, 2, 23, 0, 0, 0, time.FixedZone("offset", -3*60*60))
	if got := pageview.FormatAbsoluteTime(firstSeen, pageview.AbsoluteDate); got != "3 Sep 2026" {
		t.Fatalf("FormatAbsoluteTime(date) = %q, want UTC date", got)
	}
	if got := pageview.FormatAbsoluteTime(time.Time{}, pageview.AbsoluteDateTime); got != "—" {
		t.Fatalf("FormatAbsoluteTime(zero) = %q, want em dash", got)
	}

	for _, test := range []struct {
		name string
		ref  string
		mode pageview.TicketReferenceStyle
		want string
	}{
		{name: "plain", ref: " #00182 ", mode: pageview.TicketAsWritten, want: " #00182 "},
		{name: "normalized number", ref: "#00182", mode: pageview.TicketByNumber, want: "#182"},
		{name: "text", ref: "release-candidate", mode: pageview.TicketByNumber, want: "release-candidate"},
		{name: "blank", ref: " ", mode: pageview.TicketByNumber, want: "—"},
	} {
		if got := pageview.FormatTicketReference(test.ref, test.mode); got != test.want {
			t.Fatalf("FormatTicketReference(%q) = %q, want %q", test.ref, got, test.want)
		}
	}

	if got := pageview.FormatRunKind("", pageview.RunKindAsRun); got != "Run" {
		t.Fatalf("FormatRunKind(missing overview kind) = %q, want Run", got)
	}
	if got := pageview.FormatRunKind("", pageview.RunKindAsDash); got != "—" {
		t.Fatalf("FormatRunKind(missing history kind) = %q, want em dash", got)
	}
	if got := pageview.FormatRunKind(runrecord.Review, pageview.RunKindAsDash); got != "review" {
		t.Fatalf("FormatRunKind(review) = %q, want review", got)
	}
	if got := pageview.FormatHarnessAndModel("", "model"); got != "unknown · model" {
		t.Fatalf("FormatHarnessAndModel() = %q, want unknown harness with model", got)
	}
	if got := pageview.FormatHarnessAndModel("codex", ""); got != "codex" {
		t.Fatalf("FormatHarnessAndModel() = %q, want codex", got)
	}

	for _, test := range []struct {
		iteration int
		maximum   int
		style     pageview.IterationStyle
		want      string
	}{
		{style: pageview.IterationFraction, want: "—"},
		{iteration: 2, maximum: 3, style: pageview.IterationFraction, want: "2 / 3"},
		{iteration: 2, style: pageview.IterationOptionalMaximum, want: "2"},
		{iteration: 2, maximum: 3, style: pageview.IterationRunSummary, want: "2 of 3 iterations"},
		{iteration: 2, style: pageview.IterationRunSummary, want: "2 iterations"},
	} {
		if got := pageview.FormatIteration(test.iteration, test.maximum, test.style); got != test.want {
			t.Fatalf("FormatIteration() = %q, want %q", got, test.want)
		}
	}
}

func TestPresentRunStatusCoversObservedStatuses(t *testing.T) {
	for _, test := range []struct {
		status               runrecord.ObservedStatus
		label                string
		overviewLabel        string
		pillClass            string
		activityClass        string
		rowClass             string
		activityApplicable   bool
		showRecordedActivity bool
		dismissible          bool
	}{
		{status: runrecord.ObservedRunning, label: "running", overviewLabel: "running", pillClass: "running", activityClass: "running", activityApplicable: true, showRecordedActivity: true},
		{status: runrecord.ObservedInterrupted, label: "Interrupted", overviewLabel: "Interrupted", pillClass: "red", activityClass: "interrupted", rowClass: "interrupted", dismissible: true},
		{status: runrecord.ObservedApproved, label: "approved", overviewLabel: "approved", pillClass: "green", activityClass: "running", showRecordedActivity: true},
		{status: runrecord.ObservedExhausted, label: "exhausted", overviewLabel: "exhausted", pillClass: "plum", activityClass: "running", showRecordedActivity: true},
		{status: runrecord.ObservedFailed, label: "failed", overviewLabel: "failed", pillClass: "red", activityClass: "running", showRecordedActivity: true},
		{status: runrecord.ObservedCancelled, label: "cancelled", overviewLabel: "cancelled", pillClass: "neutral", activityClass: "running", showRecordedActivity: true},
		{status: runrecord.ObservedCompleted, label: "completed", overviewLabel: "completed", pillClass: "completed", activityClass: "running", showRecordedActivity: true},
		{status: runrecord.ObservedUnknown, label: "unknown", overviewLabel: "Unknown", pillClass: "unknown", activityClass: "unknown"},
	} {
		got := pageview.PresentRunStatus(test.status)
		if got.Label != test.label || got.OverviewLabel != test.overviewLabel ||
			got.PillClass != test.pillClass || got.ActivityClass != test.activityClass ||
			got.RowClass != test.rowClass || got.ActivityApplicable != test.activityApplicable ||
			got.ShowRecordedActivity != test.showRecordedActivity || got.Dismissible != test.dismissible {
			t.Errorf("PresentRunStatus(%q) = %#v", test.status, got)
		}
	}
}

func TestBuildOverviewPreparesCardsAndRunRows(t *testing.T) {
	now := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	model := readmodel.Overview{
		AwaitingAnswer: []readmodel.Run{{
			ProjectName: "project", ProjectPath: "/project", TicketRef: "#182", Kind: runrecord.Implement,
			Iteration: 2, MaxIterations: 3, StartedAt: now.Add(-2 * time.Minute), Question: "Question", WorkRoot: "/worktree",
		}},
		LiveRuns: []readmodel.Run{{
			ProjectName: "project", ProjectPath: "/project", RunDir: "/project/.syl/runs/run",
			TicketRef: "#183", Kind: runrecord.Review, Status: runrecord.ObservedInterrupted,
			Activity: "reviewing", Iteration: 1, MaxIterations: 2, Harness: "codex", Model: "gpt-5.6",
			StartedAt: now.Add(-48 * time.Hour), Hostname: hostname + "-remote", PID: 42,
		}},
		Projects: []readmodel.Project{
			{Name: "broken", Path: "/broken", Health: readmodel.HealthInvalid},
			{Name: "missing", Path: "/missing", Health: readmodel.HealthMissing},
		},
	}

	page := pageview.BuildOverview(model, now)
	if page.Chrome.Title != "syl ui — Overview" || page.Chrome.ServerDotClass != "live-dot" ||
		page.Chrome.ServerState != "refreshing every 3s" || page.Chrome.CopyMode != "path" {
		t.Fatalf("Overview chrome = %+v", page.Chrome)
	}
	if page.TotalRunCount != 2 || page.ProjectCount != 2 || len(page.AwaitingAnswer) != 1 || len(page.LiveRuns) != 1 {
		t.Fatalf("BuildOverview counts = %#v", page)
	}
	if got := page.AwaitingAnswer[0]; got.Ticket != "#182" || got.Kind != "implement" ||
		got.Iteration != "2 / 3" || got.Started != "2 min ago" || got.Question != "Question" {
		t.Fatalf("awaiting-answer view = %#v", got)
	}
	if got := page.LiveRuns[0]; got.Status.Label != "Interrupted" || got.Status.RowClass != "interrupted" ||
		got.Harness != "codex · gpt-5.6" || got.ShowRemoteHostname != true || !got.Dismissible {
		t.Fatalf("live Run view = %#v", got)
	}
	if got := page.Projects[0]; got.HealthClass != "pill-red" || got.Summary != "config fails to load · No live Runs" {
		t.Fatalf("invalid Project card = %#v", got)
	}
	if got := page.Projects[1]; !got.Missing || got.HealthClass != "pill-neutral" || got.Summary != "directory not found · No live Runs" {
		t.Fatalf("missing Project card = %#v", got)
	}
}

func TestBuildOverviewKeepsRecordedActivityForTerminalRuns(t *testing.T) {
	page := pageview.BuildOverview(readmodel.Overview{LiveRuns: []readmodel.Run{{
		Status: runrecord.ObservedApproved, Activity: "reviewing",
	}}}, time.Now())
	if len(page.LiveRuns) != 1 || !page.LiveRuns[0].ShowRecordedActivity || page.LiveRuns[0].Status.ActivityClass != "running" {
		t.Fatalf("terminal Run activity view = %#v", page.LiveRuns)
	}
}

func TestBuildProjectFormatsHistoryRows(t *testing.T) {
	now := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	firstSeen := time.Date(2026, time.September, 2, 0, 0, 0, 0, time.UTC)
	page := pageview.BuildProject(readmodel.ProjectPage{
		Project: readmodel.Project{
			Name: "project", Path: "/project", Health: readmodel.HealthOK,
			IssueTracker: "github", ReviewLog: "local", FirstSeen: firstSeen,
		},
		Runs: []readmodel.HistoryRun{{
			RunDir: "/project/.syl/runs/run", TicketRef: "#00184", Kind: runrecord.Implement,
			Status: runrecord.ObservedApproved, Iteration: 1, MaxIterations: 3, Verdict: "approve",
			StartedAt: now.Add(-2 * time.Hour), Duration: 61 * time.Second, DurationKnown: true,
			TotalTokens: 2_200_000, TokensKnown: true,
		}},
	}, now)
	if page.Chrome.Title != "syl ui — Project runs" || page.Chrome.ServerDotClass != "live-dot" || page.Chrome.ServerState != "refreshing every 3s" {
		t.Fatalf("Project chrome = %+v", page.Chrome)
	}
	if page.Name != "project" || page.HealthClass != "pill-green" || page.FirstSeen != "2 Sep 2026" || page.RunCount != 1 {
		t.Fatalf("Project page = %#v", page)
	}
	if got := page.Runs[0]; got.Ticket != "#184" || got.Kind != "implement" || got.Status.Label != "approved" ||
		got.Iteration != "1 / 3" || got.Started != "2 h ago" || got.Duration != "1m" || got.Tokens != "2.2M" {
		t.Fatalf("Run history row = %#v", got)
	}
}

func TestBuildRunPreparesAllPageSections(t *testing.T) {
	now := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	started := now.Add(-time.Hour)
	ended := now.Add(-30 * time.Minute)
	model := readmodel.RunPage{
		Run: readmodel.RunDetail{
			RunDir: "/project/.syl/runs/run", ProjectName: "project", ProjectPath: "/project",
			TicketRef: "#00173", Branch: "feature", Kind: runrecord.Implement,
			Status: runrecord.ObservedApproved, Iteration: 1, MaxIterations: 3,
			StartedAt: started, EndedAt: &ended, Duration: 30 * time.Minute, DurationKnown: true,
			TotalTokens: 2_080, TokensKnown: true,
		},
		Metadata: readmodel.RunMetadata{
			Branch: "feature", BranchPoint: "base", WorkRoot: "/worktree", RunDirectory: ".syl/runs/run",
			Sessions: []readmodel.Session{{Iteration: 1, Role: "implement", SessionID: "session-id"}},
		},
		Summary: "ready", SummaryExists: true, DiffStat: "file.go | 2 ++", DiffStatKnown: true,
		Iterations: []readmodel.Iteration{{
			Number: 1, Activity: "reviewing", HasVerdict: true,
			Verdict:       verdict.Verdict{Status: verdict.Approve, Summary: "Ready"},
			BlockingCount: 1, NitCount: 2,
			Roles: []readmodel.RoleRun{{
				Role: "implement", Harness: "codex", Model: "gpt-5.6",
				Artifacts: []readmodel.Artifact{{Name: "iteration-01-implement.feed"}},
			}},
			FindingGroups: []readmodel.FindingGroup{{
				Label: "Blocking", Count: 1,
				Findings: []verdict.Finding{{Location: "file.go:1", Issue: "Fix this"}},
			}},
		}},
		Usage:  []readmodel.RoleUsage{{Role: "implement", InputTokens: 2_000, OutputTokens: 80, CachedTokens: 10, Known: true}},
		Resume: []readmodel.ResumeCommand{{Command: "syl resume implement #173"}},
	}

	page := pageview.BuildRun(model)
	if page.Chrome.Title != "syl ui — Run #173" || page.Chrome.ServerDotClass != "ended-dot" ||
		page.Chrome.ServerState != "run ended, not refreshing" || page.Chrome.MainClass != "run-main-page" ||
		page.Chrome.CopyMode != "command" {
		t.Fatalf("ended Run chrome = %+v", page.Chrome)
	}
	if page.Run.Ticket != "#173" || page.Run.Branch != "feature" || page.Run.Kind != "implement" ||
		page.Run.Status.Label != "approved" || page.Run.Iteration != "1 of 3 iterations" ||
		page.Run.Duration != "30m" || page.Run.Tokens != "2.1k" || page.Run.Started != pageview.FormatAbsoluteTime(started, pageview.AbsoluteDateTime) {
		t.Fatalf("Run header = %#v", page.Run)
	}
	if page.Metadata.Sessions[0] != "it 1 implement: session-id" || page.Summary != "ready" || page.DiffStat != "file.go | 2 ++" {
		t.Fatalf("Run metadata or summary = %#v / %q / %q", page.Metadata, page.Summary, page.DiffStat)
	}
	iteration := page.Iterations[0]
	if iteration.Activity != "reviewing" || iteration.VerdictClass != "green" || iteration.VerdictStatus != "approve" ||
		iteration.Roles[0].Harness != "codex · gpt-5.6" || iteration.Roles[0].Artifacts[0].Label != "feed" ||
		iteration.FindingGroups[0].Findings[0].Issue != "Fix this" {
		t.Fatalf("Run iteration = %#v", iteration)
	}
	if got := page.Usage[0]; got.InputTokens != "2.0k" || got.OutputTokens != "80" || got.CachedTokens != "10" {
		t.Fatalf("Run usage = %#v", got)
	}
	if len(page.Resume) != 1 || page.Resume[0] != "syl resume implement #173" {
		t.Fatalf("Run resume commands = %#v", page.Resume)
	}
}

func TestBuildRunUsesOneStatusMappingAcrossPages(t *testing.T) {
	status := runrecord.ObservedInterrupted
	overview := pageview.BuildOverview(readmodel.Overview{LiveRuns: []readmodel.Run{{Status: status}}}, time.Now())
	project := pageview.BuildProject(readmodel.ProjectPage{Runs: []readmodel.HistoryRun{{Status: status}}}, time.Now())
	run := pageview.BuildRun(readmodel.RunPage{Run: readmodel.RunDetail{Status: status}})
	if overview.LiveRuns[0].Status.Label != "Interrupted" || project.Runs[0].Status.Label != "Interrupted" || run.Run.Status.Label != "Interrupted" {
		t.Fatalf("page status labels differ: overview=%#v project=%#v run=%#v", overview.LiveRuns[0].Status, project.Runs[0].Status, run.Run.Status)
	}
	if overview.LiveRuns[0].Status.PillClass != "red" || project.Runs[0].Status.PillClass != "red" || run.Run.Status.PillClass != "red" {
		t.Fatalf("page status styles differ: overview=%#v project=%#v run=%#v", overview.LiveRuns[0].Status, project.Runs[0].Status, run.Run.Status)
	}
}

func TestBuildRunPreparesEmptySummaryAsDisplayValue(t *testing.T) {
	page := pageview.BuildRun(readmodel.RunPage{SummaryExists: true})
	if !page.SummaryExists || page.Summary != "—" {
		t.Fatalf("empty recorded Summary = %#v, want em dash and visible section", page)
	}
}

func TestPageChromeReflectsRunningRunAndConfigPage(t *testing.T) {
	run := pageview.BuildRun(readmodel.RunPage{Run: readmodel.RunDetail{TicketRef: "#204", Refreshing: true}})
	if run.Chrome.Title != "syl ui — Run #204" || run.Chrome.ServerDotClass != "live-dot" ||
		run.Chrome.ServerState != "refreshing every 3s" {
		t.Fatalf("running Run chrome = %+v", run.Chrome)
	}

	config := pageview.ConfigPageChrome()
	if config.Title != "syl ui — Project config" || config.ServerDotClass != "live-dot" ||
		config.ServerState != "refreshing every 3s" || config.MainClass != "config-main" || !config.ConfigStyles {
		t.Fatalf("Config chrome = %+v", config)
	}
}
