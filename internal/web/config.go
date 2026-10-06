package web

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/igorrochap/syl/internal/configedit"
	"github.com/igorrochap/syl/internal/configview"
	"github.com/igorrochap/syl/internal/pageview"
	"github.com/igorrochap/syl/internal/readmodel"
)

type configPageData struct {
	layoutData
	configview.View
	ConfigHealthClass string
	FirstSeen         string
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
	s.render(writer, renderedPage{template: "project-config", data: s.configTemplateData(view)}, request.URL.Path == "/projects/config/content")
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
	snapshot, err := configedit.Load(page.Project.Path)
	if err != nil {
		writeServerError(writer, err)
		return
	}
	values, fieldErrors := configedit.Parse(request.Form, snapshot.Values)
	version := request.Form.Get("version")
	result := configview.Save(page, version, values, fieldErrors)
	s.renderConfigSaveResult(writer, request, projectPath, result)
}

func (s *Server) renderConfigSaveResult(
	writer http.ResponseWriter,
	request *http.Request,
	projectPath string,
	result configview.SaveResult,
) {
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
	return configPageData{
		layoutData:        layoutData{Port: s.port, Token: s.token, PageChrome: pageview.ConfigPageChrome()},
		View:              view,
		ConfigHealthClass: pageview.HealthClass(readmodel.Health(view.ConfigHealth)),
		FirstSeen:         pageview.FormatAbsoluteTime(view.Page.Project.FirstSeen, pageview.AbsoluteDate),
	}
}

func (s *Server) renderConfigContent(writer http.ResponseWriter, view configview.View) {
	s.render(writer, renderedPage{template: "project-config", data: s.configTemplateData(view)}, true)
}

func (s *Server) renderConfigFeedback(
	writer http.ResponseWriter,
	request *http.Request,
	view configview.View,
	status int,
) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.WriteHeader(status)
	s.render(writer, renderedPage{template: "project-config", data: s.configTemplateData(view)}, request.Header.Get("HX-Request") == "true")
}
