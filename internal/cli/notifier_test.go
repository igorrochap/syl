package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/igorrochap/syl/internal/adapters/notify"
	"github.com/igorrochap/syl/internal/harness"
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
	fixture.app.deps.Harnesses["codex"] = loop
	fixture.app.deps.Harnesses["claude"] = loop
	fixture.app.deps.GH = &loopGHRunner{}

	code := fixture.app.Run(context.Background(), []string{"implement", "#42"}, &fixture.stdout, &fixture.stderr)
	if code != 0 {
		t.Fatalf("implement code = %d, want 0; stderr = %q", code, fixture.stderr.String())
	}
	if len(notifier.messages) != 0 {
		t.Fatalf("notification messages = %v, want none", notifier.messages)
	}
}

type recordingNotifier struct {
	messages []string
}

func (n *recordingNotifier) Notify(_ context.Context, message string) error {
	n.messages = append(n.messages, strings.TrimSpace(message))
	return nil
}
