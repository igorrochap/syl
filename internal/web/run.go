package web

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/igorrochap/syl/internal/pageview"
	"github.com/igorrochap/syl/internal/readmodel"
	"github.com/igorrochap/syl/internal/runrecord"
)

type runPageData struct {
	layoutData
	Page pageview.Run
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
	model, err := s.runModel(runDir)
	if err != nil {
		if errors.Is(err, readmodel.ErrRunNotFound) || errors.Is(err, os.ErrNotExist) {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		writeServerError(writer, err)
		return
	}
	page := pageview.BuildRun(model)
	data := runPageData{
		layoutData: layoutData{
			Port:       s.port,
			Token:      s.token,
			PageChrome: page.Chrome,
		},
		Page: page,
	}
	s.render(writer, renderedPage{template: "run", data: data}, request.URL.Path == "/runs/content")
}

func writeRunPathError(writer http.ResponseWriter, err error) {
	if errors.Is(err, os.ErrNotExist) {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	writer.WriteHeader(http.StatusBadRequest)
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
