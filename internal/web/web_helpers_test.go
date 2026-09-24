package web

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrochap/syl/internal/readmodel"
	"github.com/igorrochap/syl/internal/runstate"
)

func TestDisplayHelpersCoverOverviewStates(t *testing.T) {
	activityCases := []struct {
		name string
		run  readmodel.Run
		want string
	}{
		{name: "interrupted", run: readmodel.Run{Interrupted: true, Activity: "implementing"}, want: "Interrupted"},
		{name: "unknown", run: readmodel.Run{Unknown: true}, want: "Unknown"},
		{name: "activity", run: readmodel.Run{Activity: "reviewing"}, want: "reviewing"},
		{name: "status", run: readmodel.Run{Status: runstate.Running}, want: "running"},
	}
	for _, test := range activityCases {
		t.Run(test.name, func(t *testing.T) {
			if got := displayActivity(test.run); got != test.want {
				t.Fatalf("displayActivity() = %q, want %q", got, test.want)
			}
		})
	}

	for _, test := range []struct {
		name string
		run  readmodel.Run
		want string
	}{
		{name: "interrupted class", run: readmodel.Run{Interrupted: true}, want: "interrupted"},
		{name: "unknown class", run: readmodel.Run{Unknown: true}, want: "unknown"},
		{name: "running class", run: readmodel.Run{}, want: "running"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := activityClass(test.run); got != test.want {
				t.Fatalf("activityClass() = %q, want %q", got, test.want)
			}
		})
	}

	for _, test := range []struct {
		name   string
		health readmodel.Health
		want   string
	}{
		{name: "ok", health: readmodel.HealthOK, want: "pill-green"},
		{name: "invalid", health: readmodel.HealthInvalid, want: "pill-red"},
		{name: "missing", health: readmodel.HealthMissing, want: "pill-neutral"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := healthClass(test.health); got != test.want {
				t.Fatalf("healthClass() = %q, want %q", got, test.want)
			}
		})
	}

	for _, test := range []struct {
		harness string
		model   string
		want    string
	}{
		{harness: "", model: "", want: "unknown"},
		{harness: "", model: "model", want: "unknown · model"},
		{harness: "codex", model: "", want: "codex"},
		{harness: "codex", model: "model", want: "codex · model"},
	} {
		if got := displayHarness(test.harness, test.model); got != test.want {
			t.Fatalf("displayHarness(%q, %q) = %q, want %q", test.harness, test.model, got, test.want)
		}
	}

	if got := displayIteration(0, 0); got != "—" {
		t.Fatalf("displayIteration(0, 0) = %q, want em dash", got)
	}
	if got := displayIteration(2, 3); got != "2 / 3" {
		t.Fatalf("displayIteration(2, 3) = %q, want 2 / 3", got)
	}
	if got := displayKind(""); got != "Run" {
		t.Fatalf("displayKind(\"\") = %q, want Run", got)
	}
	if got := displayKind(runstate.Review); got != string(runstate.Review) {
		t.Fatalf("displayKind(review) = %q, want review", got)
	}
	if got := rowClass(readmodel.Run{}); got != "" {
		t.Fatalf("rowClass(live) = %q, want empty", got)
	}
	if got := rowClass(readmodel.Run{Interrupted: true}); got != "interrupted" {
		t.Fatalf("rowClass(interrupted) = %q, want interrupted", got)
	}
	if got := displayTicket(" "); got != "—" {
		t.Fatalf("displayTicket(blank) = %q, want em dash", got)
	}
	if got := displayTicket("#182"); got != "#182" {
		t.Fatalf("displayTicket(ticket) = %q, want #182", got)
	}
}

func TestDisplayStartedCoversRelativeAndAbsoluteTimes(t *testing.T) {
	now := time.Now()
	if got := displayStarted(time.Time{}); got != "unknown" {
		t.Fatalf("displayStarted(zero) = %q, want unknown", got)
	}
	if got := displayStarted(now.Add(-30 * time.Second)); got != "just now" {
		t.Fatalf("displayStarted(seconds ago) = %q, want just now", got)
	}
	if got := displayStarted(now.Add(-2 * time.Minute)); !strings.HasSuffix(got, " min ago") {
		t.Fatalf("displayStarted(minutes ago) = %q, want minutes", got)
	}
	if got := displayStarted(now.Add(-2 * time.Hour)); !strings.HasSuffix(got, " h ago") {
		t.Fatalf("displayStarted(hours ago) = %q, want hours", got)
	}
	started := now.Add(-48 * time.Hour)
	if got, want := displayStarted(started), started.Local().Format("2 Jan 2006, 15:04"); got != want {
		t.Fatalf("displayStarted(old) = %q, want %q", got, want)
	}
}

func TestHistoryDisplayHelpersCoverStatusesAndMissingValues(t *testing.T) {
	for _, test := range []struct {
		name string
		kind runstate.Kind
		want string
	}{
		{name: "missing kind", want: "—"},
		{name: "review kind", kind: runstate.Review, want: "review"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := historyKind(test.kind); got != test.want {
				t.Fatalf("historyKind() = %q, want %q", got, test.want)
			}
		})
	}

	for _, test := range []struct {
		name   string
		status string
		want   string
	}{
		{name: "running", status: "running", want: "running"},
		{name: "interrupted", status: "Interrupted", want: "red"},
		{name: "approved", status: "approved", want: "green"},
		{name: "exhausted", status: "exhausted", want: "plum"},
		{name: "completed", status: "completed", want: "completed"},
		{name: "unknown", status: "unknown", want: "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := historyStatusClass(test.status); got != test.want {
				t.Fatalf("historyStatusClass() = %q, want %q", got, test.want)
			}
		})
	}

	for _, test := range []struct {
		name string
		ref  string
		want string
	}{
		{name: "missing", want: "—"},
		{name: "numeric", ref: "#00182", want: "#182"},
		{name: "standalone text", ref: "release-candidate", want: "release-candidate"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := historyTicket(test.ref); got != test.want {
				t.Fatalf("historyTicket() = %q, want %q", got, test.want)
			}
		})
	}

	if got := historyTokens(readmodel.HistoryRun{}); got != "—" {
		t.Fatalf("historyTokens(missing) = %q, want em dash", got)
	}
	if got := historyTokens(readmodel.HistoryRun{TokensKnown: true, TotalTokens: 2_200_000}); got != "2.2M" {
		t.Fatalf("historyTokens(known) = %q, want 2.2M", got)
	}
	if got := displayDuration(readmodel.HistoryRun{}); got != "—" {
		t.Fatalf("displayDuration(missing) = %q, want em dash", got)
	}
	if got := displayDuration(readmodel.HistoryRun{DurationKnown: true, Duration: 61 * time.Second}); got != "1m" {
		t.Fatalf("displayDuration(61s) = %q, want 1m", got)
	}
	firstSeen := time.Date(2026, time.September, 2, 0, 0, 0, 0, time.UTC)
	if got := displayFirstSeen(firstSeen); got != "2 Sep 2026" {
		t.Fatalf("displayFirstSeen() = %q, want 2 Sep 2026", got)
	}
}

func TestDisplaySummaryCoversProjectHealthAndRunCounts(t *testing.T) {
	for _, test := range []struct {
		name    string
		project readmodel.Project
		want    string
	}{
		{name: "invalid", project: readmodel.Project{Health: readmodel.HealthInvalid}, want: "config fails to load · No live Runs"},
		{name: "uninitialized", project: readmodel.Project{Health: readmodel.HealthUninitialized}, want: "no .syl/config.toml · run syl init · No live Runs"},
		{name: "missing", project: readmodel.Project{Health: readmodel.HealthMissing}, want: "directory not found · No live Runs"},
		{name: "healthy", project: readmodel.Project{Health: readmodel.HealthOK}, want: "No live Runs"},
		{name: "healthy with runs", project: readmodel.Project{Health: readmodel.HealthOK, LiveRunCount: 1, InterruptedCount: 2}, want: "1 live · 2 interrupted"},
		{name: "invalid with runs", project: readmodel.Project{Health: readmodel.HealthInvalid, InterruptedCount: 1}, want: "config fails to load · 1 interrupted"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := displaySummary(test.project); got != test.want {
				t.Fatalf("displaySummary() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestHandlerRendersContentAndNotFound(t *testing.T) {
	server, err := New(t.TempDir(), 7777)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()

	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7777/overview/content", nil)
	request.Host = "127.0.0.1:7777"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `id="overview-content"`) {
		t.Fatalf("content status/body = %d/%q", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7777/missing", nil)
	request.Host = "127.0.0.1:7777"
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing route status = %d, want 404", recorder.Code)
	}
}

func TestHandlerRendersReadModelErrors(t *testing.T) {
	server, err := New(t.TempDir(), 7777)
	if err != nil {
		t.Fatal(err)
	}
	server.model = func() (readmodel.Overview, error) { return readmodel.Overview{}, errors.New("read failed") }
	handler := server.Handler()
	for _, path := range []string{"/", "/overview/content"} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7777"+path, nil)
			request.Host = "127.0.0.1:7777"
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusInternalServerError || recorder.Body.String() != "syl ui: read failed" {
				t.Fatalf("status/body = %d/%q", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestServeRejectsMissingListenerAndReturnsServeError(t *testing.T) {
	server, err := New(t.TempDir(), 7777)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Serve(context.Background(), nil); err == nil {
		t.Fatal("Serve(nil) error = nil, want missing-listener error")
	}

	listener := &errorListener{err: errors.New("accept failed")}
	if err := server.Serve(context.Background(), listener); err == nil || !strings.Contains(err.Error(), "accept failed") {
		t.Fatalf("Serve(error listener) error = %v, want accept failure", err)
	}
}

func TestServeReportsShutdownError(t *testing.T) {
	server, err := New(t.TempDir(), 7777)
	if err != nil {
		t.Fatal(err)
	}
	listener := &closeErrorListener{closed: make(chan struct{}), ready: make(chan struct{}), err: errors.New("close failed")}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- server.Serve(ctx, listener) }()
	<-listener.ready
	cancel()
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "shut down web server") {
			t.Fatalf("Serve() error = %v, want shutdown error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve() did not stop after context cancellation")
	}
}

type errorListener struct {
	err error
}

func (listener *errorListener) Accept() (net.Conn, error) { return nil, listener.err }
func (*errorListener) Close() error                       { return nil }
func (*errorListener) Addr() net.Addr                     { return testAddress("127.0.0.1:7777") }

type closeErrorListener struct {
	once      sync.Once
	readyOnce sync.Once
	closed    chan struct{}
	ready     chan struct{}
	err       error
}

func (listener *closeErrorListener) Accept() (net.Conn, error) {
	listener.readyOnce.Do(func() { close(listener.ready) })
	<-listener.closed
	return nil, errors.New("accept after close")
}

func (listener *closeErrorListener) Close() error {
	listener.once.Do(func() { close(listener.closed) })
	return listener.err
}

func (*closeErrorListener) Addr() net.Addr { return testAddress("127.0.0.1:7777") }

type testAddress string

func (address testAddress) Network() string { return "tcp" }
func (address testAddress) String() string  { return string(address) }
