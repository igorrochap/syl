package web

import (
	"net/http"
	"strings"
	"time"

	"github.com/igorrochap/syl/internal/pageview"
)

type overviewPageData struct {
	layoutData
	Overview pageview.Overview
}

func (s *Server) overview(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/" && request.URL.Path != "/overview/content" {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	s.renderOverview(writer, request.URL.Path == "/overview/content")
}

func (s *Server) renderOverview(writer http.ResponseWriter, fragment bool) {
	model, err := s.model()
	if err != nil {
		writeServerError(writer, err)
		return
	}
	page := pageview.BuildOverview(model, time.Now())
	data := overviewPageData{
		layoutData: layoutData{
			Port:       s.port,
			Token:      s.token,
			PageChrome: page.Chrome,
		},
		Overview: page,
	}
	s.render(writer, renderedPage{template: "overview", data: data}, fragment)
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

func (s *Server) completeMutation(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("HX-Request") == "true" {
		s.renderOverview(writer, true)
		return
	}
	http.Redirect(writer, request, "/", http.StatusSeeOther)
}
