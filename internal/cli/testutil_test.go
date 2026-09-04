package cli

import (
	"bytes"
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/creack/pty"
	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/orchestration"
	"github.com/igorrochap/syl/internal/tracker"
)

var updateCLIGolden = flag.Bool("update-cli-golden", false, "rewrite CLI golden files")

type topSeamFixture struct {
	root      string
	app       *App
	harnesses map[string]harness.Adapter
	stdout    bytes.Buffer
	stderr    bytes.Buffer
}

type terminalCapture struct {
	fd uintptr
	bytes.Buffer
}

func newStyledTerminalCapture(t *testing.T) *terminalCapture {
	t.Helper()
	master, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = terminal.Close() })
	t.Cleanup(func() { _ = master.Close() })
	if err := pty.Setsize(terminal, &pty.Winsize{Cols: 120, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	return &terminalCapture{fd: terminal.Fd()}
}

func (w *terminalCapture) Fd() uintptr { return w.fd }

func assertCLIGolden(t *testing.T, name string, actual []byte) {
	t.Helper()
	if isPlainCLIGolden(name) && bytes.IndexByte(actual, '\x1b') != -1 {
		t.Fatalf("plain golden %s contains an escape sequence: %q", name, actual)
	}
	path := filepath.Join("testdata", name)
	if *updateCLIGolden {
		if err := os.WriteFile(path, actual, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, want) {
		t.Fatalf("golden %s mismatch:\n got: %q\nwant: %q", name, actual, want)
	}
}

func isPlainCLIGolden(name string) bool {
	return strings.HasPrefix(name, "plain-") || strings.Contains(name, "-plain.")
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

func fixedGLab(runner tracker.GLabRunner) func(string) tracker.GLabRunner {
	return func(string) tracker.GLabRunner { return runner }
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

type fakeGLabRunner struct{}

func (fakeGLabRunner) Run(context.Context, ...string) (string, error) { return "", nil }
