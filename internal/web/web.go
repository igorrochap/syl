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
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/igorrochap/syl/internal/readmodel"
	"github.com/igorrochap/syl/internal/runstate"
)

//go:embed templates/*.html static/*
var assets embed.FS

// Server serves the Overview, Project pages, and their embedded assets.
type Server struct {
	port         int
	sylHome      string
	token        string
	model        func() (readmodel.Overview, error)
	projectModel func(string) (readmodel.ProjectPage, error)
	runModel     func(string) (readmodel.RunPage, error)
	templates    *template.Template
	assets       http.Handler
}

// New constructs a server that reads live state from sylHome for every page request.
func New(sylHome string, port int) (*Server, error) {
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
	return &Server{
		port:         port,
		sylHome:      sylHome,
		token:        token,
		model:        reader.ReadOverview,
		projectModel: reader.ReadProject,
		runModel:     reader.ReadRun,
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

func (s *Server) projectData(projectPath string) (projectPageData, error) {
	page, err := s.projectModel(projectPath)
	if err != nil {
		return projectPageData{}, err
	}
	return projectPageData{Page: page, Port: s.port, Token: s.token}, nil
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
	runDir, err := resolveRunDirectory(runDir)
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
	path, err := safeArtifactPath(runDir, artifactName)
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

func safeArtifactPath(runDir, artifactName string) (string, error) {
	if err := validateArtifactName(runDir, artifactName); err != nil {
		return "", err
	}
	root, err := resolveRunDirectory(runDir)
	if err != nil {
		return "", err
	}
	return resolveArtifactFile(root, artifactName)
}

func validateArtifactName(runDir, artifactName string) error {
	if strings.TrimSpace(runDir) == "" || strings.TrimSpace(artifactName) == "" {
		return errors.New("run directory and artifact are required")
	}
	if isAbsoluteArtifactPath(artifactName) {
		return errors.New("absolute artifact paths are not allowed")
	}
	if hasParentPathComponent(artifactName) {
		return errors.New("artifact path traversal is not allowed")
	}
	return nil
}

func isAbsoluteArtifactPath(path string) bool {
	if filepath.IsAbs(path) || filepath.VolumeName(path) != "" || strings.HasPrefix(path, "\\") {
		return true
	}
	return len(path) >= 2 && path[1] == ':'
}

func resolveRunDirectory(runDir string) (string, error) {
	root, err := filepath.EvalSymlinks(runDir)
	if err != nil {
		return "", err
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !rootInfo.IsDir() {
		return "", errors.New("run directory is not a directory")
	}
	if !isRunDirectory(root) {
		return "", errors.New("path is not a Run directory")
	}
	return root, nil
}

func isRunDirectory(path string) bool {
	runsDirectory := filepath.Dir(path)
	return filepath.Base(path) != "" && filepath.Base(runsDirectory) == "runs" &&
		filepath.Base(filepath.Dir(runsDirectory)) == ".syl"
}

func resolveArtifactFile(root, artifactName string) (string, error) {
	candidate := filepath.Join(root, artifactName)
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	if !pathWithin(root, resolved) {
		return "", errors.New("artifact resolves outside Run directory")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("artifact is not a regular file")
	}
	return resolved, nil
}

func hasParentPathComponent(path string) bool {
	for _, component := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if component == ".." {
			return true
		}
	}
	return false
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(relative) {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
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
	if err := readmodel.Dismiss(s.sylHome, runDir); err != nil {
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
	if err := readmodel.Forget(s.sylHome, projectPath); err != nil {
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
		"activity":           displayActivity,
		"activityClass":      activityClass,
		"healthClass":        healthClass,
		"harness":            displayHarness,
		"historyKind":        historyKind,
		"historyStatus":      historyStatus,
		"historyStatusClass": historyStatusClass,
		"historyTicket":      historyTicket,
		"historyTokens":      historyTokens,
		"runArtifactURL":     runArtifactURL,
		"runDuration":        displayRunDuration,
		"runMetric":          displayRunMetric,
		"runStatus":          displayRunStatus,
		"verdictClass":       verdictClass,
		"runEndTime":         displayRunEndTime,
		"trimArtifactName":   trimArtifactName,
		"runTime":            displayRunTime,
		"runTokens":          displayRunTokens,
		"iteration":          displayIteration,
		"kind":               displayKind,
		"rowClass":           rowClass,
		"started":            displayStarted,
		"summary":            displaySummary,
		"ticket":             displayTicket,
		"duration":           displayDuration,
		"firstSeen":          displayFirstSeen,
		"urlquery":           url.QueryEscape,
	}
}

func runArtifactURL(runDir, artifactName string) string {
	return "/runs/artifact?path=" + url.QueryEscape(runDir) + "&artifact=" + url.QueryEscape(artifactName)
}

func displayRunStatus(run readmodel.RunDetail) string {
	if run.Interrupted {
		return "Interrupted"
	}
	if run.Status == "" {
		return "—"
	}
	return run.Status
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

func trimArtifactName(name string) string {
	extension := strings.TrimPrefix(filepath.Ext(name), ".")
	if extension != "" {
		return extension
	}
	return name
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

func displayActivity(run readmodel.Run) string {
	if run.Interrupted {
		return "Interrupted"
	}
	if run.Unknown {
		return "Unknown"
	}
	if run.Activity != "" {
		return run.Activity
	}
	return string(run.Status)
}

func activityClass(run readmodel.Run) string {
	if run.Interrupted {
		return "interrupted"
	}
	if run.Unknown {
		return "unknown"
	}
	return "running"
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

func displayKind(kind runstate.Kind) string {
	if kind == "" {
		return "Run"
	}
	return string(kind)
}

func rowClass(run readmodel.Run) string {
	if run.Interrupted {
		return "interrupted"
	}
	return ""
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

func historyKind(kind runstate.Kind) string {
	if kind == "" {
		return "—"
	}
	return string(kind)
}

func historyStatus(status string) string {
	return status
}

func historyStatusClass(status string) string {
	switch status {
	case "running":
		return "running"
	case "Interrupted", "failed":
		return "red"
	case "approved":
		return "green"
	case "exhausted":
		return "plum"
	case "unknown":
		return "unknown"
	case "completed":
		return "completed"
	default:
		return "neutral"
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
