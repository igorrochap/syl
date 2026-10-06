package web_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/configedit"
	"github.com/igorrochap/syl/internal/readmodel"
	"github.com/igorrochap/syl/internal/runrecord"
	"github.com/igorrochap/syl/internal/sylhome"
	"github.com/igorrochap/syl/internal/usage"
	"github.com/igorrochap/syl/internal/web"
)

const (
	testAlivePID = 12345
	testDeadPID  = 999999
)

func testProcessAlive(pid int) bool {
	return pid == testAlivePID
}

func TestHandlerProtectsConfigSaveWithTokenAndOrigin(t *testing.T) {
	sylHome := t.TempDir()
	project := t.TempDir()
	if _, err := config.Init(project); err != nil {
		t.Fatal(err)
	}
	writeWebRegistry(t, sylHome, sylhome.RegisteredProject{Path: project})
	server, err := newServer(t, sylHome)
	if err != nil {
		t.Fatal(err)
	}
	page := serveProject(t, server.Handler(), project, "/projects/config")
	if !strings.Contains(page, `name="roles.plan.sandbox"`) ||
		!strings.Contains(page, `name="roles.implement.sandbox"`) ||
		!strings.Contains(page, `name="roles.review.sandbox"`) {
		t.Fatalf("config page is missing a Role sandbox field: %q", page)
	}
	token := tokenFromPage(t, page)
	form := configForm(t, project, token)
	form.Del("token")
	if response := postMutation(t, server.Handler(), configSaveRoute(project), form, ""); response.Code != http.StatusForbidden {
		t.Fatalf("missing token status = %d, want forbidden", response.Code)
	}
	form.Set("token", token)
	if response := postMutation(t, server.Handler(), configSaveRoute(project), form, "http://evil.example:7777"); response.Code != http.StatusForbidden {
		t.Fatalf("foreign origin status = %d, want forbidden", response.Code)
	}
}

func TestConfigSaveKeepsSandboxWhenOlderFormOmitsIt(t *testing.T) {
	sylHome := t.TempDir()
	project := t.TempDir()
	if _, err := config.Init(project); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(project)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Roles.Review.SandboxMode = config.SandboxModeWorkspaceWrite
	if _, err := config.Write(project, loaded, config.OverwriteExisting); err != nil {
		t.Fatal(err)
	}
	writeWebRegistry(t, sylHome, sylhome.RegisteredProject{Path: project})
	server, err := newServer(t, sylHome)
	if err != nil {
		t.Fatal(err)
	}

	page := serveProject(t, server.Handler(), project, "/projects/config")
	form := configForm(t, project, tokenFromPage(t, page))
	form.Del("roles.plan.sandbox")
	form.Del("roles.implement.sandbox")
	form.Del("roles.review.sandbox")
	response := postMutation(t, server.Handler(), configSaveRoute(project), form, "http://localhost:7777")
	if response.Code != http.StatusSeeOther {
		t.Fatalf("config save status = %d, want redirect", response.Code)
	}

	after, err := config.Load(project)
	if err != nil {
		t.Fatal(err)
	}
	if after.Roles.Review.SandboxMode != config.SandboxModeWorkspaceWrite {
		t.Fatalf("saved review sandbox = %q, want workspace-write", after.Roles.Review.SandboxMode)
	}
}

func TestHandlerConfigRoutesCoverContentFeedbackAndBadRequests(t *testing.T) {
	sylHome := t.TempDir()
	project := t.TempDir()
	missingProject := filepath.Join(t.TempDir(), "missing-project")
	if _, err := config.Init(project); err != nil {
		t.Fatal(err)
	}
	writeWebRegistry(t, sylHome,
		sylhome.RegisteredProject{Path: project},
		sylhome.RegisteredProject{Path: missingProject},
	)
	server, err := newServer(t, sylHome)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()

	missingPath := httptest.NewRecorder()
	handler.ServeHTTP(missingPath, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7777/projects/config", nil))
	if missingPath.Code != http.StatusBadRequest {
		t.Fatalf("config page without path status = %d, want bad request", missingPath.Code)
	}

	unknownProject := httptest.NewRecorder()
	handler.ServeHTTP(unknownProject, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7777/projects/config?path="+url.QueryEscape(filepath.Join(t.TempDir(), "unknown")), nil))
	if unknownProject.Code != http.StatusNotFound {
		t.Fatalf("unknown config project status = %d, want not found", unknownProject.Code)
	}
	missingDirectory := serveProjectResponse(t, handler, missingProject, "/projects/config")
	if missingDirectory.Code != http.StatusNotFound {
		t.Fatalf("missing config Project status = %d, want not found", missingDirectory.Code)
	}

	content := serveProjectResponse(t, handler, project, "/projects/config/content")
	if content.Code != http.StatusOK {
		t.Fatalf("config fragment status = %d, want OK", content.Code)
	}

	page := serveProject(t, handler, project, "/projects/config")
	token := tokenFromPage(t, page)
	form := configForm(t, project, token)
	form.Set("loop.max_iterations", "6")
	redirect := postMutation(t, handler, configSaveRoute(project), form, "http://localhost:7777")
	if redirect.Code != http.StatusSeeOther || redirect.Header().Get("Location") != "/projects/config?path="+url.QueryEscape(project) {
		t.Fatalf("non-HTMX save = %d/%q, want redirect to Config tab", redirect.Code, redirect.Header().Get("Location"))
	}

	page = serveProject(t, handler, project, "/projects/config")
	token = tokenFromPage(t, page)
	form = configForm(t, project, token)
	validHX := rawMutation(t, handler, http.MethodPost, configSaveRoute(project), form.Encode(), token, true)
	if validHX.Code != http.StatusOK {
		t.Fatalf("HTMX config save status = %d, want OK", validHX.Code)
	}

	form = configForm(t, project, token)
	form.Set("loop.max_iterations", "0")
	invalidHX := rawMutation(t, handler, http.MethodPost, configSaveRoute(project), form.Encode(), token, true)
	if invalidHX.Code != http.StatusUnprocessableEntity {
		t.Fatalf("HTMX validation status = %d, want unprocessable entity", invalidHX.Code)
	}

	form = configForm(t, project, token)
	current, err := os.ReadFile(config.Path(project))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Path(project), append(current, []byte("\n# changed during request\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	conflictHX := rawMutation(t, handler, http.MethodPost, configSaveRoute(project), form.Encode(), token, true)
	if conflictHX.Code != http.StatusConflict {
		t.Fatalf("HTMX conflict status = %d, want conflict", conflictHX.Code)
	}

	missingSavePath := rawMutation(t, handler, http.MethodPost, "/projects/config/save", "", token, false)
	if missingSavePath.Code != http.StatusBadRequest {
		t.Fatalf("config save without path status = %d, want bad request", missingSavePath.Code)
	}
	malformedForm := rawMutation(t, handler, http.MethodPost, configSaveRoute(project), "%zz", token, false)
	if malformedForm.Code != http.StatusBadRequest {
		t.Fatalf("malformed config form status = %d, want bad request", malformedForm.Code)
	}
	unknownSave := rawMutation(t, handler, http.MethodPost, configSaveRoute(filepath.Join(t.TempDir(), "unknown")), "", token, false)
	if unknownSave.Code != http.StatusNotFound {
		t.Fatalf("unknown config save status = %d, want not found", unknownSave.Code)
	}
}

func TestHandlerServesConfigRouteStatusesForInvalidAndFixedConfig(t *testing.T) {
	sylHome := t.TempDir()
	project := filepath.Join(t.TempDir(), "invalid")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Init(project); err != nil {
		t.Fatal(err)
	}
	valid, err := os.ReadFile(config.Path(project))
	if err != nil {
		t.Fatal(err)
	}
	invalid := []byte(strings.Replace(string(valid), `effort = "xhigh"`, `effort = "<script>"`, 1))
	if err := os.WriteFile(config.Path(project), invalid, 0o644); err != nil {
		t.Fatal(err)
	}
	writeWebRegistry(t, sylHome, sylhome.RegisteredProject{Path: project})
	server, err := newServer(t, sylHome)
	if err != nil {
		t.Fatal(err)
	}

	for _, route := range []string{"/projects/config", "/projects/config/content"} {
		response := serveProjectResponse(t, server.Handler(), project, route)
		if response.Code != http.StatusOK {
			t.Fatalf("invalid config route %s status = %d, want OK", route, response.Code)
		}
	}

	if err := os.WriteFile(config.Path(project), valid, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"/projects/config", "/projects/config/content"} {
		response := serveProjectResponse(t, server.Handler(), project, route)
		if response.Code != http.StatusOK {
			t.Fatalf("fixed config route %s status = %d, want OK", route, response.Code)
		}
	}
}

func TestHandlerDoesNotPollEndedRunAndServesRawArtifacts(t *testing.T) {
	project := t.TempDir()
	runDir := filepath.Join(project, ".syl", "runs", "20260920T195033.518469000Z-173")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ended := time.Date(2026, time.September, 20, 20, 31, 0, 0, time.UTC)
	if err := runrecord.Write(runrecord.Path(runDir), runrecord.State{
		Status: runrecord.Approved, Iteration: 1, MaxIterations: 3,
		StartedAt: time.Date(2026, time.September, 20, 19, 50, 0, 0, time.UTC), EndedAt: &ended,
		Kind: runrecord.Implement, TicketRef: "#173",
	}); err != nil {
		t.Fatal(err)
	}
	writeWebArtifact(t, runDir, "metadata.txt", "Branch: feat/implementer-context-rollover\nBranch point: 30de5905ca30\nWork root: /worktree\nImplementer harness: codex\nReviewer harness: claude\n")
	writeWebArtifact(t, runDir, "sessions.txt", "iteration 1 implement: implement-session\niteration 1 review: review-session\n")
	writeWebArtifact(t, runDir, "summary.txt", "Iterations: 1\nFinal verdict: approve\nSummary: Ready for review\nDiff stat:\n file.go | 2 ++\n")
	writeWebArtifact(t, runDir, "iteration-01-implement.feed", "feed is plain text")
	writeWebArtifact(t, runDir, "iteration-01-verdict.txt", "VERDICT: approve\nSUMMARY: Ready for review\nFINDINGS:\n- [nit] file.go:1 — Consider a helper\n- [blocking] file.go:2 — Fix this\n")
	if err := usage.WriteArtifact(filepath.Join(runDir, "usage.json"), usage.Artifact{Entries: []usage.Entry{
		{Iteration: 1, Role: "implement", Harness: "codex", Model: "gpt-5.6", Tracked: true, Metrics: &usage.Metrics{InputTokens: 2000, OutputTokens: 80, TotalTokens: 2080}},
		{Iteration: 1, Role: "review", Harness: "claude", Model: "claude-sonnet", Tracked: true, Metrics: &usage.Metrics{InputTokens: 100, OutputTokens: 30, TotalTokens: 130}},
	}}); err != nil {
		t.Fatal(err)
	}

	server, err := newServer(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	body := serveRun(t, server.Handler(), runDir, "/runs")
	if strings.Contains(body, "hx-trigger=\"every 3s") {
		t.Fatal("ended Run page contains a polling trigger")
	}

	request := httptest.NewRequest(http.MethodGet, "/runs/artifact?path="+url.QueryEscape(runDir)+"&artifact=iteration-01-implement.feed", nil)
	request.Host = "127.0.0.1:7777"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "text/plain; charset=utf-8" || recorder.Body.String() != "feed is plain text" {
		t.Fatalf("artifact response = %d/%q/%q", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
	}
}

func TestHandlerRejectsInvalidRunRequests(t *testing.T) {
	server, err := newServer(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	missingRun := filepath.Join(t.TempDir(), "missing-run")
	tests := []struct {
		path string
		want int
	}{
		{path: "/runs/unknown", want: http.StatusNotFound},
		{path: "/runs", want: http.StatusBadRequest},
		{path: "/runs?path=" + url.QueryEscape(missingRun), want: http.StatusNotFound},
	}
	for _, test := range tests {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		request.Host = "127.0.0.1:7777"
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		if recorder.Code != test.want {
			t.Fatalf("Run request %q status = %d, want %d", test.path, recorder.Code, test.want)
		}
	}
}

func TestHandlerPollsOnlyRunningRunAndRefusesUnsafeArtifacts(t *testing.T) {
	project := t.TempDir()
	runDir := filepath.Join(project, ".syl", "runs", "running-173")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runrecord.Write(runrecord.Path(runDir), runrecord.State{
		Status: runrecord.Running, Activity: runrecord.Reviewing, Iteration: 1, MaxIterations: 3,
		PID: testAlivePID, Hostname: testHostname(t), StartedAt: time.Now().UTC(), Kind: runrecord.Implement, TicketRef: "#173",
	}); err != nil {
		t.Fatal(err)
	}
	writeWebArtifact(t, runDir, "metadata.txt", "Branch: feat/example\n")
	writeWebArtifact(t, runDir, "iteration-01-review.feed", "review")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	writeWebArtifact(t, filepath.Dir(outside), filepath.Base(outside), "secret")
	if err := os.Symlink(outside, filepath.Join(runDir, "outside-link")); err != nil {
		t.Fatal(err)
	}

	server, err := newServer(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	body := serveRun(t, server.Handler(), runDir, "/runs")
	if !strings.Contains(body, "hx-trigger=\"every 3s [document.visibilityState === 'visible']\"") {
		t.Fatalf("running Run body = %s, want polling trigger", body)
	}

	for _, artifact := range []string{"../../config.toml", "/tmp/config.toml", "outside-link"} {
		request := httptest.NewRequest(http.MethodGet, "/runs/artifact?path="+url.QueryEscape(runDir)+"&artifact="+url.QueryEscape(artifact), nil)
		request.Host = "127.0.0.1:7777"
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		if recorder.Code == http.StatusOK || strings.Contains(recorder.Body.String(), "secret") {
			t.Fatalf("unsafe artifact %q response = %d/%q", artifact, recorder.Code, recorder.Body.String())
		}
	}

	for _, route := range []string{"/runs", "/runs/content"} {
		request := httptest.NewRequest(http.MethodGet, route+"?path="+url.QueryEscape(filepath.Dir(outside)), nil)
		request.Host = "127.0.0.1:7777"
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		if recorder.Code == http.StatusOK {
			t.Fatalf("unsafe Run path on %s returned OK: %q", route, recorder.Body.String())
		}
	}
}

func TestHandlerDismissesInterruptedRunWithoutChangingRunFiles(t *testing.T) {
	sylHome, runDir := writeMutationFixture(t, false)
	server, err := newServer(t, sylHome)
	if err != nil {
		t.Fatal(err)
	}
	beforeRun := snapshotFiles(t, runDir)
	beforeHome := snapshotFiles(t, sylHome)
	token := tokenFromPage(t, serveOverview(t, server.Handler()))

	response := postMutation(t, server.Handler(), "/runs/dismiss", url.Values{
		"run_dir": {runDir}, "token": {token},
	}, "")
	if response.Code != http.StatusSeeOther {
		t.Fatalf("dismiss status = %d, want redirect; body = %q", response.Code, response.Body.String())
	}
	markers, err := openSylHome(t, sylHome).LiveRuns()
	if err != nil {
		t.Fatal(err)
	}
	if len(markers) != 0 {
		t.Fatalf("markers = %#v, want marker removed", markers)
	}
	if after := snapshotFiles(t, runDir); !reflect.DeepEqual(after, beforeRun) {
		t.Fatalf("Run files changed: before %#v, after %#v", beforeRun, after)
	}
	if after := snapshotFiles(t, sylHome); reflect.DeepEqual(after, beforeHome) {
		t.Fatal("syl home did not change after dismissing marker")
	}
	overview, err := readmodel.ReadOverview(openSylHome(t, sylHome))
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.AwaitingAnswer)+len(overview.LiveRuns) != 0 {
		t.Fatalf("Overview still contains dismissed Runs: %#v", overview)
	}
}

func TestHandlerRefusesDismissForLiveRun(t *testing.T) {
	sylHome, runDir := writeMutationFixture(t, true)
	server, err := newServer(t, sylHome)
	if err != nil {
		t.Fatal(err)
	}
	beforeRun := snapshotFiles(t, runDir)
	beforeHome := snapshotFiles(t, sylHome)
	token := tokenFromPage(t, serveOverview(t, server.Handler()))

	response := postMutation(t, server.Handler(), "/runs/dismiss", url.Values{
		"run_dir": {runDir}, "token": {token},
	}, "")
	if response.Code != http.StatusConflict {
		t.Fatalf("dismiss live status = %d, want conflict", response.Code)
	}
	if after := snapshotFiles(t, runDir); !reflect.DeepEqual(after, beforeRun) {
		t.Fatalf("live Run files changed: before %#v, after %#v", beforeRun, after)
	}
	if after := snapshotFiles(t, sylHome); !reflect.DeepEqual(after, beforeHome) {
		t.Fatalf("syl home changed after refusing live Run: before %#v, after %#v", beforeHome, after)
	}
}

func TestHandlerRejectsMutationWithoutTokenWrongTokenAndForeignOrigin(t *testing.T) {
	sylHome, runDir := writeMutationFixture(t, false)
	server, err := newServer(t, sylHome)
	if err != nil {
		t.Fatal(err)
	}
	token := tokenFromPage(t, serveOverview(t, server.Handler()))
	beforeRun := snapshotFiles(t, runDir)
	beforeHome := snapshotFiles(t, sylHome)
	for _, test := range []struct {
		name   string
		token  string
		origin string
	}{
		{name: "missing token"},
		{name: "wrong token", token: "wrong-token"},
		{name: "foreign origin", token: token, origin: "http://evil.example:7777"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := postMutation(t, server.Handler(), "/runs/dismiss", url.Values{
				"run_dir": {runDir}, "token": {test.token},
			}, test.origin)
			if response.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want forbidden", response.Code)
			}
			if after := snapshotFiles(t, runDir); !reflect.DeepEqual(after, beforeRun) {
				t.Fatalf("Run files changed: before %#v, after %#v", beforeRun, after)
			}
			if after := snapshotFiles(t, sylHome); !reflect.DeepEqual(after, beforeHome) {
				t.Fatalf("syl home changed: before %#v, after %#v", beforeHome, after)
			}
		})
	}
}

func TestHandlerAcceptsItsOwnOrigin(t *testing.T) {
	sylHome, runDir := writeMutationFixture(t, false)
	server, err := newServer(t, sylHome)
	if err != nil {
		t.Fatal(err)
	}
	token := tokenFromPage(t, serveOverview(t, server.Handler()))

	response := postMutation(t, server.Handler(), "/runs/dismiss", url.Values{
		"run_dir": {runDir}, "token": {token},
	}, "http://localhost:7777")
	if response.Code != http.StatusSeeOther {
		t.Fatalf("same-origin dismiss status = %d, want redirect", response.Code)
	}
}

func TestHandlerRejectsMalformedAndIncompleteMutations(t *testing.T) {
	server, err := newServer(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	token := tokenFromPage(t, serveOverview(t, server.Handler()))
	for _, test := range []struct {
		name  string
		route string
		body  string
		want  int
	}{
		{name: "dismiss malformed form", route: "/runs/dismiss", body: "%", want: http.StatusBadRequest},
		{name: "dismiss missing run", route: "/runs/dismiss", body: "token=", want: http.StatusBadRequest},
		{name: "forget malformed form", route: "/projects/forget", body: "%", want: http.StatusBadRequest},
		{name: "forget missing project", route: "/projects/forget", body: "token=", want: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := rawMutation(t, server.Handler(), http.MethodPost, test.route, test.body, token, false)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d; body = %q", response.Code, test.want, response.Body.String())
			}
		})
	}

	response := rawMutation(t, server.Handler(), http.MethodGet, "/runs/dismiss", "", token, false)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET dismiss status = %d, want method not allowed", response.Code)
	}
}

func TestHandlerReportsDismissStateReadFailure(t *testing.T) {
	sylHome, runDir := writeMutationFixture(t, false)
	if err := os.WriteFile(runrecord.Path(runDir), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	server, err := newServer(t, sylHome)
	if err != nil {
		t.Fatal(err)
	}
	token := tokenFromPage(t, serveOverview(t, server.Handler()))
	response := rawMutation(t, server.Handler(), http.MethodPost, "/runs/dismiss", url.Values{
		"run_dir": {runDir},
	}.Encode(), token, false)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("dismiss corrupt-state status = %d, want internal server error", response.Code)
	}
}

func TestHandlerReportsForgetPathFailure(t *testing.T) {
	server, err := newServer(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	token := tokenFromPage(t, serveOverview(t, server.Handler()))
	response := rawMutation(t, server.Handler(), http.MethodPost, "/projects/forget", url.Values{
		"path": {"\x00"},
	}.Encode(), token, false)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("forget invalid-path status = %d, want internal server error", response.Code)
	}
}

func TestHandlerReturnsOverviewFragmentForHXMutation(t *testing.T) {
	sylHome, runDir := writeMutationFixture(t, false)
	server, err := newServer(t, sylHome)
	if err != nil {
		t.Fatal(err)
	}
	token := tokenFromPage(t, serveOverview(t, server.Handler()))
	response := rawMutation(t, server.Handler(), http.MethodPost, "/runs/dismiss", url.Values{
		"run_dir": {runDir},
	}.Encode(), token, true)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("HX dismiss status/Content-Type = %d/%q, want 200/html", response.Code, response.Header().Get("Content-Type"))
	}
}

func TestHandlerForgetsProjectWithoutChangingProjectFiles(t *testing.T) {
	sylHome := t.TempDir()
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".syl"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".syl", "config.toml"), []byte("invalid = [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	projectFile := filepath.Join(project, "untouched.txt")
	if err := os.WriteFile(projectFile, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeWebRegistry(t, sylHome, sylhome.RegisteredProject{Path: project})
	server, err := newServer(t, sylHome)
	if err != nil {
		t.Fatal(err)
	}
	beforeProject := snapshotFiles(t, project)
	token := tokenFromPage(t, serveOverview(t, server.Handler()))

	response := postMutation(t, server.Handler(), "/projects/forget", url.Values{
		"path": {project}, "token": {token},
	}, "")
	if response.Code != http.StatusSeeOther {
		t.Fatalf("forget status = %d, want redirect; body = %q", response.Code, response.Body.String())
	}
	entries, err := openSylHome(t, sylHome).Projects()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("registry entries = %#v, want forgotten Project removed", entries)
	}
	if after := snapshotFiles(t, project); !reflect.DeepEqual(after, beforeProject) {
		t.Fatalf("Project files changed: before %#v, after %#v", beforeProject, after)
	}
}

func TestHandlerProjectRejectsUnknownProjectAndMissingPath(t *testing.T) {
	server, err := newServer(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		path string
		want int
	}{
		{name: "missing path", path: "/projects", want: http.StatusBadRequest},
		{name: "unknown project", path: "/projects?path=" + url.QueryEscape(t.TempDir()), want: http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7777"+test.path, nil)
			request.Host = "127.0.0.1:7777"
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, request)
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d; body = %q", recorder.Code, test.want, recorder.Body.String())
			}
		})
	}
}

func TestHandlerServesOverviewAndEmbeddedAssets(t *testing.T) {
	server, err := newServer(t, t.TempDir())
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
	assetRequest := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7777/assets/htmx.min.js", nil)
	assetRequest.Host = "127.0.0.1:7777"
	assetRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(assetRecorder, assetRequest)
	if assetRecorder.Code != http.StatusOK {
		t.Fatalf("htmx asset status = %d, want OK", assetRecorder.Code)
	}
	styleRequest := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7777/assets/style.css", nil)
	styleRequest.Host = "127.0.0.1:7777"
	styleRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(styleRecorder, styleRequest)
	if styleRecorder.Code != http.StatusOK {
		t.Fatalf("style asset status = %d, want OK", styleRecorder.Code)
	}
}

func TestServeStopsWhenContextIsCancelled(t *testing.T) {
	server, err := newServer(t, t.TempDir())
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

func serveOverview(t *testing.T, handler http.Handler) string {
	t.Helper()
	host := "127.0.0.1:7777"
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

func writeWebRegistry(t *testing.T, sylHome string, entries ...sylhome.RegisteredProject) {
	t.Helper()
	contents, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sylHome, "projects.json"), contents, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeWebRun(t *testing.T, project, name string, state runrecord.State) {
	t.Helper()
	runDir := filepath.Join(project, ".syl", "runs", name)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runrecord.Write(runrecord.Path(runDir), state); err != nil {
		t.Fatal(err)
	}
}

func serveProject(t *testing.T, handler http.Handler, projectPath, route string) string {
	t.Helper()
	recorder := serveProjectResponse(t, handler, projectPath, route)
	if recorder.Code != http.StatusOK {
		t.Fatalf("Project status = %d; body = %q", recorder.Code, recorder.Body.String())
	}
	return recorder.Body.String()
}

func serveProjectResponse(t *testing.T, handler http.Handler, projectPath, route string) *httptest.ResponseRecorder {
	t.Helper()
	requestURL := "http://127.0.0.1:7777" + route + "?path=" + url.QueryEscape(projectPath)
	request := httptest.NewRequest(http.MethodGet, requestURL, nil)
	request.Host = "127.0.0.1:7777"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func configSaveRoute(projectPath string) string {
	return "/projects/config/save?path=" + url.QueryEscape(projectPath)
}

func configForm(t *testing.T, projectPath, token string) url.Values {
	t.Helper()
	snapshot, err := configedit.Load(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{}
	form.Set("token", token)
	form.Set("version", snapshot.Version)
	form.Set("tracker.issues", snapshot.Values.TrackerIssues)
	form.Set("tracker.reviews", snapshot.Values.TrackerReviews)
	setRoleForm(form, "plan", snapshot.Values.Plan)
	setRoleForm(form, "implement", snapshot.Values.Implement)
	setRoleForm(form, "review", snapshot.Values.Review)
	form.Set("loop.max_iterations", strconv.Itoa(snapshot.Values.MaxIterations))
	if snapshot.Values.NotificationsEnabled {
		form.Set("notifications.enabled", "true")
	}
	form.Set("worktree.root", snapshot.Values.WorktreeRoot)
	form.Set("worktree.setup", snapshot.Values.WorktreeSetup)
	for _, path := range snapshot.Values.WorktreeCopy {
		form.Add("worktree.copy", path)
	}
	return form
}

func setRoleForm(form url.Values, name string, values configedit.RoleValues) {
	prefix := "roles." + name + "."
	form.Set(prefix+"harness", values.Harness)
	form.Set(prefix+"model", values.Model)
	form.Set(prefix+"effort", values.Effort)
	form.Set(prefix+"sandbox", values.SandboxMode)
	if values.MCP {
		form.Set(prefix+"mcp", "true")
	}
}

func serveRun(t *testing.T, handler http.Handler, runDir, route string) string {
	t.Helper()
	requestURL := "http://127.0.0.1:7777" + route + "?path=" + url.QueryEscape(runDir)
	request := httptest.NewRequest(http.MethodGet, requestURL, nil)
	request.Host = "127.0.0.1:7777"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("Run status = %d; body = %q", recorder.Code, recorder.Body.String())
	}
	return recorder.Body.String()
}

func writeWebArtifact(t *testing.T, directory, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeMutationFixture(t *testing.T, live bool) (string, string) {
	t.Helper()
	sylHome := t.TempDir()
	project := t.TempDir()
	if _, err := config.Init(project); err != nil {
		t.Fatal(err)
	}
	writeWebRegistry(t, sylHome, sylhome.RegisteredProject{Path: project})
	runDir := filepath.Join(project, ".syl", "runs", "run-183")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pid := testDeadPID
	if live {
		pid = testAlivePID
	}
	state := runrecord.State{
		Status: runrecord.Running, Activity: runrecord.Implementing, PID: pid,
		Hostname: testHostname(t), StartedAt: time.Now().UTC(), Kind: runrecord.Implement, TicketRef: "#183",
	}
	if err := runrecord.Write(runrecord.Path(runDir), state); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "metadata.txt"), []byte("Work root: /worktree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	createLiveRun(t, sylHome, project, runDir, state.TicketRef, state.PID, state.Hostname)
	return sylHome, runDir
}

func newServer(t *testing.T, path string) (*web.Server, error) {
	t.Helper()
	return web.NewWithProcessLiveness(openSylHome(t, path), 7777, testProcessAlive)
}

func openSylHome(t *testing.T, path string) sylhome.Dir {
	t.Helper()
	dir, err := sylhome.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func createLiveRun(t *testing.T, sylHome, project, runDir, ticket string, pid int, host string) {
	t.Helper()
	run, err := openSylHome(t, sylHome).MarkLive(sylhome.LiveRun{
		ProjectPath: project,
		RunDir:      runDir,
		TicketRef:   ticket,
		Host:        host,
		PID:         pid,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Unmark() })
}

func tokenFromPage(t *testing.T, body string) string {
	t.Helper()
	match := regexp.MustCompile(`name="syl-token" content="([^"]+)"`).FindStringSubmatch(body)
	if len(match) != 2 || match[1] == "" {
		t.Fatalf("page does not contain a UI token: %q", body)
	}
	return match[1]
}

func postMutation(t *testing.T, handler http.Handler, route string, values url.Values, origin string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:7777"+route, strings.NewReader(values.Encode()))
	request.Host = "127.0.0.1:7777"
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func rawMutation(t *testing.T, handler http.Handler, method, route, body, token string, hx bool) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "http://127.0.0.1:7777"+route, strings.NewReader(body))
	request.Host = "127.0.0.1:7777"
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-Syl-Token", token)
	if hx {
		request.Header.Set("HX-Request", "true")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func snapshotFiles(t *testing.T, root string) map[string][]byte {
	t.Helper()
	snapshot := map[string][]byte{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		snapshot[relative] = contents
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return snapshot
}
