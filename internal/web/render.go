package web

import (
	"html/template"
	"net/http"
	"net/url"

	"github.com/igorrochap/syl/internal/pageview"
)

type renderedPage struct {
	template string
	data     any
}

type layoutData struct {
	Port  int
	Token string
	pageview.PageChrome
}

func (s *Server) render(writer http.ResponseWriter, page renderedPage, fragment bool) {
	templateName := page.template
	if fragment {
		templateName += "-content"
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.templates.ExecuteTemplate(writer, templateName, page.data)
}

func templateFunctions() template.FuncMap {
	return template.FuncMap{
		"urlquery": url.QueryEscape,
	}
}

func writeServerError(writer http.ResponseWriter, err error) {
	writer.WriteHeader(http.StatusInternalServerError)
	_, _ = writer.Write([]byte("syl ui: " + err.Error()))
}
