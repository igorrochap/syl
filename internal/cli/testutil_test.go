package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/orchestration"
	"github.com/igorrochap/syl/internal/tracker"
)

type topSeamFixture struct {
	root      string
	app       *App
	harnesses map[string]harness.Adapter
	stdout    bytes.Buffer
	stderr    bytes.Buffer
}

func newTopSeamFixture(t *testing.T) *topSeamFixture {
	return newTopSeamFixtureWithGit(t, false)
}

func newTopSeamFixtureWithGit(t *testing.T, initGit bool) *topSeamFixture {
	t.Helper()
	root := t.TempDir()
	if initGit {
		command := exec.Command("git", "init", "--quiet", root)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git init: %v\n%s", err, output)
		}
	}
	configPath := filepath.Join(root, ".syl", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	config := `[tracker]
issues = "github"
reviews = "local"

[roles.plan]
harness = "claude"
model = "claude-opus-5"
effort = "high"

[roles.implement]
harness = "codex"
model = "gpt-5.6-luna"
effort = "xhigh"

[roles.review]
harness = "claude"
model = "claude-sonnet-5"
effort = "medium"
`
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	harnesses := map[string]harness.Adapter{
		"claude":   fakeHarness{},
		"codex":    fakeHarness{},
		"opencode": fakeHarness{},
	}
	return &topSeamFixture{
		root:      root,
		harnesses: harnesses,
		app: New(root, root, Dependencies{
			Harnesses: harnessFactories(harnesses),
			Notifier:  fakeNotifier{},
			GH:        fixedGH(fakeGHRunner{}),
		}),
	}
}

func harnessFactories(adapters map[string]harness.Adapter) func(string) map[string]harness.Adapter {
	return func(string) map[string]harness.Adapter { return adapters }
}

func fixedGH(runner tracker.GHRunner) func(string) tracker.GHRunner {
	return func(string) tracker.GHRunner { return runner }
}

func fixedGit(runner orchestration.GitRunner) func(string) orchestration.GitRunner {
	return func(string) orchestration.GitRunner { return runner }
}

type fakeHarness struct{}

func (fakeHarness) Run(context.Context, harness.Request) (harness.Stream, error) {
	return emptyHarnessStream{}, nil
}

func (fakeHarness) Resume(context.Context, string, harness.Request) (harness.Stream, error) {
	return emptyHarnessStream{}, nil
}

func (fakeHarness) Attach(context.Context, harness.Request) error { return nil }

type emptyHarnessStream struct{}

func (emptyHarnessStream) Events() <-chan harness.Event {
	events := make(chan harness.Event)
	close(events)
	return events
}

func (emptyHarnessStream) Wait() error { return nil }

type fakeNotifier struct{}

func (fakeNotifier) Notify(context.Context, string) error { return nil }

type fakeGHRunner struct{}

func (fakeGHRunner) Run(context.Context, ...string) (string, error) { return "", nil }
