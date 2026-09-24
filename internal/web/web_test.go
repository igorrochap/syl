package web_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/registry"
	"github.com/igorrochap/syl/internal/runmarker"
	"github.com/igorrochap/syl/internal/runstate"
	"github.com/igorrochap/syl/internal/web"
)

func TestHandlerRejectsUntrustedHostWithoutPageContent(t *testing.T) {
	server, err := web.New(t.TempDir(), 7777)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://evil.example:7777/", nil)
	request.Host = "evil.example:7777"
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusMisdirectedRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusMisdirectedRequest)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("body = %q, want no page content", recorder.Body.String())
	}
}

func TestHandlerRendersEmbeddedOverviewAndAssets(t *testing.T) {
	server, err := web.New(t.TempDir(), 7777)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7777/", nil)
	request.Host = "127.0.0.1:7777"
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %q", recorder.Code, recorder.Body.String())
	}
	for _, expected := range []string{
		"syl<span>/ui</span>",
		"syl-dark",
		"hx-trigger=\"every 3s\"",
		"No Projects registered yet",
		"/assets/style.css",
	} {
		if !strings.Contains(recorder.Body.String(), expected) {
			t.Fatalf("overview body does not contain %q", expected)
		}
	}

	assetRequest := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7777/assets/htmx.min.js", nil)
	assetRequest.Host = "127.0.0.1:7777"
	assetRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(assetRecorder, assetRequest)
	if assetRecorder.Code != http.StatusOK || !strings.Contains(assetRecorder.Body.String(), `version:"2.0.4"`) {
		t.Fatalf("htmx asset status/body = %d/%q", assetRecorder.Code, assetRecorder.Body.String())
	}
	styleRequest := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7777/assets/style.css", nil)
	styleRequest.Host = "127.0.0.1:7777"
	styleRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(styleRecorder, styleRequest)
	if styleRecorder.Code != http.StatusOK || !strings.Contains(styleRecorder.Body.String(), "--accent:#7DD3FC") {
		t.Fatalf("style asset status/body = %d/%q", styleRecorder.Code, styleRecorder.Body.String())
	}
}

func TestHandlerRendersAwaitingAndInterruptedRuns(t *testing.T) {
	sylHome := t.TempDir()
	project := t.TempDir()
	if _, err := config.Init(project); err != nil {
		t.Fatal(err)
	}
	contents, err := json.Marshal([]registry.Entry{{Path: project}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registry.Path(sylHome), contents, 0o644); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(project, ".syl", "runs", "overview")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	state := runstate.State{
		Status: runstate.Running, Activity: runstate.AwaitingAnswer, Iteration: 2, MaxIterations: 3,
		Question: "Choose the deployment target", PID: os.Getpid(), Hostname: testHostname(t),
		StartedAt: time.Now().UTC(), Kind: runstate.Implement, TicketRef: "#181",
	}
	if err := runstate.Write(runstate.Path(runDir), state); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "metadata.txt"), []byte("Work root: /tmp/worktree\nImplementer harness: codex\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	marker, err := runmarker.Create(sylHome, project, runDir, "#181", os.Getpid(), testHostname(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = marker.Remove() })
	server, err := web.New(sylHome, 7777)
	if err != nil {
		t.Fatal(err)
	}

	body := serveOverview(t, server.Handler(), "127.0.0.1:7777")
	for _, expected := range []string{"Choose the deployment target", "/tmp/worktree", "ok", "awaiting-answer"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("awaiting Overview does not contain %q", expected)
		}
	}

	state.Activity = runstate.Implementing
	state.Question = ""
	state.PID = 999999
	if err := runstate.Write(runstate.Path(runDir), state); err != nil {
		t.Fatal(err)
	}
	body = serveOverview(t, server.Handler(), "localhost:7777")
	for _, expected := range []string{"Interrupted", "implementing", "#181"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("interrupted Overview does not contain %q", expected)
		}
	}
}

func TestServeStopsWhenContextIsCancelled(t *testing.T) {
	server, err := web.New(t.TempDir(), 7777)
	if err != nil {
		t.Fatal(err)
	}
	listener := &blockingListener{closed: make(chan struct{})}
	contextToCancel, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- server.Serve(contextToCancel, listener) }()
	cancel()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve() did not stop after context cancellation")
	}
}

type blockingListener struct {
	once   sync.Once
	closed chan struct{}
}

func (listener *blockingListener) Accept() (net.Conn, error) {
	if listener.closed == nil {
		listener.closed = make(chan struct{})
	}
	<-listener.closed
	return nil, errors.New("listener closed")
}

func (listener *blockingListener) Close() error {
	if listener.closed == nil {
		listener.closed = make(chan struct{})
	}
	listener.once.Do(func() { close(listener.closed) })
	return nil
}

func (*blockingListener) Addr() net.Addr { return fakeAddress("127.0.0.1:7777") }

type fakeAddress string

func (address fakeAddress) Network() string { return "tcp" }

func (address fakeAddress) String() string { return string(address) }

func serveOverview(t *testing.T, handler http.Handler, host string) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "http://"+host+"/", nil)
	request.Host = host
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("Overview status = %d; body = %q", recorder.Code, recorder.Body.String())
	}
	return recorder.Body.String()
}

func testHostname(t *testing.T) string {
	t.Helper()
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	return host
}
