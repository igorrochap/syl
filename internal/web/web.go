// Package web serves the local syl ui web panel.
package web

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
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
	model        func() (readmodel.Overview, error)
	projectModel func(string) (readmodel.ProjectPage, error)
	templates    *template.Template
	assets       http.Handler
}

// New constructs a server that reads live state from sylHome for every page request.
func New(sylHome string, port int) (*Server, error) {
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
		model:        reader.ReadOverview,
		projectModel: reader.ReadProject,
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
		LocalHostname: localHostname(),
		TotalRunCount: len(overview.AwaitingAnswer) + len(overview.LiveRuns),
	}, nil
}

type projectPageData struct {
	Page readmodel.ProjectPage
	Port int
}

func (s *Server) projectData(projectPath string) (projectPageData, error) {
	page, err := s.projectModel(projectPath)
	if err != nil {
		return projectPageData{}, err
	}
	return projectPageData{Page: page, Port: s.port}, nil
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
