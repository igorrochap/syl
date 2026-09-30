package web

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/igorrochap/syl/internal/pageview"
	"github.com/igorrochap/syl/internal/readmodel"
)

type projectPageData struct {
	layoutData
	Page pageview.Project
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
	model, err := s.projectModel(projectPath)
	if err != nil {
		if errors.Is(err, readmodel.ErrProjectNotFound) {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		writeServerError(writer, err)
		return
	}
	page := pageview.BuildProject(model, time.Now())
	data := projectPageData{
		layoutData: layoutData{
			Port:       s.port,
			Token:      s.token,
			PageChrome: page.Chrome,
		},
		Page: page,
	}
	s.render(writer, renderedPage{template: "project", data: data}, request.URL.Path == "/projects/content")
}
