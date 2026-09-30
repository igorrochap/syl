// Package web serves the local syl ui web panel.
package web

import (
	"context"
	"crypto/rand"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net"
	"net/http"
	"time"

	"github.com/igorrochap/syl/internal/readmodel"
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
