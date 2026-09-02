package cli

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/igorrochap/syl/internal/harness"
)

func TestImplementPrintsIdentificationBannerWithResolvedConfigAndArtifacts(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	configureBannerRole(t, fixture.root, "implement", "custom-implementer", "high")
	configureBannerRole(t, fixture.root, "review", "claude-custom-reviewer", "low")
	configureBannerMaxIterations(t, fixture.root, 2)
	commitWorkingTree(t, fixture.root, "configure banner")

	loop := &loopHarness{
		root: fixture.root,
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement-session"}},
			{{Type: harness.EventSession, SessionID: "review-session"}, {Type: harness.EventAssistantText, Text: approveVerdictText}},
		},
	}
	fixture.harnesses["codex"] = loop
	fixture.harnesses["claude"] = loop
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})

	code := fixture.app.Run(context.Background(), []string{"implement", "#42"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, stderr = %q", code, fixture.stderr.String())
	}

	runDirs, err := filepath.Glob(filepath.Join(fixture.root, ".syl", "runs", "*-42"))
	if err != nil || len(runDirs) != 1 {
		t.Fatalf("run directories = %v, err = %v; want one issue artifact directory", runDirs, err)
	}
	relativeRunDir, err := filepath.Rel(fixture.root, runDirs[0])
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := "syl implement #42 — Add resilient workflow\n" +
		"  implementer:     codex · custom-implementer · effort high\n" +
		"  reviewer:        claude · claude-custom-reviewer · effort low\n" +
		"  max iterations:  2\n" +
		"  run artifacts:   " + relativeRunDir + "\n"
	if got := fixture.stdout.String(); !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("stdout = %q, want prefix %q", got, wantPrefix)
	}
}

func TestImplementBannerShowsContextForEachRole(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	loop := &loopHarness{
		root: fixture.root,
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement-session"}},
			{{Type: harness.EventSession, SessionID: "review-session"}, {Type: harness.EventAssistantText, Text: approveVerdictText}},
		},
	}
	fixture.harnesses["codex"] = loop
	fixture.harnesses["claude"] = loop
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})

	code := fixture.app.Run(context.Background(), []string{
		"implement", "#42",
		"--implement-context", "implementer context",
		"--review-context", "reviewer context",
	}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, stderr = %q", code, fixture.stderr.String())
	}
	for _, expected := range []string{
		"implementer:     codex · gpt-5.6-luna · effort xhigh · context",
		"reviewer:        claude · claude-sonnet-5 · effort medium · context",
	} {
		if !strings.Contains(fixture.stdout.String(), expected) {
			t.Errorf("stdout = %q, want context indicator %q", fixture.stdout.String(), expected)
		}
	}
}

func TestReviewPrintsTicketIdentificationBannerBeforeVerdict(t *testing.T) {
	harness := &scriptedHarness{first: []harness.Event{
		{Type: harness.EventSession, SessionID: "review-session"},
		{Type: harness.EventAssistantText, Text: approveVerdictText},
	}}
	fixture := newReviewFixture(t, harness)
	configureLocalIssues(t, fixture.root)
	writeReviewTicket(t, fixture.root, "feature-a", "07", "Improve the tracker", "Review the requested tracker behavior.")

	code := fixture.app.Run(context.Background(), []string{"review", "#07"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("review code = %d, stderr = %q", code, fixture.stderr.String())
	}
	wantPrefix := "syl review #07 — Improve the tracker\n" +
		"  reviewer:  claude · claude-sonnet-5 · effort medium\n"
	if got := fixture.stdout.String(); !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("stdout = %q, want prefix %q", got, wantPrefix)
	}
	if got := fixture.stdout.String(); strings.Contains(got, "implementer:") || strings.Contains(got, "max iterations:") || strings.Contains(got, "run artifacts:") {
		t.Fatalf("stdout = %q, want only the reviewer banner line", got)
	}
}

func TestReviewBannerShowsContext(t *testing.T) {
	harness := &scriptedHarness{first: []harness.Event{
		{Type: harness.EventSession, SessionID: "review-session"},
		{Type: harness.EventAssistantText, Text: approveVerdictText},
	}}
	fixture := newReviewFixture(t, harness)

	code := fixture.app.Run(context.Background(), []string{"review", "--context", "reviewer context"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("review code = %d, stderr = %q", code, fixture.stderr.String())
	}
	want := "  reviewer:  claude · claude-sonnet-5 · effort medium · context\n"
	if !strings.Contains(fixture.stdout.String(), want) {
		t.Fatalf("stdout = %q, want context indicator %q", fixture.stdout.String(), want)
	}
}

func TestPlanPrintsIdentificationBannerForInvokedTarget(t *testing.T) {
	fixture := newPlanFixture(t)

	code := fixture.app.Run(context.Background(), []string{"plan", "#42"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("plan code = %d, stderr = %q", code, fixture.stderr.String())
	}
	wantPrefix := "syl plan #42\n" +
		"  planner:     claude · claude-opus-5 · effort high\n"
	if got := fixture.stdout.String(); !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("stdout = %q, want prefix %q", got, wantPrefix)
	}
	if strings.Contains(fixture.stdout.String(), "implementer:") || strings.Contains(fixture.stdout.String(), "reviewer:") {
		t.Fatalf("stdout = %q, want only the plan banner line", fixture.stdout.String())
	}
}

func TestReviewValidationFailurePrintsNoIdentificationBanner(t *testing.T) {
	fixture := newReviewFixture(t, &scriptedHarness{})
	if err := os.WriteFile(filepath.Join(fixture.root, ".syl", "config.toml"), []byte("[roles.review]\nharness = \"claude\"\nmodel = \"invalid\"\neffort = \"medium\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	code := fixture.app.Run(context.Background(), []string{"review"}, &fixture.stdout, &fixture.stderr)
	if code == 0 {
		t.Fatal("review code = 0, want config validation failure")
	}
	if got := fixture.stdout.String(); got != "" {
		t.Fatalf("stdout = %q, want no banner after config validation failure", got)
	}
}

func TestReviewUnconfiguredHarnessPrintsNoIdentificationBanner(t *testing.T) {
	fixture := newReviewFixture(t, &scriptedHarness{})
	fixture.app.deps.Harnesses = nil

	code := fixture.app.Run(context.Background(), []string{"review"}, &fixture.stdout, &fixture.stderr)
	if code == 0 {
		t.Fatal("review code = 0, want unconfigured-harness failure")
	}
	if got := fixture.stdout.String(); got != "" {
		t.Fatalf("stdout = %q, want no banner before an unconfigured harness failure", got)
	}
}

func configureBannerRole(t *testing.T, root, role, model, effort string) {
	t.Helper()
	path := filepath.Join(root, ".syl", "config.toml")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	contentsString := string(contents)
	oldModel := map[string]string{
		"implement": "gpt-5.6-luna",
		"review":    "claude-sonnet-5",
	}[role]
	contentsString = strings.Replace(contentsString, `model = "`+oldModel+`"`, `model = "`+model+`"`, 1)
	oldEffort := map[string]string{
		"implement": "xhigh",
		"review":    "medium",
	}[role]
	contentsString = strings.Replace(contentsString, `effort = "`+oldEffort+`"`, `effort = "`+effort+`"`, 1)
	if err := os.WriteFile(path, []byte(contentsString), 0o644); err != nil {
		t.Fatal(err)
	}
}

func configureBannerMaxIterations(t *testing.T, root string, max int) {
	t.Helper()
	path := filepath.Join(root, ".syl", "config.toml")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := string(contents) + "\n[loop]\nmax_iterations = " + strconv.Itoa(max) + "\n"
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
}
