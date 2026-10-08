package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/harness"
)

func TestPlanComposesInteractivePromptForFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "tickets",
			args: []string{"plan", "add offline mode"},
			want: "/to-tickets\n\nTopic: add offline mode\n\n" +
				"Use the to-tickets skill to produce tickets on the configured GitHub tracker.",
		},
		{
			name: "spec then tickets",
			args: []string{"plan", "--spec", "add offline mode"},
			want: "/to-spec\n\nTopic: add offline mode\n\n" +
				"First use the to-spec skill to publish a spec on the configured GitHub tracker. " +
				"After the spec is complete, use the to-tickets skill to produce tickets on the configured GitHub tracker.",
		},
		{
			name: "grill then tickets",
			args: []string{"plan", "--grill", "add offline mode"},
			want: "/grill-me\n\nTopic: add offline mode\n\n" +
				"First use the grill-me skill to grill the user on this topic. " +
				"After the grilling is complete, use the to-tickets skill to produce tickets on the configured GitHub tracker.",
		},
		{
			name: "grill with docs then tickets",
			args: []string{"plan", "--grill", "--with-docs", "add offline mode"},
			want: "/grill-with-docs\n\nTopic: add offline mode\n\n" +
				"First use the grill-with-docs skill to grill the user on this topic. " +
				"After the grilling is complete, use the to-tickets skill to produce tickets on the configured GitHub tracker.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newPlanFixture(t)
			adapter := &planHarness{}
			fixture.harnesses["claude"] = adapter

			code := fixture.app.Run(context.Background(), tt.args, &fixture.stdout, &fixture.stderr)
			if code != 0 {
				t.Fatalf("plan code = %d, stderr = %q", code, fixture.stderr.String())
			}
			if len(adapter.requests) != 1 {
				t.Fatalf("attach requests = %d, want 1", len(adapter.requests))
			}
			wantRequest := harness.Request{
				Model:       "claude-opus-5",
				Effort:      config.EffortHigh,
				Prompt:      tt.want,
				MCP:         true,
				SandboxMode: config.SandboxModeFullAccess,
			}
			if !reflect.DeepEqual(adapter.requests[0], wantRequest) {
				t.Fatalf("Attach request = %#v, want %#v", adapter.requests[0], wantRequest)
			}
			if !strings.Contains(fixture.stdout.String(), "No tickets created.") {
				t.Fatalf("stdout = %q, want zero-created report", fixture.stdout.String())
			}
		})
	}
}

func TestPlanRejectsWithDocsWithoutStartingSession(t *testing.T) {
	fixture := newPlanFixture(t)
	adapter := &planHarness{}
	fixture.harnesses["claude"] = adapter

	code := fixture.app.Run(context.Background(), []string{"plan", "--with-docs", "add offline mode"}, &fixture.stdout, &fixture.stderr)
	if code == 0 || !strings.Contains(fixture.stderr.String(), "--with-docs requires --grill") {
		t.Fatalf("plan code = %d, stderr = %q, want flag validation failure", code, fixture.stderr.String())
	}
	if len(adapter.requests) != 0 {
		t.Fatalf("attach requests = %d, want 0", len(adapter.requests))
	}
}

func TestPlanFailsBeforeAttachWhenReferencedSkillIsMissing(t *testing.T) {
	fixture := newPlanFixture(t)
	adapter := &planHarness{}
	fixture.harnesses["claude"] = adapter
	if err := os.RemoveAll(filepath.Join(fixture.root, ".agents", "skills", "to-spec")); err != nil {
		t.Fatal(err)
	}

	code := fixture.app.Run(context.Background(), []string{"plan", "--spec", "add offline mode"}, &fixture.stdout, &fixture.stderr)
	if code == 0 || !strings.Contains(fixture.stderr.String(), "to-spec") {
		t.Fatalf("plan code = %d, stderr = %q, want missing to-spec failure", code, fixture.stderr.String())
	}
	if len(adapter.requests) != 0 {
		t.Fatalf("attach requests = %d, want 0", len(adapter.requests))
	}
}

func TestPlanReportsTicketsCreatedOnLocalTracker(t *testing.T) {
	fixture := newPlanFixture(t)
	setIssueTracker(t, fixture.root, config.TrackerLocal)
	writePlanTicket(t, fixture.root, "existing", 13, "Existing", "None — can start immediately")
	adapter := &planHarness{attach: func() error {
		writePlanTicket(t, fixture.root, "offline", 14, "Sync later", "#13")
		writePlanTicket(t, fixture.root, "offline", 15, "Cache writes", "None — can start immediately")
		return nil
	}}
	fixture.harnesses["claude"] = adapter

	code := fixture.app.Run(context.Background(), []string{"plan", "add offline mode"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("plan code = %d, stderr = %q", code, fixture.stderr.String())
	}
	for _, want := range []string{"Created: #14, #15", "Next: syl implement 15"} {
		if !strings.Contains(fixture.stdout.String(), want) {
			t.Fatalf("stdout = %q, want %q", fixture.stdout.String(), want)
		}
	}
}

func TestPlanReportsTicketsCreatedOnGitHubTracker(t *testing.T) {
	fixture := newPlanFixture(t)
	github := fixture.app.deps.GH(fixture.app.originRoot).(*planGHRunner)
	github.after = `[` +
		`{"number":23,"title":"Blocked","body":"## Blocked by\n\n- #22","state":"OPEN","labels":[]},` +
		`{"number":22,"title":"Ready","body":"## Blocked by\n\n- None","state":"OPEN","labels":[]}` +
		`]`

	code := fixture.app.Run(context.Background(), []string{"plan", "add offline mode"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("plan code = %d, stderr = %q", code, fixture.stderr.String())
	}
	for _, want := range []string{"Created: #22, #23", "Next: syl implement 22"} {
		if !strings.Contains(fixture.stdout.String(), want) {
			t.Fatalf("stdout = %q, want %q", fixture.stdout.String(), want)
		}
	}
}

func TestPlanWithoutNoRemoteKeepsGitHubMissingRemoteError(t *testing.T) {
	fixture := newPlanFixture(t)
	runner := &planGHRunner{
		beforeErr:    errors.New("repository lookup failed"),
		beforeOutput: "unable to determine current repository",
	}
	fixture.app.deps.GH = fixedGH(runner)
	adapter := &planHarness{}
	fixture.harnesses["claude"] = adapter

	code := fixture.app.Run(context.Background(), []string{"plan", "add offline mode"}, &fixture.stdout, &fixture.stderr)
	if code == 0 || !strings.Contains(fixture.stderr.String(), "no GitHub remote found") {
		t.Fatalf("plan code = %d, stderr = %q, want existing no-remote error", code, fixture.stderr.String())
	}
	if len(adapter.requests) != 0 {
		t.Fatalf("attach requests = %d, want 0", len(adapter.requests))
	}
}

func TestPlanNoRemoteAttachesAfterMissingGitHubRemoteAtStart(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{name: "missing remote", output: "unable to determine current repository"},
		{name: "not a git repository", output: "not a git repository"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPlanFixture(t)
			runner := &planGHRunner{beforeErr: errors.New("repository lookup failed"), beforeOutput: test.output}
			fixture.app.deps.GH = fixedGH(runner)
			adapter := &planHarness{}
			fixture.harnesses["claude"] = adapter

			code := fixture.app.Run(context.Background(), []string{"plan", "--no-remote", "add offline mode"}, &fixture.stdout, &fixture.stderr)
			if code != 0 {
				t.Fatalf("plan code = %d, stderr = %q", code, fixture.stderr.String())
			}
			if len(adapter.requests) != 1 {
				t.Fatalf("attach requests = %d, want 1", len(adapter.requests))
			}
		})
	}
}

func TestPlanNoRemoteStillFailsForGitHubAuthenticationAndInstallationErrors(t *testing.T) {
	tests := []struct {
		name       string
		runnerErr  error
		runnerText string
		want       string
	}{
		{name: "unauthenticated", runnerErr: errors.New("gh auth status failed"), runnerText: "not logged into any GitHub hosts", want: "gh auth login"},
		{name: "not installed", runnerErr: exec.ErrNotFound, want: "install GitHub CLI"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPlanFixture(t)
			fixture.app.deps.GH = fixedGH(&planGHRunner{beforeErr: test.runnerErr, beforeOutput: test.runnerText})
			adapter := &planHarness{}
			fixture.harnesses["claude"] = adapter

			code := fixture.app.Run(context.Background(), []string{"plan", "--no-remote", "add offline mode"}, &fixture.stdout, &fixture.stderr)
			if code == 0 || !strings.Contains(fixture.stderr.String(), test.want) {
				t.Fatalf("plan code = %d, stderr = %q, want %q", code, fixture.stderr.String(), test.want)
			}
			if len(adapter.requests) != 0 {
				t.Fatalf("attach requests = %d, want 0", len(adapter.requests))
			}
		})
	}
}

func TestPlanNoRemoteReportsMissingOriginAfterGitHubSession(t *testing.T) {
	fixture := newPlanFixture(t)
	fixture.app.deps.GH = fixedGH(&planGHRunner{
		afterErr:    errors.New("repository lookup failed"),
		afterOutput: "unable to determine current repository",
	})
	adapter := &planHarness{}
	fixture.harnesses["claude"] = adapter

	code := fixture.app.Run(context.Background(), []string{"plan", "--no-remote", "add offline mode"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("plan code = %d, stderr = %q", code, fixture.stderr.String())
	}
	if !strings.Contains(fixture.stdout.String(), "No tickets created (no origin configured).") {
		t.Fatalf("stdout = %q, want missing-origin notice", fixture.stdout.String())
	}
	if len(adapter.requests) != 1 {
		t.Fatalf("attach requests = %d, want 1", len(adapter.requests))
	}
}

func TestPlanNoRemoteReportsEveryTicketWhenGitHubOriginAppears(t *testing.T) {
	fixture := newPlanFixture(t)
	runner := &planGHRunner{
		beforeErr:    errors.New("repository lookup failed"),
		beforeOutput: "unable to determine current repository",
		after: `[{"number":1,"title":"First","body":"**Blocked by:** None","state":"OPEN","labels":[]},` +
			`{"number":2,"title":"Second","body":"**Blocked by:** None","state":"OPEN","labels":[]}]`,
	}
	fixture.app.deps.GH = fixedGH(runner)
	fixture.harnesses["claude"] = &planHarness{}

	code := fixture.app.Run(context.Background(), []string{"plan", "--no-remote", "add offline mode"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("plan code = %d, stderr = %q", code, fixture.stderr.String())
	}
	for _, want := range []string{"Created: #1, #2", "Next: syl implement 1"} {
		if !strings.Contains(fixture.stdout.String(), want) {
			t.Fatalf("stdout = %q, want %q", fixture.stdout.String(), want)
		}
	}
}

func TestPlanNoRemoteLeavesExistingGitHubRemoteOutputUnchanged(t *testing.T) {
	outputs := make([]string, 0, 2)
	codes := make([]int, 0, 2)
	for _, args := range [][]string{
		{"plan", "add offline mode"},
		{"plan", "--no-remote", "add offline mode"},
	} {
		fixture := newPlanFixture(t)
		fixture.app.deps.GH = fixedGH(&planGHRunner{})
		fixture.harnesses["claude"] = &planHarness{}
		codes = append(codes, fixture.app.Run(context.Background(), args, &fixture.stdout, &fixture.stderr))
		outputs = append(outputs, fixture.stdout.String()+"\n"+fixture.stderr.String())
	}
	if codes[0] != codes[1] || outputs[0] != outputs[1] {
		t.Fatalf("plan with --no-remote = (%d, %q), without = (%d, %q), want identical behavior", codes[1], outputs[1], codes[0], outputs[0])
	}
}

func TestPlanNoRemoteHasNoEffectOnLocalTracker(t *testing.T) {
	outputs := make([]string, 0, 2)
	codes := make([]int, 0, 2)
	for _, args := range [][]string{
		{"plan", "add offline mode"},
		{"plan", "--no-remote", "add offline mode"},
	} {
		fixture := newPlanFixture(t)
		setIssueTracker(t, fixture.root, config.TrackerLocal)
		fixture.harnesses["claude"] = &planHarness{}
		codes = append(codes, fixture.app.Run(context.Background(), args, &fixture.stdout, &fixture.stderr))
		outputs = append(outputs, fixture.stdout.String()+"\n"+fixture.stderr.String())
	}
	if codes[0] != codes[1] || outputs[0] != outputs[1] {
		t.Fatalf("local plan with --no-remote = (%d, %q), without = (%d, %q), want identical behavior", codes[1], outputs[1], codes[0], outputs[0])
	}
}

func TestPlanNoRemoteSupportsGitLabTracker(t *testing.T) {
	tests := []struct {
		name   string
		labels []planGLabResult
		issues []planGLabResult
		want   []string
	}{
		{
			name: "remote remains missing after session",
			labels: []planGLabResult{
				{output: "no GitLab project found for this directory", err: errors.New("project lookup failed")},
				{output: "no GitLab project found for this directory", err: errors.New("project lookup failed")},
			},
			want: []string{"No tickets created (no origin configured)."},
		},
		{
			name: "origin appears and tickets are listed",
			labels: []planGLabResult{
				{output: "not a git repository", err: errors.New("repository lookup failed")},
				{output: `[{"name":"todo"},{"name":"doing"}]`},
			},
			issues: []planGLabResult{{output: `[{"iid":7,"title":"First","description":"**Blocked by:** None","state":"opened","labels":["todo"]},{"iid":8,"title":"Second","description":"**Blocked by:** None","state":"opened","labels":["todo"]}]`}},
			want:   []string{"Created: #7, #8", "Next: syl implement 7"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPlanFixture(t)
			setIssueTracker(t, fixture.root, config.TrackerGitLab)
			fixture.app.deps.GLab = fixedGLab(&planGLabRunner{labels: test.labels, issues: test.issues})
			adapter := &planHarness{}
			fixture.harnesses["claude"] = adapter

			code := fixture.app.Run(context.Background(), []string{"plan", "--no-remote", "add offline mode"}, &fixture.stdout, &fixture.stderr)
			if code != 0 {
				t.Fatalf("plan code = %d, stderr = %q", code, fixture.stderr.String())
			}
			for _, want := range test.want {
				if !strings.Contains(fixture.stdout.String(), want) {
					t.Fatalf("stdout = %q, want %q", fixture.stdout.String(), want)
				}
			}
			if len(adapter.requests) != 1 {
				t.Fatalf("attach requests = %d, want 1", len(adapter.requests))
			}
		})
	}
}

func TestPlanHelpDescribesNoRemoteFlag(t *testing.T) {
	fixture := newPlanFixture(t)
	code := fixture.app.Run(context.Background(), []string{"plan", "--help"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("plan --help code = %d, stderr = %q", code, fixture.stderr.String())
	}
	for _, want := range []string{"--no-remote", "remote trackers"} {
		if !strings.Contains(fixture.stdout.String(), want) {
			t.Fatalf("plan help = %q, want %q", fixture.stdout.String(), want)
		}
	}
}

func TestPlanOutputMatchesPlainAndStyledGoldens(t *testing.T) {
	for _, test := range []struct {
		name   string
		styled bool
	}{
		{name: "plain"},
		{name: "styled", styled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.styled {
				t.Setenv("NO_COLOR", "")
			} else {
				t.Setenv("NO_COLOR", "1")
			}
			fixture := newPlanFixture(t)
			setIssueTracker(t, fixture.root, config.TrackerLocal)
			fixture.harnesses["claude"] = &planHarness{attach: func() error {
				writePlanTicket(t, fixture.root, "offline", 14, "Sync later", "None — can start immediately")
				return nil
			}}
			var output interface {
				io.Writer
				String() string
			} = &fixture.stdout
			if test.styled {
				output = newStyledTerminalCapture(t)
			}
			code := fixture.app.Run(context.Background(), []string{"plan", "#42"}, output, &fixture.stderr)
			if code != 0 {
				t.Fatalf("plan code = %d, stderr = %q", code, fixture.stderr.String())
			}
			assertCLIGolden(t, "plan-"+test.name+".golden", []byte(output.String()))
		})
	}
}

func newPlanFixture(t *testing.T) *topSeamFixture {
	t.Helper()
	fixture := newTopSeamFixture(t)
	for _, name := range []string{"grill-me", "grill-with-docs", "to-spec", "to-tickets"} {
		path := filepath.Join(fixture.root, ".agents", "skills", name, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("---\nname: "+name+"\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fixture.app.deps.GH = fixedGH(&planGHRunner{})
	return fixture
}

func setIssueTracker(t *testing.T, root string, issueTracker config.Tracker) {
	t.Helper()
	path := filepath.Join(root, ".syl", "config.toml")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(contents), `issues = "github"`, fmt.Sprintf("issues = %q", issueTracker), 1)
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writePlanTicket(t *testing.T, root, feature string, number int, title, blockedBy string) {
	t.Helper()
	path := filepath.Join(root, ".scratch", feature, "issues", fmt.Sprintf("%02d-ticket.md", number))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	contents := fmt.Sprintf(
		"# %02d — %s\n\n**What to build:** Details.\n\n**Blocked by:** %s\n\n**Status:** todo\n",
		number,
		title,
		blockedBy,
	)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

type planHarness struct {
	requests []harness.Request
	attach   func() error
}

func (*planHarness) Run(context.Context, harness.Request) (harness.Stream, error) {
	return nil, fmt.Errorf("unexpected harness run")
}

func (*planHarness) Resume(context.Context, string, harness.Request) (harness.Stream, error) {
	return nil, fmt.Errorf("unexpected resume")
}

func (h *planHarness) Attach(_ context.Context, request harness.Request) error {
	h.requests = append(h.requests, request)
	if h.attach != nil {
		return h.attach()
	}
	return nil
}

func (*planHarness) AttachSession(context.Context, string, harness.Request) error {
	return fmt.Errorf("unexpected session attach")
}

type planGHRunner struct {
	lists        int
	after        string
	beforeErr    error
	beforeOutput string
	afterErr     error
	afterOutput  string
}

func (r *planGHRunner) Run(_ context.Context, args ...string) (string, error) {
	switch strings.Join(args, " ") {
	case "label list --limit 100 --json name":
		return `[{"name":"todo"},{"name":"doing"}]`, nil
	case "issue list --state all --limit 100 --json number,title,body,state,labels":
		r.lists++
		if r.lists == 1 && r.beforeErr != nil {
			return r.beforeOutput, r.beforeErr
		}
		if r.lists > 1 && r.afterErr != nil {
			return r.afterOutput, r.afterErr
		}
		if r.lists == 1 || r.after == "" {
			return `[]`, nil
		}
		return r.after, nil
	default:
		return "", fmt.Errorf("unexpected gh command %q", strings.Join(args, " "))
	}
}

type planGLabResult struct {
	output string
	err    error
}

type planGLabRunner struct {
	labels     []planGLabResult
	issues     []planGLabResult
	labelCalls int
	issueCalls int
}

func (r *planGLabRunner) Run(_ context.Context, args ...string) (string, error) {
	switch strings.Join(args, " ") {
	case "label list --output json --per-page 100":
		result := planGLabResult{output: `[{"name":"todo"},{"name":"doing"}]`}
		if r.labelCalls < len(r.labels) {
			result = r.labels[r.labelCalls]
		}
		r.labelCalls++
		return result.output, result.err
	case "issue list --all --output json --per-page 100":
		result := planGLabResult{output: `[]`}
		if r.issueCalls < len(r.issues) {
			result = r.issues[r.issueCalls]
		}
		r.issueCalls++
		return result.output, result.err
	default:
		return "", fmt.Errorf("unexpected glab command %q", strings.Join(args, " "))
	}
}
