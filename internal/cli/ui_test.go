package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrochap/syl/internal/registry"
	"github.com/igorrochap/syl/internal/runmarker"
	"github.com/igorrochap/syl/internal/runstate"
)

func TestUIPrintsPlainStartupBannerAndStopMessage(t *testing.T) {
	contextToCancel, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	listener := &uiTestListener{closed: make(chan struct{}), onAccept: cancel}
	app := New("/", "/", "/Users/me/.syl", Dependencies{
		Listen: func(string, string) (net.Listener, error) { return listener, nil },
	})
	var stdout, stderr strings.Builder

	if code := app.Run(contextToCancel, []string{"ui", "--port", "8123", "--no-open"}, &stdout, &stderr); code != 0 {
		t.Fatalf("ui code = %d, stderr = %q", code, stderr.String())
	}

	assertCLIGolden(t, "ui-plain.golden", []byte(stdout.String()))
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestUIPrintsStyledStartupBannerAndStopMessage(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	contextToCancel, cancel := context.WithCancel(context.Background())
	cancel()
	listener := &uiTestListener{closed: make(chan struct{})}
	app := New("/", "/", "/Users/me/.syl", Dependencies{
		Listen: func(string, string) (net.Listener, error) { return listener, nil },
	})
	stdout := newStyledTerminalCapture(t)
	var stderr strings.Builder

	if code := app.Run(contextToCancel, []string{"ui", "--port", "8123", "--no-open"}, stdout, &stderr); code != 0 {
		t.Fatalf("ui code = %d, stderr = %q", code, stderr.String())
	}

	assertCLIGolden(t, "ui-styled.golden", stdout.Bytes())
}

func TestUIBannerCountsProjectsLiveRunsAndAwaitingAnswers(t *testing.T) {
	sylHome := t.TempDir()
	projects := []string{t.TempDir(), t.TempDir()}
	writeUIRegistry(t, sylHome, projects...)
	createUIRun(t, sylHome, projects[0], "awaiting", runstate.State{
		Status: runstate.Running, Activity: runstate.AwaitingAnswer, PID: os.Getpid(), Hostname: uiHostname(t),
		StartedAt: time.Now().UTC(), Kind: runstate.Implement, TicketRef: "#1",
	})
	createUIRun(t, sylHome, projects[0], "live", runstate.State{
		Status: runstate.Running, Activity: runstate.Implementing, PID: os.Getpid(), Hostname: uiHostname(t),
		StartedAt: time.Now().UTC(), Kind: runstate.Implement, TicketRef: "#2",
	})
	createUIRun(t, sylHome, projects[1], "interrupted", runstate.State{
		Status: runstate.Running, Activity: runstate.Reviewing, PID: 999999, Hostname: uiHostname(t),
		StartedAt: time.Now().UTC(), Kind: runstate.Review, TicketRef: "#3",
	})

	contextToCancel, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	listener := &uiTestListener{closed: make(chan struct{}), onAccept: cancel}
	app := New("/", "/", sylHome, Dependencies{
		Listen: func(string, string) (net.Listener, error) { return listener, nil },
	})
	var stdout, stderr strings.Builder

	if code := app.Run(contextToCancel, []string{"ui", "--no-open"}, &stdout, &stderr); code != 0 {
		t.Fatalf("ui code = %d, stderr = %q", code, stderr.String())
	}
	for _, expected := range []string{
		"projects:   2",
		"live runs:  3 (1 awaiting answer)",
		"browser:    not opened (--no-open)",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("stdout = %q, want %q", stdout.String(), expected)
		}
	}
}

func TestUIStartsWhenOverviewCountsAreUnavailable(t *testing.T) {
	sylHome := t.TempDir()
	if err := os.WriteFile(registry.Path(sylHome), []byte("not json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	contextToCancel, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	listener := &uiTestListener{closed: make(chan struct{}), onAccept: cancel}
	app := New("/", "/", sylHome, Dependencies{
		Listen: func(string, string) (net.Listener, error) { return listener, nil },
	})
	var stdout, stderr strings.Builder

	if code := app.Run(contextToCancel, []string{"ui", "--no-open"}, &stdout, &stderr); code != 0 {
		t.Fatalf("ui code = %d, stderr = %q", code, stderr.String())
	}
	for _, expected := range []string{"projects:   unavailable", "live runs:  unavailable"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("stdout = %q, want %q", stdout.String(), expected)
		}
	}
}

func TestUIKeepsProjectCountWhenLiveRunsAreUnavailable(t *testing.T) {
	sylHome := t.TempDir()
	writeUIRegistry(t, sylHome, t.TempDir())
	activeDir := filepath.Join(sylHome, "active")
	if err := os.MkdirAll(activeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(activeDir, "broken.json"), []byte("not json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	contextToCancel, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	listener := &uiTestListener{closed: make(chan struct{}), onAccept: cancel}
	app := New("/", "/", sylHome, Dependencies{
		Listen: func(string, string) (net.Listener, error) { return listener, nil },
	})
	var stdout, stderr strings.Builder

	if code := app.Run(contextToCancel, []string{"ui", "--no-open"}, &stdout, &stderr); code != 0 {
		t.Fatalf("ui code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "projects:   1") {
		t.Fatalf("stdout = %q, want readable Project count", stdout.String())
	}
	if !strings.Contains(stdout.String(), "live runs:  unavailable") {
		t.Fatalf("stdout = %q, want unavailable live-run count", stdout.String())
	}
}

func TestUIStartsOnRequestedLoopbackPortAndOpensBrowser(t *testing.T) {
	listener := &uiTestListener{closed: make(chan struct{})}
	var openedURL string
	contextToCancel, cancel := context.WithCancel(context.Background())
	app := New("/", "/", "/Users/me/.syl", Dependencies{
		Listen: func(network, address string) (net.Listener, error) {
			if network != "tcp" || address != "127.0.0.1:8123" {
				t.Fatalf("Listen(%q, %q), want tcp/127.0.0.1:8123", network, address)
			}
			return listener, nil
		},
		BrowserOpener: func(url string) error {
			openedURL = url
			cancel()
			return nil
		},
	})
	var stdout, stderr strings.Builder

	if code := app.Run(contextToCancel, []string{"ui", "--port", "8123"}, &stdout, &stderr); code != 0 {
		t.Fatalf("ui code = %d, stderr = %q", code, stderr.String())
	}
	if openedURL != "http://127.0.0.1:8123/" {
		t.Fatalf("opened URL = %q, want requested port", openedURL)
	}
	assertCLIGolden(t, "ui-open-plain.golden", []byte(stdout.String()))
}

func TestUINoOpenDoesNotInvokeBrowserAndDoesNotRegisterProject(t *testing.T) {
	listener := &uiTestListener{closed: make(chan struct{})}
	contextToCancel, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	called := false
	app := New("/", "/", t.TempDir(), Dependencies{
		Listen: func(string, string) (net.Listener, error) {
			return listener, nil
		},
		BrowserOpener: func(string) error {
			called = true
			return nil
		},
	})

	cancel()
	if code := app.Run(contextToCancel, []string{"ui", "--no-open"}, nil, nil); code != 0 {
		t.Fatalf("ui code = %d", code)
	}
	if called {
		t.Fatal("browser opener was called with --no-open")
	}
	if _, err := os.Stat(registry.Path(app.sylHome)); !os.IsNotExist(err) {
		t.Fatalf("registry path stat = %v, want absent registry", err)
	}
}

func TestUIPortFailureNamesRequestedPort(t *testing.T) {
	app := New("/", "/", t.TempDir(), Dependencies{
		Listen: func(string, string) (net.Listener, error) {
			return nil, errors.New("address already in use")
		},
	})
	var stdout, stderr strings.Builder

	if code := app.Run(context.Background(), []string{"ui", "--port", "7777", "--no-open"}, &stdout, &stderr); code == 0 {
		t.Fatal("ui code = 0, want port failure")
	}
	if !strings.Contains(stderr.String(), "port 7777") {
		t.Fatalf("stderr = %q, want requested port", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want no banner", stdout.String())
	}
}

func TestUIRejectsInvalidPort(t *testing.T) {
	app := New("/", "/", t.TempDir(), Dependencies{})
	var stderr strings.Builder

	if code := app.Run(context.Background(), []string{"ui", "--port", "0", "--no-open"}, nil, &stderr); code == 0 {
		t.Fatal("ui code = 0, want invalid-port failure")
	}
	if !strings.Contains(stderr.String(), "between 1 and 65535") {
		t.Fatalf("stderr = %q, want invalid-port error", stderr.String())
	}
}

func TestUIBrowserFailureKeepsServing(t *testing.T) {
	contextToCancel, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	listener := &uiTestListener{closed: make(chan struct{}), onAccept: cancel}
	app := New("/", "/", t.TempDir(), Dependencies{
		Listen: func(string, string) (net.Listener, error) { return listener, nil },
		BrowserOpener: func(string) error {
			return errors.New("browser unavailable")
		},
	})
	var stdout, stderr strings.Builder

	if code := app.Run(contextToCancel, []string{"ui"}, &stdout, &stderr); code != 0 {
		t.Fatalf("ui code = %d, want browser failure to keep serving; stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "browser:    could not open (browser unavailable); open the URL above") {
		t.Fatalf("stdout = %q, want browser failure row", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestUIBrowserOpensAfterBanner(t *testing.T) {
	contextToCancel, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	listener := &uiTestListener{closed: make(chan struct{})}
	var stdout, stderr strings.Builder
	app := New("/", "/", t.TempDir(), Dependencies{
		Listen: func(string, string) (net.Listener, error) { return listener, nil },
		BrowserOpener: func(string) error {
			if !strings.Contains(stdout.String(), "syl ui — http://127.0.0.1:8123/") {
				t.Fatal("browser opener ran before startup banner")
			}
			cancel()
			return nil
		},
	})

	if code := app.Run(contextToCancel, []string{"ui", "--port", "8123"}, &stdout, &stderr); code != 0 {
		t.Fatalf("ui code = %d, stderr = %q", code, stderr.String())
	}
}

func TestUIDefaultSeamsAreReachable(t *testing.T) {
	app := New("/", "/", t.TempDir(), Dependencies{})
	listener, err := app.uiListener(0)
	if err == nil {
		_ = listener.Close()
	}
	t.Setenv("PATH", t.TempDir())
	if err := app.openBrowser("http://127.0.0.1:7777/"); err == nil {
		t.Fatal("openBrowser() error = nil with an empty PATH")
	}
}

type uiTestListener struct {
	once       sync.Once
	acceptOnce sync.Once
	closed     chan struct{}
	onAccept   func()
}

func (listener *uiTestListener) Accept() (net.Conn, error) {
	listener.acceptOnce.Do(func() {
		if listener.onAccept != nil {
			listener.onAccept()
		}
	})
	<-listener.closed
	return nil, errors.New("listener closed")
}

func (listener *uiTestListener) Close() error {
	listener.once.Do(func() { close(listener.closed) })
	return nil
}

func (*uiTestListener) Addr() net.Addr { return uiTestAddress("127.0.0.1:7777") }

type uiTestAddress string

func (address uiTestAddress) Network() string { return "tcp" }

func (address uiTestAddress) String() string { return string(address) }

func writeUIRegistry(t *testing.T, sylHome string, projects ...string) {
	t.Helper()
	entries := make([]registry.Entry, 0, len(projects))
	for _, project := range projects {
		entries = append(entries, registry.Entry{Path: project})
	}
	contents, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registry.Path(sylHome), contents, 0o644); err != nil {
		t.Fatal(err)
	}
}

func createUIRun(t *testing.T, sylHome, project, name string, state runstate.State) {
	t.Helper()
	runDir := filepath.Join(project, ".syl", "runs", name)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runstate.Write(runstate.Path(runDir), state); err != nil {
		t.Fatal(err)
	}
	marker, err := runmarker.Create(sylHome, project, runDir, state.TicketRef, state.PID, state.Hostname)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = marker.Remove() })
}

func uiHostname(t *testing.T) string {
	t.Helper()
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	return hostname
}
