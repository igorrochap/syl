package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/igorrochap/rig/internal/harness"
)

type topSeamFixture struct {
	root   string
	app    *App
	stdout bytes.Buffer
	stderr bytes.Buffer
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
	configPath := filepath.Join(root, ".rig", "config.toml")
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

	return &topSeamFixture{
		root: root,
		app: New(root, Dependencies{
			Harnesses: map[string]harness.Adapter{
				"claude":   fakeHarness{},
				"codex":    fakeHarness{},
				"opencode": fakeHarness{},
			},
			Notifier: fakeNotifier{},
			GH:       fakeGHRunner{},
		}),
	}
}

type fakeHarness struct{}

func (fakeHarness) Run(context.Context, harness.Request) (harness.Stream, error) {
	return emptyHarnessStream{}, nil
}

func (fakeHarness) Resume(context.Context, string, string) (harness.Stream, error) {
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
