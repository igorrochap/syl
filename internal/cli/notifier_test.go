package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gitadapter "github.com/igorrochap/syl/internal/adapters/git"
	"github.com/igorrochap/syl/internal/adapters/notify"
	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/orchestration"
)

func TestSelectNotificationBackendByPlatform(t *testing.T) {
	tests := []struct {
		name          string
		goos          string
		kernelRelease string
		environment   map[string]string
		want          notify.NotificationBackend
	}{
		{name: "macOS", goos: "darwin", want: notify.NotificationMacOS},
		{name: "Linux desktop", goos: "linux", environment: map[string]string{"DISPLAY": ":0"}, want: notify.NotificationNotifySend},
		{name: "WSL2", goos: "linux", kernelRelease: "6.1.21-microsoft-standard-WSL2", environment: map[string]string{"DISPLAY": ":0"}, want: notify.NotificationPowerShell},
		{name: "fallback", goos: "freebsd", want: notify.NotificationTerminalBell},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := notify.SelectNotificationBackend(tt.goos, tt.kernelRelease, tt.environment); got != tt.want {
				t.Fatalf("selectNotificationBackend() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestImplementDoesNotNotifyWhenNotificationsAreDisabled(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	configPath := filepath.Join(fixture.root, ".syl", "config.toml")
	configContents, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	configContents = append(configContents, []byte("\n[notifications]\nenabled = false\n")...)
	if err := os.WriteFile(configPath, configContents, 0o644); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "-C", fixture.root, "add", ".syl/config.toml")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git add config: %v\n%s", err, output)
	}
	command = exec.Command("git", "-C", fixture.root, "-c", "user.name=syl test", "-c", "user.email=syl@example.test", "commit", "--quiet", "-m", "notifications disabled")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git commit config: %v\n%s", err, output)
	}
	notifier := &recordingNotifier{}
	fixture.app.deps.Notifier = notifier
	loop := &loopHarness{
		root: fixture.root,
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement"}},
			{{Type: harness.EventSession, SessionID: "review"}, {Type: harness.EventAssistantText, Text: approveVerdictText}},
		},
	}
	fixture.harnesses["codex"] = loop
	fixture.harnesses["claude"] = loop
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})

	code := fixture.app.Run(context.Background(), []string{"implement", "#42"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, want 0; stderr = %q", code, fixture.stderr.String())
	}
	if len(notifier.messages) != 0 {
		t.Fatalf("notification messages = %v, want none", notifier.messages)
	}
}

func TestImplementCompletionNotificationIdentifiesProjectAndActiveBranch(t *testing.T) {
	fixture := newImplementLoopFixture(t)
	notifier := &recordingNotifier{}
	loop := &loopHarness{
		root: fixture.root,
		streams: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement"}},
			{{Type: harness.EventSession, SessionID: "review"}, {Type: harness.EventAssistantText, Text: approveVerdictText}},
		},
	}
	fixture.harnesses["codex"] = loop
	fixture.harnesses["claude"] = loop
	fixture.app.deps.GH = fixedGH(&loopGHRunner{})
	fixture.app.deps.Notifier = notifier

	code := fixture.app.Run(context.Background(), []string{"implement", "#42"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, stderr = %q", code, fixture.stderr.String())
	}

	want := "[project: " + filepath.Base(fixture.root) + " | branch: feat/add-resilient-workflow] implement #42 finished: approve"
	if len(notifier.messages) != 1 || notifier.messages[0] != want {
		t.Fatalf("notifications = %v, want %q", notifier.messages, want)
	}
}

func TestImplementCompletionNotificationUsesUnknownBranchWhenGitBranchIsUnavailable(t *testing.T) {
	tests := []struct {
		name        string
		branchValue string
		branchErr   error
	}{
		{name: "empty branch", branchValue: "\n"},
		{name: "git error", branchErr: errors.New("git branch failed")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newImplementLoopFixture(t)
			notifier := &recordingNotifier{}
			loop := &loopHarness{
				root: fixture.root,
				streams: [][]harness.Event{
					{{Type: harness.EventSession, SessionID: "implement"}},
					{{Type: harness.EventSession, SessionID: "review"}, {Type: harness.EventAssistantText, Text: approveVerdictText}},
				},
			}
			fixture.harnesses["codex"] = loop
			fixture.harnesses["claude"] = loop
			fixture.app.deps.GH = fixedGH(&loopGHRunner{})
			fixture.app.deps.Git = fixedGit(&branchLookupGit{
				delegate:    gitadapter.ExecGitRunner{Dir: fixture.root},
				branchValue: tt.branchValue,
				branchErr:   tt.branchErr,
			})
			fixture.app.deps.Notifier = notifier

			code := fixture.app.Run(context.Background(), []string{"implement", "#42"}, &fixture.stdout, &fixture.stderr)
			if code != 0 {
				t.Fatalf("implement code = %d, stderr = %q", code, fixture.stderr.String())
			}

			want := "[project: " + filepath.Base(fixture.root) + " | branch: unknown-branch] implement #42 finished: approve"
			if len(notifier.messages) != 1 || notifier.messages[0] != want {
				t.Fatalf("notifications = %v, want %q", notifier.messages, want)
			}
		})
	}
}

type recordingNotifier struct {
	messages []string
}

func (n *recordingNotifier) Notify(_ context.Context, message string) error {
	n.messages = append(n.messages, strings.TrimSpace(message))
	return nil
}

type branchLookupGit struct {
	delegate    orchestration.GitRunner
	branchValue string
	branchErr   error
}

func (g *branchLookupGit) Run(ctx context.Context, args ...string) (string, error) {
	if strings.Join(args, " ") == "branch --show-current" {
		return g.branchValue, g.branchErr
	}
	return g.delegate.Run(ctx, args...)
}
