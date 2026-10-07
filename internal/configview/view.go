// Package configview builds the HTTP-independent view model for a Project's Config tab.
package configview

import (
	"errors"
	"os"
	"strings"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/configedit"
	"github.com/igorrochap/syl/internal/readmodel"
)

// State identifies which Config tab screen should be shown.
type State string

const (
	// StateForm shows the editable config form.
	StateForm State = "form"
	// StateInvalid shows the read-only source for a config that fails to load.
	StateInvalid State = "invalid"
	// StateUninitialized shows the setup instructions when config is missing.
	StateUninitialized State = "uninitialized"
)

// View contains every Config tab decision and value used to render the page.
type View struct {
	Page             readmodel.ProjectPage
	State            State
	ConfigHealth     readmodel.Health
	Values           configedit.Values
	Version          string
	Errors           configedit.FieldErrors
	Conflict         bool
	HasLiveRunBanner bool
	TrackerOptions   []string
	HarnessOptions   []string
	EffortOptions    []string
	SandboxOptions   []string
	Invalid          *InvalidConfig
}

// InvalidConfig contains the error and read-only source shown for an invalid config.
type InvalidConfig struct {
	Path  string
	Error string
	Lines []SourceLine
}

// SourceLine is one numbered line from the invalid config file.
type SourceLine struct {
	Number int
	Text   string
	Marked bool
}

// Build decides which Config tab screen to show and loads its data.
func Build(page readmodel.ProjectPage) (View, error) {
	view := newFormView(page)
	if page.Project.Health == readmodel.HealthUninitialized {
		view.State = StateUninitialized
		view.ConfigHealth = readmodel.HealthUninitialized
		return view, nil
	}

	snapshot, err := configedit.Load(page.Project.Path)
	if err == nil {
		view.Values = snapshot.Values
		view.Version = snapshot.Version
		return view, nil
	}
	if page.Project.Health == readmodel.HealthMissing {
		return View{}, err
	}
	if errors.Is(err, os.ErrNotExist) {
		view.State = StateUninitialized
		view.ConfigHealth = readmodel.HealthUninitialized
		return view, nil
	}

	var loadError configedit.LoadError
	if !errors.As(err, &loadError) {
		return View{}, err
	}
	contents, err := os.ReadFile(config.Path(page.Project.Path))
	if err != nil {
		return View{}, err
	}
	view.State = StateInvalid
	view.ConfigHealth = readmodel.HealthInvalid
	view.Invalid = &InvalidConfig{
		Path:  config.Path(page.Project.Path),
		Error: loadError.Error(),
		Lines: sourceLines(contents, loadError.Key),
	}
	return view, nil
}

// Save applies submitted values and returns the view and outcome for the next response.
func Save(page readmodel.ProjectPage, version string, values configedit.Values, fieldErrors configedit.FieldErrors) SaveResult {
	if len(fieldErrors) > 0 {
		view := newFormView(page).withFeedback(values, version, fieldErrors, false)
		return SaveResult{Outcome: SaveFieldErrors, View: view}
	}

	err := configedit.Save(page.Project.Path, version, values)
	if err == nil {
		nextView, err := Build(page)
		if err != nil {
			return SaveResult{Outcome: SaveUnexpectedError, Err: err}
		}
		return SaveResult{Outcome: SaveSaved, View: nextView}
	}

	if errors.Is(err, configedit.ErrConflict) {
		view := newFormView(page).withFeedback(values, version, nil, true)
		return SaveResult{Outcome: SaveConflict, View: view}
	}
	var validationErrors configedit.FieldErrors
	if errors.As(err, &validationErrors) {
		view := newFormView(page).withFeedback(values, version, validationErrors, false)
		return SaveResult{Outcome: SaveFieldErrors, View: view}
	}
	return SaveResult{Outcome: SaveUnexpectedError, Err: err}
}

// SaveOutcome is the result category of a Config tab save.
type SaveOutcome uint8

const (
	// SaveSaved means the values were persisted.
	SaveSaved SaveOutcome = iota
	// SaveFieldErrors means values failed validation.
	SaveFieldErrors
	// SaveConflict means the config changed since the form was loaded.
	SaveConflict
	// SaveUnexpectedError means the save failed for another reason.
	SaveUnexpectedError
)

// SaveResult contains the next view and the category of a save result.
type SaveResult struct {
	Outcome SaveOutcome
	View    View
	Err     error
}

func newFormView(page readmodel.ProjectPage) View {
	return View{
		Page:             page,
		State:            StateForm,
		ConfigHealth:     readmodel.HealthOK,
		HasLiveRunBanner: page.Project.LiveRunCount > 0,
		TrackerOptions:   configedit.TrackerOptions(),
		HarnessOptions:   configedit.HarnessOptions(),
		EffortOptions:    configedit.EffortOptions(),
		SandboxOptions:   configedit.SandboxOptions(),
	}
}

func (view View) withFeedback(values configedit.Values, version string, fieldErrors configedit.FieldErrors, conflict bool) View {
	view.State = StateForm
	view.ConfigHealth = view.Page.Project.Health
	view.Values = values
	view.Version = version
	view.Errors = fieldErrors
	view.Conflict = conflict
	view.Invalid = nil
	return view
}

func sourceLines(contents []byte, key string) []SourceLine {
	lines := strings.Split(string(contents), "\n")
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		lines = []string{""}
	}

	table := ""
	result := make([]SourceLine, 0, len(lines))
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			table = strings.TrimSpace(trimmed[1 : len(trimmed)-1])
		}
		result = append(result, SourceLine{
			Number: index + 1,
			Text:   line,
			Marked: sourceLineMatchesKey(trimmed, table, key),
		})
	}
	return result
}

func sourceLineMatchesKey(line, table, key string) bool {
	if key == "" || strings.HasPrefix(line, "#") {
		return false
	}
	separator := strings.Index(line, "=")
	if separator < 0 {
		return false
	}
	name := strings.Trim(strings.TrimSpace(line[:separator]), `"`)
	if name == "" {
		return false
	}
	if !strings.Contains(name, ".") && table != "" {
		name = table + "." + name
	}
	return name == key
}
