package web_test

import (
	"context"
	"encoding/json"
	"errors"
	"html"
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
	"github.com/igorrochap/syl/internal/registry"
	"github.com/igorrochap/syl/internal/runmarker"
	"github.com/igorrochap/syl/internal/runstate"
	"github.com/igorrochap/syl/internal/usage"
	"github.com/igorrochap/syl/internal/web"
)

func TestHandlerRendersStructuredConfigForm(t *testing.T) {
	sylHome := t.TempDir()
	project := t.TempDir()
	if _, err := config.Init(project); err != nil {
		t.Fatal(err)
	}
	writeWebRegistry(t, sylHome, registry.Entry{Path: project})
	server, err := web.New(sylHome, 7777)
	if err != nil {
		t.Fatal(err)
	}
	body := serveProject(t, server.Handler(), project, "/projects/config")
	for _, field := range []string{
		`name="tracker.issues"`, `name="tracker.reviews"`,
		`name="roles.plan.harness"`, `name="roles.plan.model"`, `name="roles.plan.effort"`, `name="roles.plan.mcp"`,
		`name="roles.implement.harness"`, `name="roles.implement.model"`, `name="roles.implement.effort"`, `name="roles.implement.mcp"`,
		`name="roles.review.harness"`, `name="roles.review.model"`, `name="roles.review.effort"`, `name="roles.review.mcp"`,
		`name="loop.max_iterations"`, `name="notifications.enabled"`, `name="worktree.root"`, `name="worktree.setup"`,
		`name="worktree.copy"`, "Saving rewrites", "Comments you added by hand are not kept.", "Codex ignores this",
		`data-mcp-harness="implement"`, `data-mcp-role="implement"`, `data-mcp-value`,
		"document.addEventListener('change'", "updateSaveState", "save.disabled=hasErrors",
	} {
		if !strings.Contains(body, field) {
			t.Fatalf("config form does not contain %q: %s", field, body)
		}
	}
	if strings.Contains(body, "Changes apply to the next Run") {
		t.Fatal("config form shows a live-Run banner without a live Run")
	}
	for _, option := range append(append(configedit.TrackerOptions(), configedit.HarnessOptions()...), configedit.EffortOptions()...) {
		if !strings.Contains(body, `value="`+option+`"`) {
			t.Fatalf("config form does not contain option %q", option)
		}
	}
}

func TestHandlerShowsLiveRunBannerOnConfigForm(t *testing.T) {
	sylHome := t.TempDir()
	project := t.TempDir()
	if _, err := config.Init(project); err != nil {
		t.Fatal(err)
	}
	writeWebRegistry(t, sylHome, registry.Entry{Path: project})
	runDir := filepath.Join(project, ".syl", "runs", "live-config")
	writeWebRun(t, project, filepath.Base(runDir), runstate.State{
		Status: runstate.Running, PID: os.Getpid(), Hostname: testHostname(t), Kind: runstate.Implement,
		StartedAt: time.Now().UTC(),
	})
	if _, err := runmarker.Create(sylHome, project, runDir, "#186", os.Getpid(), testHostname(t)); err != nil {
		t.Fatal(err)
	}
	server, err := web.New(sylHome, 7777)
	if err != nil {
		t.Fatal(err)
	}
	body := serveProject(t, server.Handler(), project, "/projects/config")
	if !strings.Contains(body, "1 Run is live in this project") || !strings.Contains(body, "Changes apply to the next Run") {
		t.Fatalf("config form = %s, want live-Run banner", body)
	}
}

func TestHandlerSavesValidConfigAndLoadsItBack(t *testing.T) {
	sylHome := t.TempDir()
	project := t.TempDir()
	if _, err := config.Init(project); err != nil {
		t.Fatal(err)
	}
	writeWebRegistry(t, sylHome, registry.Entry{Path: project})
	server, err := web.New(sylHome, 7777)
	if err != nil {
		t.Fatal(err)
	}
	page := serveProject(t, server.Handler(), project, "/projects/config")
	form := configForm(t, project, tokenFromPage(t, page))
	form.Set("loop.max_iterations", "6")
	response := postMutation(t, server.Handler(), configSaveRoute(project), form, "http://localhost:7777")
	if response.Code != http.StatusSeeOther {
		t.Fatalf("save status = %d; body = %q", response.Code, response.Body.String())
	}
	loaded, err := config.Load(project)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Loop.MaxIterations != 6 {
		t.Fatalf("loaded max_iterations = %d, want 6", loaded.Loop.MaxIterations)
	}
}

func TestHandlerShowsValidationErrorWithoutWriting(t *testing.T) {
	sylHome := t.TempDir()
	project := t.TempDir()
	if _, err := config.Init(project); err != nil {
		t.Fatal(err)
	}
	writeWebRegistry(t, sylHome, registry.Entry{Path: project})
	server, err := web.New(sylHome, 7777)
	if err != nil {
		t.Fatal(err)
	}
	page := serveProject(t, server.Handler(), project, "/projects/config")
	form := configForm(t, project, tokenFromPage(t, page))
	form.Set("loop.max_iterations", "0")
	before, err := os.ReadFile(config.Path(project))
	if err != nil {
		t.Fatal(err)
	}
	response := postMutation(t, server.Handler(), configSaveRoute(project), form, "")
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "loop.max_iterations must be positive; got 0") {
		t.Fatalf("validation response = %d/%q", response.Code, response.Body.String())
	}
	after, err := os.ReadFile(config.Path(project))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("invalid config save changed the file")
	}
}

func TestHandlerRefusesConfigConflictAndKeepsDiskChange(t *testing.T) {
	sylHome := t.TempDir()
	project := t.TempDir()
	if _, err := config.Init(project); err != nil {
		t.Fatal(err)
	}
	writeWebRegistry(t, sylHome, registry.Entry{Path: project})
	server, err := web.New(sylHome, 7777)
	if err != nil {
		t.Fatal(err)
	}
	page := serveProject(t, server.Handler(), project, "/projects/config")
	form := configForm(t, project, tokenFromPage(t, page))
	external, err := os.ReadFile(config.Path(project))
	if err != nil {
		t.Fatal(err)
	}
	external = append(external, []byte("\n# changed by editor\n")...)
	if err := os.WriteFile(config.Path(project), external, 0o644); err != nil {
		t.Fatal(err)
	}
	form.Set("loop.max_iterations", "9")
	response := postMutation(t, server.Handler(), configSaveRoute(project), form, "")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "changed on disk") || !strings.Contains(response.Body.String(), "Reload config") {
		t.Fatalf("conflict response = %d/%q", response.Code, response.Body.String())
	}
	loaded, err := config.Load(project)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Loop.MaxIterations != 3 || !strings.Contains(string(external), "changed by editor") {
		t.Fatal("conflicting save did not preserve the on-disk change")
	}
}

func TestHandlerProtectsConfigSaveWithTokenAndOrigin(t *testing.T) {
	sylHome := t.TempDir()
	project := t.TempDir()
	if _, err := config.Init(project); err != nil {
		t.Fatal(err)
	}
	writeWebRegistry(t, sylHome, registry.Entry{Path: project})
	server, err := web.New(sylHome, 7777)
	if err != nil {
		t.Fatal(err)
	}
	page := serveProject(t, server.Handler(), project, "/projects/config")
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

func TestHandlerConfigRoutesCoverContentFeedbackAndBadRequests(t *testing.T) {
	sylHome := t.TempDir()
	project := t.TempDir()
	invalidProject := t.TempDir()
	if _, err := config.Init(project); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(invalidProject, ".syl"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Path(invalidProject), []byte("[tracker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeWebRegistry(t, sylHome, registry.Entry{Path: project}, registry.Entry{Path: invalidProject})
	server, err := web.New(sylHome, 7777)
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

	content := serveProject(t, handler, project, "/projects/config/content")
	if !strings.Contains(content, `name="loop.max_iterations"`) {
		t.Fatalf("config content does not contain form fields: %s", content)
	}

	invalidResponse := serveProjectResponse(t, handler, invalidProject, "/projects/config")
	if invalidResponse.Code != http.StatusOK || !strings.Contains(invalidResponse.Body.String(), "This config fails to load") {
		t.Fatalf("invalid config page = %d/%q, want read-only invalid view", invalidResponse.Code, invalidResponse.Body.String())
	}

	page := serveProject(t, handler, project, "/projects/config")
	token := tokenFromPage(t, page)
	form := configForm(t, project, token)
	validHX := rawMutation(t, handler, http.MethodPost, configSaveRoute(project), form.Encode(), token, true)
	if validHX.Code != http.StatusOK || !strings.Contains(validHX.Body.String(), `name="loop.max_iterations"`) {
		t.Fatalf("HTMX config save = %d/%q, want rendered config content", validHX.Code, validHX.Body.String())
	}

	form = configForm(t, project, token)
	form.Set("loop.max_iterations", "0")
	invalidHX := rawMutation(t, handler, http.MethodPost, configSaveRoute(project), form.Encode(), token, true)
	if invalidHX.Code != http.StatusUnprocessableEntity || !strings.Contains(invalidHX.Body.String(), "loop.max_iterations must be positive; got 0") {
		t.Fatalf("HTMX validation response = %d/%q", invalidHX.Code, invalidHX.Body.String())
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
	if conflictHX.Code != http.StatusConflict || !strings.Contains(conflictHX.Body.String(), "Reload config") {
		t.Fatalf("HTMX conflict response = %d/%q", conflictHX.Code, conflictHX.Body.String())
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

func TestHandlerRendersInvalidConfigAsReadOnlySource(t *testing.T) {
	sylHome := t.TempDir()
	project := t.TempDir()
	if _, err := config.Init(project); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(config.Path(project))
	if err != nil {
		t.Fatal(err)
	}
	contents = []byte(strings.Replace(string(contents), `effort = "xhigh"`, `effort = "ultra"`, 1))
	if err := os.WriteFile(config.Path(project), contents, 0o644); err != nil {
		t.Fatal(err)
	}
	writeWebRegistry(t, sylHome, registry.Entry{Path: project})
	server, err := web.New(sylHome, 7777)
	if err != nil {
		t.Fatal(err)
	}

	body := serveProject(t, server.Handler(), project, "/projects/config")
	wantError := `roles.implement.effort: invalid value "ultra"; want low, medium, high, or xhigh`
	bodyText := html.UnescapeString(body)
	for _, expected := range []string{
		wantError,
		config.Path(project),
		`data-copy-path="` + config.Path(project) + `"`,
		`class="config-source-line marked"`,
		`hx-get="/projects/config/content?path=`,
		`href="/projects?path=`,
	} {
		if !strings.Contains(bodyText, expected) {
			t.Fatalf("invalid config body does not contain %q: %s", expected, body)
		}
	}
	if strings.Contains(body, "<form") || strings.Contains(body, "<textarea") || strings.Contains(body, "contenteditable") {
		t.Fatalf("invalid config rendered a raw editor control: %s", body)
	}
}

func TestHandlerRendersMalformedConfigWithoutMarkedLine(t *testing.T) {
	sylHome := t.TempDir()
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(config.Path(project)), 0o755); err != nil {
		t.Fatal(err)
	}
	contents := "[tracker\nissues = \"local\"\n"
	if err := os.WriteFile(config.Path(project), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	_, loadErr := config.Load(project)
	if loadErr == nil {
		t.Fatal("config.Load() succeeded for malformed TOML")
	}
	writeWebRegistry(t, sylHome, registry.Entry{Path: project})
	server, err := web.New(sylHome, 7777)
	if err != nil {
		t.Fatal(err)
	}

	body := serveProject(t, server.Handler(), project, "/projects/config")
	if !strings.Contains(html.UnescapeString(body), loadErr.Error()) {
		t.Fatalf("malformed config body does not contain exact load error %q: %s", loadErr, body)
	}
	if strings.Contains(body, `class="config-source-line marked"`) {
		t.Fatalf("malformed config marked a source line: %s", body)
	}
}

func TestHandlerEscapesInvalidConfigSourceAndLoadsFormAfterFix(t *testing.T) {
	sylHome := t.TempDir()
	project := t.TempDir()
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
	writeWebRegistry(t, sylHome, registry.Entry{Path: project})
	server, err := web.New(sylHome, 7777)
	if err != nil {
		t.Fatal(err)
	}

	body := serveProject(t, server.Handler(), project, "/projects/config")
	if !strings.Contains(body, "&lt;script&gt;") || strings.Contains(body, `effort = "<script>"`) {
		t.Fatalf("invalid config source was not escaped as text: %s", body)
	}

	if err := os.WriteFile(config.Path(project), valid, 0o644); err != nil {
		t.Fatal(err)
	}
	content := serveProjectResponse(t, server.Handler(), project, "/projects/config/content")
	if content.Code != http.StatusOK || !strings.Contains(content.Body.String(), `name="roles.implement.effort"`) || strings.Contains(content.Body.String(), "This config fails to load") {
		t.Fatalf("fixed config content = %d/%q, want structured form", content.Code, content.Body.String())
	}
}

func TestHandlerShowsUninitializedConfigAndKeepsRunHistoryAvailable(t *testing.T) {
	sylHome := t.TempDir()
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".syl", "runs", "run-187"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runstate.Write(runstate.Path(filepath.Join(project, ".syl", "runs", "run-187")), runstate.State{
		Status: runstate.Approved, TicketRef: "#187", Kind: runstate.Implement,
	}); err != nil {
		t.Fatal(err)
	}
	writeWebRegistry(t, sylHome, registry.Entry{Path: project})
	server, err := web.New(sylHome, 7777)
	if err != nil {
		t.Fatal(err)
	}

	body := serveProject(t, server.Handler(), project, "/projects/config")
	for _, expected := range []string{
		"No <code>.syl/config.toml</code> exists",
		"syl init",
		"href=\"/projects?path=",
		"Runs",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("uninitialized config body does not contain %q: %s", expected, body)
		}
	}
	runs := serveProject(t, server.Handler(), project, "/projects")
	if !strings.Contains(runs, "#187") {
		t.Fatalf("uninitialized project runs body does not contain history: %s", runs)
	}
}

func TestHandlerRendersProjectHistoryAndConfigMetadata(t *testing.T) {
	sylHome := t.TempDir()
	project := t.TempDir()
	if _, err := config.Init(project); err != nil {
		t.Fatal(err)
	}
	firstSeen := time.Date(2026, time.September, 2, 0, 0, 0, 0, time.UTC)
	writeWebRegistry(t, sylHome, registry.Entry{Path: project, FirstSeen: firstSeen})
	runDir := filepath.Join(project, ".syl", "runs", "20260924T120000.000000000Z-184")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ended := time.Date(2026, time.September, 24, 12, 1, 0, 0, time.UTC)
	if err := runstate.Write(runstate.Path(runDir), runstate.State{
		Status: runstate.Approved, Iteration: 1, MaxIterations: 3,
		StartedAt: time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC), EndedAt: &ended,
		Kind: runstate.Implement, TicketRef: "#184",
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "metadata.txt"), []byte("Implementer harness: codex\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "summary.txt"), []byte("Final verdict: approve\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := usage.WriteArtifact(filepath.Join(runDir, "usage.json"), usage.Artifact{Entries: []usage.Entry{{
		Tracked: true, Metrics: &usage.Metrics{TotalTokens: 2_200_000},
	}}}); err != nil {
		t.Fatal(err)
	}

	server, err := web.New(sylHome, 7777)
	if err != nil {
		t.Fatal(err)
	}
	body := serveProject(t, server.Handler(), project, "/projects")
	for _, expected := range []string{
		filepath.Base(project), project, "ok", "issues: github", "reviews: local", "first seen 2 Sep 2026",
		"Runs", "Config", "#184", "implement", "approved", "1 / 3", "approve", "1m", "2.2M",
		"/runs?path=", `document.visibilityState === 'visible'`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("Project body does not contain %q: %s", expected, body)
		}
	}
}

func TestHandlerRendersRunPageAndRawArtifacts(t *testing.T) {
	project := t.TempDir()
	runDir := filepath.Join(project, ".syl", "runs", "20260920T195033.518469000Z-173")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ended := time.Date(2026, time.September, 20, 20, 31, 0, 0, time.UTC)
	if err := runstate.Write(runstate.Path(runDir), runstate.State{
		Status: runstate.Approved, Iteration: 1, MaxIterations: 3,
		StartedAt: time.Date(2026, time.September, 20, 19, 50, 0, 0, time.UTC), EndedAt: &ended,
		Kind: runstate.Implement, TicketRef: "#173",
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

	server, err := web.New(t.TempDir(), 7777)
	if err != nil {
		t.Fatal(err)
	}
	body := serveRun(t, server.Handler(), runDir, "/runs")
	content := serveRun(t, server.Handler(), runDir, "/runs/content")
	if !strings.Contains(content, `id="run-content"`) {
		t.Fatalf("Run content does not contain its root element: %s", content)
	}
	for _, expected := range []string{
		"#173", "feat/implementer-context-rollover", "approved", "implement", "1 of 3 iterations",
		"Summary", "Ready for review", "Iterations", "Blocking · 1", "Nit · 1", "Diff stat",
		"Branch point", "30de5905ca30", "Usage", "implement", "review", "syl resume implement #173", "syl resume review #173",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("Run body does not contain %q: %s", expected, body)
		}
	}
	if strings.Contains(body, "hx-trigger=\"every 3s") {
		t.Fatal("ended Run page contains a polling trigger")
	}
	writeWebArtifact(t, runDir, "summary.txt", "Iterations: 1\nFinal verdict: approve\nDiff stat:\n file.go | 2 ++\n")
	emptySummaryBody := serveRun(t, server.Handler(), runDir, "/runs")
	if !strings.Contains(emptySummaryBody, `aria-labelledby="summary-heading"`) || !strings.Contains(emptySummaryBody, `class="run-summary">—</p>`) {
		t.Fatalf("Run body does not show an empty summary section: %s", emptySummaryBody)
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
	server, err := web.New(t.TempDir(), 7777)
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
	if err := runstate.Write(runstate.Path(runDir), runstate.State{
		Status: runstate.Running, Activity: runstate.Reviewing, Iteration: 1, MaxIterations: 3,
		PID: os.Getpid(), Hostname: testHostname(t), StartedAt: time.Now().UTC(), Kind: runstate.Implement, TicketRef: "#173",
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

	server, err := web.New(t.TempDir(), 7777)
	if err != nil {
		t.Fatal(err)
	}
	body := serveRun(t, server.Handler(), runDir, "/runs")
	if !strings.Contains(body, "hx-trigger=\"every 3s [document.visibilityState === 'visible']\"") || !strings.Contains(body, "Activity: reviewing") {
		t.Fatalf("running Run body = %s, want polling and activity", body)
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

func TestHandlerShowsInterruptedProjectRunAndFindsNewRunsOnNextRequest(t *testing.T) {
	sylHome := t.TempDir()
	project := t.TempDir()
	if _, err := config.Init(project); err != nil {
		t.Fatal(err)
	}
	writeWebRegistry(t, sylHome, registry.Entry{Path: project})
	writeWebRun(t, project, "20260924T120000.000000000Z-1", runstate.State{
		Status: runstate.Running, Activity: runstate.Reviewing, Iteration: 1, MaxIterations: 3,
		PID: 999999, Hostname: testHostname(t), StartedAt: time.Now().UTC(), Kind: runstate.Implement, TicketRef: "#1",
	})
	server, err := web.New(sylHome, 7777)
	if err != nil {
		t.Fatal(err)
	}
	body := serveProject(t, server.Handler(), project, "/projects/content")
	for _, expected := range []string{"Interrupted", "#1"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("first Project body does not contain %q", expected)
		}
	}

	writeWebRun(t, project, "20260924T130000.000000000Z-2", runstate.State{
		Status: runstate.Approved, Iteration: 1, MaxIterations: 3,
		StartedAt: time.Now().UTC(), Kind: runstate.Implement, TicketRef: "#2",
	})
	body = serveProject(t, server.Handler(), project, "/projects/content")
	if !strings.Contains(body, "#2") {
		t.Fatal("second Project request did not find the new Run")
	}
}

func TestHandlerDismissesInterruptedRunWithoutChangingRunFiles(t *testing.T) {
	sylHome, runDir := writeMutationFixture(t, false)
	server, err := web.New(sylHome, 7777)
	if err != nil {
		t.Fatal(err)
	}
	beforeRun := snapshotFiles(t, runDir)
	beforeHome := snapshotFiles(t, sylHome)
	token := tokenFromPage(t, serveOverview(t, server.Handler(), "127.0.0.1:7777"))

	response := postMutation(t, server.Handler(), "/runs/dismiss", url.Values{
		"run_dir": {runDir}, "token": {token},
	}, "")
	if response.Code != http.StatusSeeOther {
		t.Fatalf("dismiss status = %d, want redirect; body = %q", response.Code, response.Body.String())
	}
	markers, err := runmarker.List(sylHome)
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
	if body := serveOverview(t, server.Handler(), "localhost:7777"); strings.Contains(body, "#183") {
		t.Fatal("dismissed Run still appears in Overview")
	}
}

func TestHandlerRefusesDismissForLiveRun(t *testing.T) {
	sylHome, runDir := writeMutationFixture(t, true)
	server, err := web.New(sylHome, 7777)
	if err != nil {
		t.Fatal(err)
	}
	beforeRun := snapshotFiles(t, runDir)
	beforeHome := snapshotFiles(t, sylHome)
	token := tokenFromPage(t, serveOverview(t, server.Handler(), "127.0.0.1:7777"))

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
	server, err := web.New(sylHome, 7777)
	if err != nil {
		t.Fatal(err)
	}
	token := tokenFromPage(t, serveOverview(t, server.Handler(), "127.0.0.1:7777"))
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
	server, err := web.New(sylHome, 7777)
	if err != nil {
		t.Fatal(err)
	}
	token := tokenFromPage(t, serveOverview(t, server.Handler(), "127.0.0.1:7777"))

	response := postMutation(t, server.Handler(), "/runs/dismiss", url.Values{
		"run_dir": {runDir}, "token": {token},
	}, "http://localhost:7777")
	if response.Code != http.StatusSeeOther {
		t.Fatalf("same-origin dismiss status = %d, want redirect", response.Code)
	}
}

func TestHandlerRejectsMalformedAndIncompleteMutations(t *testing.T) {
	server, err := web.New(t.TempDir(), 7777)
	if err != nil {
		t.Fatal(err)
	}
	token := tokenFromPage(t, serveOverview(t, server.Handler(), "127.0.0.1:7777"))
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
	if err := os.WriteFile(runstate.Path(runDir), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	server, err := web.New(sylHome, 7777)
	if err != nil {
		t.Fatal(err)
	}
	token := tokenFromPage(t, serveOverview(t, server.Handler(), "127.0.0.1:7777"))
	response := rawMutation(t, server.Handler(), http.MethodPost, "/runs/dismiss", url.Values{
		"run_dir": {runDir},
	}.Encode(), token, false)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("dismiss corrupt-state status = %d, want internal server error", response.Code)
	}
}

func TestHandlerReportsForgetPathFailure(t *testing.T) {
	server, err := web.New(t.TempDir(), 7777)
	if err != nil {
		t.Fatal(err)
	}
	token := tokenFromPage(t, serveOverview(t, server.Handler(), "127.0.0.1:7777"))
	response := rawMutation(t, server.Handler(), http.MethodPost, "/projects/forget", url.Values{
		"path": {"\x00"},
	}.Encode(), token, false)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("forget invalid-path status = %d, want internal server error", response.Code)
	}
}

func TestHandlerRendersOverviewContentForHXMutation(t *testing.T) {
	sylHome, runDir := writeMutationFixture(t, false)
	server, err := web.New(sylHome, 7777)
	if err != nil {
		t.Fatal(err)
	}
	token := tokenFromPage(t, serveOverview(t, server.Handler(), "127.0.0.1:7777"))
	response := rawMutation(t, server.Handler(), http.MethodPost, "/runs/dismiss", url.Values{
		"run_dir": {runDir},
	}.Encode(), token, true)
	if response.Code != http.StatusOK {
		t.Fatalf("HX dismiss status = %d, want 200; body = %q", response.Code, response.Body.String())
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
	writeWebRegistry(t, sylHome, registry.Entry{Path: project})
	server, err := web.New(sylHome, 7777)
	if err != nil {
		t.Fatal(err)
	}
	beforeProject := snapshotFiles(t, project)
	token := tokenFromPage(t, serveOverview(t, server.Handler(), "127.0.0.1:7777"))

	response := postMutation(t, server.Handler(), "/projects/forget", url.Values{
		"path": {project}, "token": {token},
	}, "")
	if response.Code != http.StatusSeeOther {
		t.Fatalf("forget status = %d, want redirect; body = %q", response.Code, response.Body.String())
	}
	entries, err := registry.List(sylHome)
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
	server, err := web.New(t.TempDir(), 7777)
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

func TestOverviewShowsForgetOnlyForMissingProject(t *testing.T) {
	sylHome := t.TempDir()
	okProject := t.TempDir()
	uninitializedProject := t.TempDir()
	invalidProject := t.TempDir()
	missingProject := filepath.Join(t.TempDir(), "missing")
	if _, err := config.Init(okProject); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(invalidProject, ".syl"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Path(invalidProject), []byte("invalid = [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeWebRegistry(t, sylHome,
		registry.Entry{Path: okProject},
		registry.Entry{Path: uninitializedProject},
		registry.Entry{Path: invalidProject},
		registry.Entry{Path: missingProject},
	)
	server, err := web.New(sylHome, 7777)
	if err != nil {
		t.Fatal(err)
	}

	body := serveOverview(t, server.Handler(), "127.0.0.1:7777")
	if got := strings.Count(body, ">Forget</button>"); got != 1 {
		t.Fatalf("Forget buttons = %d, want only the missing Project", got)
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

func writeWebRegistry(t *testing.T, sylHome string, entries ...registry.Entry) {
	t.Helper()
	contents, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registry.Path(sylHome), contents, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeWebRun(t *testing.T, project, name string, state runstate.State) {
	t.Helper()
	runDir := filepath.Join(project, ".syl", "runs", name)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runstate.Write(runstate.Path(runDir), state); err != nil {
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
	writeWebRegistry(t, sylHome, registry.Entry{Path: project})
	runDir := filepath.Join(project, ".syl", "runs", "run-183")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pid := 999999
	if live {
		pid = os.Getpid()
	}
	state := runstate.State{
		Status: runstate.Running, Activity: runstate.Implementing, PID: pid,
		Hostname: testHostname(t), StartedAt: time.Now().UTC(), Kind: runstate.Implement, TicketRef: "#183",
	}
	if err := runstate.Write(runstate.Path(runDir), state); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "metadata.txt"), []byte("Work root: /worktree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runmarker.Create(sylHome, project, runDir, state.TicketRef, state.PID, state.Hostname); err != nil {
		t.Fatal(err)
	}
	return sylHome, runDir
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
