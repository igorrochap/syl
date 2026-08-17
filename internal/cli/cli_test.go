package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/igorrochap/rig/internal/config"
	"github.com/igorrochap/rig/internal/harness"
)

func TestRunHelpListsAllCommands(t *testing.T) {
	fixture := newTopSeamFixture(t)

	code := fixture.app.Run(context.Background(), []string{"--help"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("Run() code = %d, want 0; stderr = %q", code, fixture.stderr.String())
	}
	for _, command := range []string{"init", "sync", "plan", "implement", "review"} {
		if !strings.Contains(fixture.stdout.String(), command) {
			t.Errorf("help output %q does not list %q", fixture.stdout.String(), command)
		}
	}
}

func TestRunSubcommandHelpIsAvailableWithoutConfig(t *testing.T) {
	app := New(t.TempDir(), Dependencies{})
	var stdout, stderr strings.Builder

	code := app.Run(context.Background(), []string{"sync", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run() code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "synchronize tracker data") {
		t.Fatalf("stdout = %q, want sync help", stdout.String())
	}
}

func TestQuestionAnswerInputProtocolIsDocumentedInHelp(t *testing.T) {
	fixture := newTopSeamFixture(t)

	code := fixture.app.Run(context.Background(), []string{"review", "--help"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("review help code = %d, stderr = %q", code, fixture.stderr.String())
	}
	if !strings.Contains(fixture.stdout.String(), "multi-line answer from stdin until an empty line or EOF") {
		t.Fatalf("review help = %q, want QUESTION input protocol", fixture.stdout.String())
	}
}

func TestRunRefusesCommandsWithoutConfig(t *testing.T) {
	for _, command := range []string{"sync", "plan", "implement", "review"} {
		t.Run(command, func(t *testing.T) {
			root := t.TempDir()
			app := New(root, Dependencies{})
			var stdout, stderr strings.Builder

			code := app.Run(context.Background(), []string{command}, &stdout, &stderr)
			if code == 0 {
				t.Fatal("Run() code = 0, want failure")
			}
			if !strings.Contains(stderr.String(), "run rig init") {
				t.Fatalf("stderr = %q, want init guidance", stderr.String())
			}
		})
	}
}

func TestRunStubCommandThroughTopSeam(t *testing.T) {
	fixture := newTopSeamFixture(t)

	code := fixture.app.Run(context.Background(), []string{"sync"}, &fixture.stdout, &fixture.stderr)
	if code == 0 {
		t.Fatal("Run() code = 0, want not-implemented failure")
	}
	if !strings.Contains(fixture.stderr.String(), "sync: not implemented yet") {
		t.Fatalf("stderr = %q, want stub message", fixture.stderr.String())
	}
}

func TestImplementResolvesAndMarksGitHubTicketDoing(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	loop := &loopHarness{
		root: fixture.root,
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement"}},
			{{Type: harness.EventSession, SessionID: "review"}, {Type: harness.EventAssistantText, Text: approveVerdictText}},
		},
	}
	fixture.app.deps.Harnesses["codex"] = loop
	fixture.app.deps.Harnesses["claude"] = loop
	github := &implementGitHubRunner{}
	fixture.app.deps.GH = github

	code := fixture.app.Run(context.Background(), []string{"implement", "#42"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, want 0; stderr = %q", code, fixture.stderr.String())
	}
	if !github.hasCall("issue edit 42 --add-label doing --remove-label todo") {
		t.Fatalf("GitHub calls = %#v, want todo to doing transition", github.calls)
	}
	if github.hasCall("issue close 42") || github.hasCall("issue edit 42 --state closed") {
		t.Fatalf("GitHub calls = %#v, want no close operation", github.calls)
	}
}

func TestRunStubCommandThroughGitBackedTopSeam(t *testing.T) {
	fixture := newTopSeamFixtureWithGit(t, true)

	code := fixture.app.Run(context.Background(), []string{"sync"}, &fixture.stdout, &fixture.stderr)
	if code == 0 {
		t.Fatal("Run() code = 0, want not-implemented failure")
	}
	if !strings.Contains(fixture.stderr.String(), "sync: not implemented yet") {
		t.Fatalf("stderr = %q, want stub message", fixture.stderr.String())
	}
}

func TestRunRejectsUnexpectedCommandArguments(t *testing.T) {
	fixture := newTopSeamFixture(t)

	code := fixture.app.Run(context.Background(), []string{"sync", "unexpected"}, &fixture.stdout, &fixture.stderr)
	if code == 0 {
		t.Fatal("Run() code = 0, want argument validation failure")
	}
	if !strings.Contains(fixture.stderr.String(), `unknown command "unexpected"`) {
		t.Fatalf("stderr = %q, want Cobra argument error", fixture.stderr.String())
	}
}

func TestRunInitCreatesConfig(t *testing.T) {
	root := t.TempDir()
	app := New(root, Dependencies{Input: defaultInitInput()})
	var stdout, stderr strings.Builder

	code := app.Run(context.Background(), []string{"init"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run() code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".rig", "config.toml")); err != nil {
		t.Fatalf("init config: %v", err)
	}
	if _, err := config.Load(root); err != nil {
		t.Fatalf("init wrote invalid config: %v", err)
	}
	if !strings.Contains(stdout.String(), ".rig/config.toml") {
		t.Fatalf("stdout = %q, want config path", stdout.String())
	}
}

func TestNewDefaultsToCurrentDirectoryWhenProjectRootIsEmpty(t *testing.T) {
	root := t.TempDir()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(workingDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
	app := New("", Dependencies{Input: defaultInitInput()})
	var stdout, stderr strings.Builder

	code := app.Run(context.Background(), []string{"init"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run() code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".rig", "config.toml")); err != nil {
		t.Fatalf("default project root config: %v", err)
	}
}

type implementGitHubRunner struct {
	calls []string
}

func (r *implementGitHubRunner) Run(_ context.Context, args ...string) (string, error) {
	call := strings.Join(args, " ")
	r.calls = append(r.calls, call)
	switch call {
	case "label list --limit 100 --json name":
		return `[{"name":"todo"},{"name":"doing"}]`, nil
	case "issue view 42 --json number,title,body,state,labels":
		return `{"number":42,"title":"Implement this","body":"Details","state":"OPEN","labels":[{"name":"todo"}]}`, nil
	case "issue edit 42 --add-label doing --remove-label todo":
		return "", nil
	default:
		return "", fmt.Errorf("unexpected gh command %q", call)
	}
}

func (r *implementGitHubRunner) hasCall(call string) bool {
	for _, got := range r.calls {
		if got == call {
			return true
		}
	}
	return false
}
