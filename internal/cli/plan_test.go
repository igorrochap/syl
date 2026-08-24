package cli

import (
	"context"
	"fmt"
	"os"
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
				Model:  "claude-opus-5",
				Effort: config.EffortHigh,
				Prompt: tt.want,
				MCP:    true,
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

type planGHRunner struct {
	lists int
	after string
}

func (r *planGHRunner) Run(_ context.Context, args ...string) (string, error) {
	switch strings.Join(args, " ") {
	case "label list --limit 100 --json name":
		return `[{"name":"todo"},{"name":"doing"}]`, nil
	case "issue list --state all --limit 100 --json number,title,body,state,labels":
		r.lists++
		if r.lists == 1 || r.after == "" {
			return `[]`, nil
		}
		return r.after, nil
	default:
		return "", fmt.Errorf("unexpected gh command %q", strings.Join(args, " "))
	}
}
