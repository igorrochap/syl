// Package web serves the local syl ui web panel.
package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/igorrochap/syl/internal/configedit"
	"github.com/igorrochap/syl/internal/configview"
	"github.com/igorrochap/syl/internal/readmodel"
	"github.com/igorrochap/syl/internal/runrecord"
	"github.com/igorrochap/syl/internal/sylhome"
)

//go:embed templates/*.html static/*
var assets embed.FS

// Server serves the Overview, Project pages, and their embedded assets.
type Server struct {
	port         int
	sylHome      sylhome.Dir
	token        string
	model        func() (readmodel.Overview, error)
	projectModel func(string) (readmodel.ProjectPage, error)
	runModel     func(string) (readmodel.RunPage, error)
	dismissRun   func(string) error
	templates    *template.Template
	assets       http.Handler
}

// New constructs a server that reads live state from sylHome for every page request.
func New(sylHome sylhome.Dir, port int) (*Server, error) {
	return newServer(sylHome, port, nil)
}

// NewWithProcessLiveness constructs a server with an injected process check.
func NewWithProcessLiveness(
	sylHome sylhome.Dir,
	port int,
	processAlive func(int) bool,
) (*Server, error) {
	return newServer(sylHome, port, processAlive)
}

func newServer(
	sylHome sylhome.Dir,
	port int,
	processAlive func(int) bool,
) (*Server, error) {
	token, err := newToken(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ui token: %w", err)
	}
	templates, err := template.New("overview").Funcs(templateFunctions()).ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse web templates: %w", err)
	}
	staticFiles, err := fs.Sub(assets, "static")
	if err != nil {
		return nil, fmt.Errorf("prepare web assets: %w", err)
	}
	reader := readmodel.NewReader(sylHome)
	dismissRun := func(runDir string) error {
		return readmodel.Dismiss(sylHome, runDir)
	}
	if processAlive != nil {
		reader = readmodel.NewReaderWithProcessLiveness(sylHome, processAlive)
		dismissRun = func(runDir string) error {
			return readmodel.DismissWithProcessLiveness(sylHome, runDir, processAlive)
		}
	}
	return &Server{
		port:         port,
		sylHome:      sylHome,
		token:        token,
		model:        reader.ReadOverview,
		projectModel: reader.ReadProject,
		runModel:     reader.ReadRun,
		dismissRun:   dismissRun,
		templates:    templates,
		assets:       http.StripPrefix("/assets/", http.FileServer(http.FS(staticFiles))),
	}, nil
}

// Handler returns the host-guarded HTTP handler for the server.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.overview)
	mux.HandleFunc("/projects", s.project)
	mux.HandleFunc("/projects/", s.project)
	mux.HandleFunc("/projects/config", s.configPage)
	mux.HandleFunc("/projects/config/content", s.configPage)
	mux.HandleFunc("/projects/config/save", s.saveConfig)
	mux.HandleFunc("/runs/artifact", s.artifact)
	mux.HandleFunc("/runs", s.run)
	mux.HandleFunc("/runs/", s.run)
	mux.HandleFunc("/runs/dismiss", s.dismiss)
	mux.HandleFunc("/projects/forget", s.forget)
	mux.Handle("/assets/", s.assets)
	return hostGuard(s.port, mux)
}

// Serve runs the HTTP server until ctx is cancelled or serving fails.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	if listener == nil {
		return errors.New("web listener is required")
	}
	server := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()

	select {
	case err := <-serveResult:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownErr := server.Shutdown(shutdownContext)
		serveErr := <-serveResult
		if shutdownErr != nil {
			return fmt.Errorf("shut down web server: %w", shutdownErr)
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
		return nil
	}
}

type pageData struct {
	Overview      readmodel.Overview
	Port          int
	Token         string
	LocalHostname string
	TotalRunCount int
}

func (s *Server) overview(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/" && request.URL.Path != "/overview/content" {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	if request.URL.Path == "/" {
		s.renderPage(writer)
		return
	}
	s.renderContent(writer)
}

func (s *Server) project(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/projects" && request.URL.Path != "/projects/content" {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	projectPath := request.URL.Query().Get("path")
	if strings.TrimSpace(projectPath) == "" {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	data, err := s.projectData(projectPath)
	if err != nil {
		if errors.Is(err, readmodel.ErrProjectNotFound) {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		writeServerError(writer, err)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	templateName := "project"
	if request.URL.Path == "/projects/content" {
		templateName = "project-content"
	}
	if err := s.templates.ExecuteTemplate(writer, templateName, data); err != nil {
		return
	}
}

func (s *Server) renderPage(writer http.ResponseWriter) {
	data, err := s.pageData()
	if err != nil {
		writeServerError(writer, err)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(writer, "overview", data); err != nil {
		return
	}
}

func (s *Server) renderContent(writer http.ResponseWriter) {
	data, err := s.pageData()
	if err != nil {
		writeServerError(writer, err)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(writer, "overview-content", data); err != nil {
		return
	}
}

func (s *Server) pageData() (pageData, error) {
	overview, err := s.model()
	if err != nil {
		return pageData{}, err
	}
	return pageData{
		Overview:      overview,
		Port:          s.port,
		Token:         s.token,
		LocalHostname: localHostname(),
		TotalRunCount: len(overview.AwaitingAnswer) + len(overview.LiveRuns),
	}, nil
}

type projectPageData struct {
	Page  readmodel.ProjectPage
	Port  int
	Token string
}

type configPageData struct {
	configview.View
	Port  int
	Token string
}

func (s *Server) projectData(projectPath string) (projectPageData, error) {
	page, err := s.projectModel(projectPath)
	if err != nil {
		return projectPageData{}, err
	}
	return projectPageData{Page: page, Port: s.port, Token: s.token}, nil
}

func (s *Server) configPage(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/projects/config" && request.URL.Path != "/projects/config/content" {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	projectPath := request.URL.Query().Get("path")
	if strings.TrimSpace(projectPath) == "" {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	page, err := s.projectModel(projectPath)
	if err != nil {
		writeConfigPageError(writer, err)
		return
	}
	view, err := configview.Build(page)
	if err != nil {
		writeConfigPageError(writer, err)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	templateName := "project-config"
	if request.URL.Path == "/projects/config/content" {
		templateName = "project-config-content"
	}
	if err := s.templates.ExecuteTemplate(writer, templateName, s.configTemplateData(view)); err != nil {
		return
	}
}

func writeConfigPageError(writer http.ResponseWriter, err error) {
	if errors.Is(err, readmodel.ErrProjectNotFound) || errors.Is(err, os.ErrNotExist) {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	writeServerError(writer, err)
}

func (s *Server) saveConfig(writer http.ResponseWriter, request *http.Request) {
	if !s.authorizeMutation(writer, request) {
		return
	}
	if err := request.ParseForm(); err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	projectPath := request.URL.Query().Get("path")
	if strings.TrimSpace(projectPath) == "" {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	page, ok := s.configSavePage(writer, projectPath)
	if !ok {
		return
	}
	values, fieldErrors := configedit.Parse(request.Form)
	version := request.Form.Get("version")
	result := configview.Save(page, version, values, fieldErrors)
	s.renderConfigSaveResult(writer, request, projectPath, result)
}

func (s *Server) renderConfigSaveResult(writer http.ResponseWriter, request *http.Request, projectPath string, result configview.SaveResult) {
	switch result.Outcome {
	case configview.SaveSaved:
		s.renderConfigSaved(writer, request, projectPath, result.View)
	case configview.SaveFieldErrors:
		s.renderConfigFeedback(writer, request, result.View, http.StatusUnprocessableEntity)
	case configview.SaveConflict:
		s.renderConfigFeedback(writer, request, result.View, http.StatusConflict)
	case configview.SaveUnexpectedError:
		writeServerError(writer, result.Err)
	}
}

func (s *Server) renderConfigSaved(writer http.ResponseWriter, request *http.Request, projectPath string, view configview.View) {
	if request.Header.Get("HX-Request") == "true" {
		s.renderConfigContent(writer, view)
		return
	}
	http.Redirect(writer, request, "/projects/config?path="+url.QueryEscape(projectPath), http.StatusSeeOther)
}

func (s *Server) configSavePage(writer http.ResponseWriter, projectPath string) (readmodel.ProjectPage, bool) {
	page, err := s.projectModel(projectPath)
	if err == nil {
		return page, true
	}
	if errors.Is(err, readmodel.ErrProjectNotFound) {
		writer.WriteHeader(http.StatusNotFound)
		return readmodel.ProjectPage{}, false
	}
	writeServerError(writer, err)
	return readmodel.ProjectPage{}, false
}

func (s *Server) configTemplateData(view configview.View) configPageData {
	return configPageData{View: view, Port: s.port, Token: s.token}
}

func (s *Server) renderConfigContent(writer http.ResponseWriter, view configview.View) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(writer, "project-config-content", s.configTemplateData(view)); err != nil {
		return
	}
}

func (s *Server) renderConfigFeedback(writer http.ResponseWriter, request *http.Request, view configview.View, status int) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.WriteHeader(status)
	if request.Header.Get("HX-Request") != "true" {
		if err := s.templates.ExecuteTemplate(writer, "project-config", s.configTemplateData(view)); err != nil {
			return
		}
		return
	}
	if err := s.templates.ExecuteTemplate(writer, "project-config-content", s.configTemplateData(view)); err != nil {
		return
	}
}

type runPageData struct {
	Page  readmodel.RunPage
	Port  int
	Token string
}

func (s *Server) run(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/runs" && request.URL.Path != "/runs/content" {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	runDir := request.URL.Query().Get("path")
	if strings.TrimSpace(runDir) == "" {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	runDir, err := runrecord.ResolveRunDirectory(runDir)
	if err != nil {
		writeRunPathError(writer, err)
		return
	}
	data, err := s.runData(runDir)
	if err != nil {
		if errors.Is(err, readmodel.ErrRunNotFound) || errors.Is(err, os.ErrNotExist) {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		writeServerError(writer, err)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	templateName := "run"
	if request.URL.Path == "/runs/content" {
		templateName = "run-content"
	}
	if err := s.templates.ExecuteTemplate(writer, templateName, data); err != nil {
		return
	}
}

func writeRunPathError(writer http.ResponseWriter, err error) {
	if errors.Is(err, os.ErrNotExist) {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	writer.WriteHeader(http.StatusBadRequest)
}

func (s *Server) runData(runDir string) (runPageData, error) {
	page, err := s.runModel(runDir)
	if err != nil {
		return runPageData{}, err
	}
	return runPageData{Page: page, Port: s.port, Token: s.token}, nil
}

func (s *Server) artifact(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/runs/artifact" {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	runDir := request.URL.Query().Get("path")
	artifactName := request.URL.Query().Get("artifact")
	path, err := runrecord.ResolveArtifact(runDir, artifactName)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		writeServerError(writer, err)
		return
	}
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = writer.Write(contents)
}

func (s *Server) dismiss(writer http.ResponseWriter, request *http.Request) {
	if !s.authorizeMutation(writer, request) {
		return
	}
	if err := request.ParseForm(); err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	runDir := request.FormValue("run_dir")
	if strings.TrimSpace(runDir) == "" {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	if err := s.dismissRun(runDir); err != nil {
		if errors.Is(err, readmodel.ErrRunMarkerNotFound) || errors.Is(err, readmodel.ErrRunNotInterrupted) {
			writer.WriteHeader(http.StatusConflict)
			return
		}
		writeServerError(writer, err)
		return
	}
	s.completeMutation(writer, request)
}

func (s *Server) forget(writer http.ResponseWriter, request *http.Request) {
	if !s.authorizeMutation(writer, request) {
		return
	}
	if err := request.ParseForm(); err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	projectPath := request.FormValue("path")
	if strings.TrimSpace(projectPath) == "" {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	if err := s.sylHome.ForgetProject(projectPath); err != nil {
		writeServerError(writer, err)
		return
	}
	s.completeMutation(writer, request)
}

func (s *Server) authorizeMutation(writer http.ResponseWriter, request *http.Request) bool {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return false
	}
	if !sameOrigin(request, s.port) {
		writer.WriteHeader(http.StatusForbidden)
		return false
	}
	if !validToken(request, s.token) {
		writer.WriteHeader(http.StatusForbidden)
		return false
	}
	return true
}

func (s *Server) completeMutation(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("HX-Request") == "true" {
		s.renderContent(writer)
		return
	}
	http.Redirect(writer, request, "/", http.StatusSeeOther)
}

func hostGuard(port int, next http.Handler) http.Handler {
	allowedHosts := map[string]struct{}{
		"127.0.0.1:" + strconv.Itoa(port): {},
		"localhost:" + strconv.Itoa(port): {},
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if _, ok := allowedHosts[request.Host]; !ok {
			writer.WriteHeader(http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func newToken(reader io.Reader) (string, error) {
	bytes := make([]byte, 32)
	if _, err := io.ReadFull(reader, bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func validToken(request *http.Request, expected string) bool {
	provided := request.Header.Get("X-Syl-Token")
	if provided == "" {
		provided = request.PostFormValue("token")
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func sameOrigin(request *http.Request, port int) bool {
	origins := request.Header.Values("Origin")
	switch len(origins) {
	case 0:
		return true
	case 1:
		break
	default:
		return false
	}
	origin, err := url.Parse(origins[0])
	if err != nil || !validOriginURL(origin) {
		return false
	}
	originPort := origin.Port()
	if originPort == "" {
		originPort = "80"
	}
	if originPort != strconv.Itoa(port) {
		return false
	}
	return isLoopbackOrigin(origin)
}

func validOriginURL(origin *url.URL) bool {
	if origin.Scheme != "http" {
		return false
	}
	if origin.Path != "" {
		return false
	}
	if origin.RawQuery != "" {
		return false
	}
	if origin.Fragment != "" {
		return false
	}
	return origin.User == nil
}

func isLoopbackOrigin(origin *url.URL) bool {
	hostname := strings.ToLower(origin.Hostname())
	return hostname == "127.0.0.1" || hostname == "localhost"
}

func writeServerError(writer http.ResponseWriter, err error) {
	writer.WriteHeader(http.StatusInternalServerError)
	_, _ = writer.Write([]byte("syl ui: " + err.Error()))
}

func templateFunctions() template.FuncMap {
	return template.FuncMap{
		"healthClass":      healthClass,
		"harness":          displayHarness,
		"historyKind":      historyKind,
		"historyTicket":    historyTicket,
		"historyTokens":    historyTokens,
		"runArtifactURL":   runArtifactURL,
		"runDuration":      displayRunDuration,
		"runMetric":        displayRunMetric,
		"status":           presentRunStatus,
		"verdictClass":     verdictClass,
		"runEndTime":       displayRunEndTime,
		"trimArtifactName": runrecord.ArtifactLabel,
		"runTime":          displayRunTime,
		"runTokens":        displayRunTokens,
		"iteration":        displayIteration,
		"kind":             displayKind,
		"started":          displayStarted,
		"summary":          displaySummary,
		"ticket":           displayTicket,
		"duration":         displayDuration,
		"firstSeen":        displayFirstSeen,
		"boolText":         boolText,
		"urlquery":         url.QueryEscape,
	}
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func runArtifactURL(runDir, artifactName string) string {
	return "/runs/artifact?path=" + url.QueryEscape(runDir) + "&artifact=" + url.QueryEscape(artifactName)
}

func displayRunTime(value time.Time) string {
	if value.IsZero() {
		return "—"
	}
	return value.Local().Format("2 Jan 2006, 15:04")
}

func displayRunEndTime(value *time.Time) string {
	if value == nil {
		return "—"
	}
	return displayRunTime(*value)
}

func displayRunDuration(run readmodel.RunDetail) string {
	if !run.DurationKnown {
		return "—"
	}
	return formatDuration(run.Duration)
}

func displayRunTokens(run readmodel.RunDetail) string {
	if !run.TokensKnown {
		return "—"
	}
	return formatTokenCount(run.TotalTokens)
}

func displayRunMetric(value int64, known bool) string {
	if !known {
		return "—"
	}
	return formatTokenCount(value)
}

func verdictClass(status any) string {
	switch fmt.Sprint(status) {
	case "approve":
		return "green"
	case "revise":
		return "plum"
	default:
		return "neutral"
	}
}

func formatDuration(duration time.Duration) string {
	seconds := int(duration / time.Second)
	if seconds < 1 {
		return "0s"
	}
	minutes, seconds := seconds/60, seconds%60
	hours, minutes := minutes/60, minutes%60
	if hours > 0 {
		return strconv.Itoa(hours) + "h " + strconv.Itoa(minutes) + "m"
	}
	if minutes > 0 {
		return strconv.Itoa(minutes) + "m"
	}
	return strconv.Itoa(seconds) + "s"
}

func healthClass(health readmodel.Health) string {
	switch health {
	case readmodel.HealthOK:
		return "pill-green"
	case readmodel.HealthInvalid:
		return "pill-red"
	default:
		return "pill-neutral"
	}
}

func displayHarness(harnessName, model string) string {
	if harnessName == "" {
		harnessName = "unknown"
	}
	if model == "" {
		return harnessName
	}
	return harnessName + " · " + model
}

func displayIteration(iterationNumber, maximum int) string {
	if iterationNumber <= 0 && maximum <= 0 {
		return "—"
	}
	return strconv.Itoa(iterationNumber) + " / " + strconv.Itoa(maximum)
}

func displayKind(kind runrecord.Kind) string {
	if kind == "" {
		return "Run"
	}
	return string(kind)
}

func displayStarted(startedAt time.Time) string {
	if startedAt.IsZero() {
		return "unknown"
	}
	ago := time.Since(startedAt)
	if ago < time.Minute {
		return "just now"
	}
	if ago < time.Hour {
		return strconv.Itoa(int(ago/time.Minute)) + " min ago"
	}
	if ago < 24*time.Hour {
		return strconv.Itoa(int(ago/time.Hour)) + " h ago"
	}
	return startedAt.Local().Format("2 Jan 2006, 15:04")
}

func displaySummary(project readmodel.Project) string {
	prefix := ""
	switch project.Health {
	case readmodel.HealthInvalid:
		prefix = "config fails to load"
	case readmodel.HealthUninitialized:
		prefix = "no .syl/config.toml · run syl init"
	case readmodel.HealthMissing:
		prefix = "directory not found"
	}
	if prefix != "" {
		runSummary := liveRunSummary(project)
		if runSummary == "" {
			return prefix
		}
		return prefix + " · " + runSummary
	}
	return liveRunSummary(project)
}

func liveRunSummary(project readmodel.Project) string {
	parts := make([]string, 0, 2)
	if project.LiveRunCount > 0 {
		parts = append(parts, strconv.Itoa(project.LiveRunCount)+" live")
	}
	if project.InterruptedCount > 0 {
		parts = append(parts, strconv.Itoa(project.InterruptedCount)+" interrupted")
	}
	if len(parts) == 0 {
		return "No live Runs"
	}
	return strings.Join(parts, " · ")
}

func displayTicket(ticketReference string) string {
	if strings.TrimSpace(ticketReference) == "" {
		return "—"
	}
	return ticketReference
}

func historyKind(kind runrecord.Kind) string {
	if kind == "" {
		return "—"
	}
	return string(kind)
}

type runStatusView struct {
	Label                string
	OverviewLabel        string
	PillClass            string
	ActivityClass        string
	RowClass             string
	ActivityApplicable   bool
	ShowRecordedActivity bool
	Dismissible          bool
}

func presentRunStatus(status runrecord.ObservedStatus) runStatusView {
	switch status {
	case runrecord.ObservedRunning:
		return runStatusView{
			Label: "running", OverviewLabel: "running", PillClass: "running",
			ActivityClass: "running", ActivityApplicable: true, ShowRecordedActivity: true,
		}
	case runrecord.ObservedInterrupted:
		return runStatusView{
			Label: "Interrupted", OverviewLabel: "Interrupted", PillClass: "red",
			ActivityClass: "interrupted", RowClass: "interrupted", Dismissible: true,
		}
	case runrecord.ObservedApproved:
		return runStatusView{
			Label: "approved", OverviewLabel: "approved", PillClass: "green", ActivityClass: "running",
			ShowRecordedActivity: true,
		}
	case runrecord.ObservedExhausted:
		return runStatusView{
			Label: "exhausted", OverviewLabel: "exhausted", PillClass: "plum", ActivityClass: "running",
			ShowRecordedActivity: true,
		}
	case runrecord.ObservedFailed:
		return runStatusView{
			Label: "failed", OverviewLabel: "failed", PillClass: "red", ActivityClass: "running",
			ShowRecordedActivity: true,
		}
	case runrecord.ObservedCancelled:
		return runStatusView{
			Label: "cancelled", OverviewLabel: "cancelled", PillClass: "neutral", ActivityClass: "running",
			ShowRecordedActivity: true,
		}
	case runrecord.ObservedCompleted:
		return runStatusView{
			Label: "completed", OverviewLabel: "completed", PillClass: "completed", ActivityClass: "running",
			ShowRecordedActivity: true,
		}
	case runrecord.ObservedUnknown:
		return runStatusView{Label: "unknown", OverviewLabel: "Unknown", PillClass: "unknown", ActivityClass: "unknown"}
	default:
		return runStatusView{Label: "—", OverviewLabel: "Unknown", PillClass: "neutral", ActivityClass: "unknown"}
	}
}

func historyTicket(ticketReference string) string {
	trimmed := strings.TrimSpace(ticketReference)
	if trimmed == "" {
		return "—"
	}
	number := strings.TrimPrefix(trimmed, "#")
	if parsed, err := strconv.Atoi(number); err == nil && parsed > 0 {
		return "#" + strconv.Itoa(parsed)
	}
	return trimmed
}

func historyTokens(run readmodel.HistoryRun) string {
	if !run.TokensKnown {
		return "—"
	}
	return formatTokenCount(run.TotalTokens)
}

func displayDuration(run readmodel.HistoryRun) string {
	if !run.DurationKnown {
		return "—"
	}
	seconds := int(run.Duration / time.Second)
	if seconds < 1 {
		return "0s"
	}
	minutes, seconds := seconds/60, seconds%60
	hours, minutes := minutes/60, minutes%60
	if hours > 0 {
		return strconv.Itoa(hours) + "h " + strconv.Itoa(minutes) + "m"
	}
	if minutes > 0 {
		return strconv.Itoa(minutes) + "m"
	}
	return strconv.Itoa(seconds) + "s"
}

func displayFirstSeen(firstSeen time.Time) string {
	if firstSeen.IsZero() {
		return "—"
	}
	return firstSeen.UTC().Format("2 Jan 2006")
}

func formatTokenCount(tokens int64) string {
	switch {
	case tokens >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(tokens)/1_000_000)
	case tokens >= 1_000:
		return fmt.Sprintf("%.1fk", float64(tokens)/1_000)
	default:
		return strconv.FormatInt(tokens, 10)
	}
}

func localHostname() string {
	host, err := os.Hostname()
	if err != nil {
		return ""
	}
	return host
}
