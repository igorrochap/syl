package cli

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/igorrochap/syl/internal/registry"
)

func TestUIStartsOnRequestedLoopbackPortAndOpensBrowser(t *testing.T) {
	listener := &uiTestListener{closed: make(chan struct{})}
	var openedURL string
	contextToCancel, cancel := context.WithCancel(context.Background())
	app := New("/", "/", t.TempDir(), Dependencies{
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

	if code := app.Run(contextToCancel, []string{"ui", "--port", "8123"}, nil, nil); code != 0 {
		t.Fatalf("ui code = %d", code)
	}
	if openedURL != "http://127.0.0.1:8123/" {
		t.Fatalf("opened URL = %q, want requested port", openedURL)
	}
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
	var stderr strings.Builder

	if code := app.Run(context.Background(), []string{"ui", "--port", "7777", "--no-open"}, nil, &stderr); code == 0 {
		t.Fatal("ui code = 0, want port failure")
	}
	if !strings.Contains(stderr.String(), "port 7777") {
		t.Fatalf("stderr = %q, want requested port", stderr.String())
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

func TestUIBrowserFailureClosesListener(t *testing.T) {
	listener := &uiTestListener{closed: make(chan struct{})}
	app := New("/", "/", t.TempDir(), Dependencies{
		Listen: func(string, string) (net.Listener, error) { return listener, nil },
		BrowserOpener: func(string) error {
			return errors.New("browser unavailable")
		},
	})
	var stderr strings.Builder

	if code := app.Run(context.Background(), []string{"ui"}, nil, &stderr); code == 0 {
		t.Fatal("ui code = 0, want browser failure")
	}
	if !strings.Contains(stderr.String(), "open ui in browser") {
		t.Fatalf("stderr = %q, want browser error", stderr.String())
	}
	select {
	case <-listener.closed:
	default:
		t.Fatal("listener was not closed after browser failure")
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
	once   sync.Once
	closed chan struct{}
}

func (listener *uiTestListener) Accept() (net.Conn, error) {
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
