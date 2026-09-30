package configview_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/configedit"
	"github.com/igorrochap/syl/internal/configview"
	"github.com/igorrochap/syl/internal/readmodel"
)

func TestBuildFormViewAndLiveRunBanner(t *testing.T) {
	project := t.TempDir()
	if _, err := config.Init(project); err != nil {
		t.Fatal(err)
	}
	page := projectPage(project, readmodel.HealthInvalid)
	page.Project.LiveRunCount = 1

	view, err := configview.Build(page)
	if err != nil {
		t.Fatal(err)
	}
	if view.State != configview.StateForm {
		t.Fatalf("state = %q, want form", view.State)
	}
	if view.Page.Project.Health != readmodel.HealthInvalid {
		t.Fatalf("Project health = %q, want unchanged invalid", view.Page.Project.Health)
	}
	if view.ConfigHealth != readmodel.HealthOK {
		t.Fatalf("Config health = %q, want ok for the form state", view.ConfigHealth)
	}
	if view.Values.MaxIterations != 3 || view.Values.Implement.Effort != "xhigh" {
		t.Fatalf("form values = %+v, want config values", view.Values)
	}
	if len(view.TrackerOptions) == 0 || len(view.HarnessOptions) == 0 || len(view.EffortOptions) == 0 {
		t.Fatalf("form options are incomplete: %+v", view)
	}
	if !view.HasLiveRunBanner {
		t.Fatal("live Run banner is hidden when a Run is live")
	}

	page.Project.LiveRunCount = 0
	view, err = configview.Build(page)
	if err != nil {
		t.Fatal(err)
	}
	if view.HasLiveRunBanner {
		t.Fatal("live Run banner is shown when no Run is live")
	}
}

func TestBuildInvalidViewMarksTheFailingKey(t *testing.T) {
	project := t.TempDir()
	if _, err := config.Init(project); err != nil {
		t.Fatal(err)
	}
	path := config.Path(project)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	contents = []byte(strings.Replace(string(contents), `effort = "xhigh"`, `effort = "ultra"`, 1))
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatal(err)
	}

	view, err := configview.Build(projectPage(project, readmodel.HealthInvalid))
	if err != nil {
		t.Fatal(err)
	}
	if view.State != configview.StateInvalid || view.Invalid == nil {
		t.Fatalf("view = %+v, want invalid state", view)
	}
	if view.Invalid.Path != path || !strings.Contains(view.Invalid.Error, `invalid value "ultra"`) {
		t.Fatalf("invalid config details = %+v", view.Invalid)
	}
	marked := 0
	for _, line := range view.Invalid.Lines {
		if !line.Marked {
			continue
		}
		marked++
		if line.Text != `effort = "ultra"` {
			t.Fatalf("marked source line = %q, want failing effort line", line.Text)
		}
	}
	if marked != 1 {
		t.Fatalf("marked source lines = %d, want 1", marked)
	}
}

func TestBuildInvalidViewWithoutKeyDoesNotMarkASourceLine(t *testing.T) {
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(config.Path(project)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Path(project), []byte("[tracker\nissues = \"local\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	view, err := configview.Build(projectPage(project, readmodel.HealthInvalid))
	if err != nil {
		t.Fatal(err)
	}
	if view.State != configview.StateInvalid || view.Invalid == nil {
		t.Fatalf("view = %+v, want invalid state", view)
	}
	for _, line := range view.Invalid.Lines {
		if line.Marked {
			t.Fatalf("syntax error marked source line %d: %q", line.Number, line.Text)
		}
	}
}

func TestBuildEmptyInvalidViewKeepsOneSourceLine(t *testing.T) {
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(config.Path(project)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Path(project), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	view, err := configview.Build(projectPage(project, readmodel.HealthInvalid))
	if err != nil {
		t.Fatal(err)
	}
	if view.State != configview.StateInvalid || view.Invalid == nil || len(view.Invalid.Lines) != 1 {
		t.Fatalf("invalid empty source view = %+v, want one numbered line", view)
	}
	if view.Invalid.Lines[0].Number != 1 || view.Invalid.Lines[0].Text != "" {
		t.Fatalf("empty source line = %+v, want numbered empty line", view.Invalid.Lines[0])
	}
}

func TestBuildUninitializedView(t *testing.T) {
	project := t.TempDir()
	view, err := configview.Build(projectPage(project, readmodel.HealthUninitialized))
	if err != nil {
		t.Fatal(err)
	}
	if view.State != configview.StateUninitialized || view.Invalid != nil {
		t.Fatalf("view = %+v, want uninitialized state", view)
	}
}

func TestBuildMapsMissingConfigToUninitializedState(t *testing.T) {
	project := t.TempDir()
	view, err := configview.Build(projectPage(project, readmodel.HealthOK))
	if err != nil {
		t.Fatal(err)
	}
	if view.State != configview.StateUninitialized || view.ConfigHealth != readmodel.HealthUninitialized {
		t.Fatalf("view = %+v, want missing config mapped to uninitialized", view)
	}
}

func TestBuildReturnsErrorForMissingProject(t *testing.T) {
	project := filepath.Join(t.TempDir(), "missing-project")
	view, err := configview.Build(projectPage(project, readmodel.HealthMissing))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Build() error = %v, want missing-file error; view = %+v", err, view)
	}
}

func TestSaveReturnsSavedView(t *testing.T) {
	project := initializedProject(t)
	view := buildFormView(t, project)
	values := view.Values
	values.MaxIterations = 6

	result := configview.Save(view.Page, view.Version, values, nil)
	if result.Outcome != configview.SaveSaved {
		t.Fatalf("outcome = %v, error = %v, want saved", result.Outcome, result.Err)
	}
	if result.View.State != configview.StateForm || result.View.Values.MaxIterations != 6 {
		t.Fatalf("saved view = %+v, want updated form", result.View)
	}
	loaded, err := config.Load(project)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Loop.MaxIterations != 6 {
		t.Fatalf("saved max_iterations = %d, want 6", loaded.Loop.MaxIterations)
	}
}

func TestSaveReturnsFieldErrorsView(t *testing.T) {
	project := initializedProject(t)
	view := buildFormView(t, project)
	values := view.Values
	values.MaxIterations = 0
	fieldErrors := configedit.FieldErrors{"loop.max_iterations": "loop.max_iterations must be positive; got 0"}
	before, err := os.ReadFile(config.Path(project))
	if err != nil {
		t.Fatal(err)
	}

	result := configview.Save(view.Page, view.Version, values, fieldErrors)
	if result.Outcome != configview.SaveFieldErrors {
		t.Fatalf("outcome = %v, want field errors", result.Outcome)
	}
	if result.View.State != configview.StateForm || !reflect.DeepEqual(result.View.Errors, fieldErrors) || result.View.Conflict {
		t.Fatalf("field error view = %+v", result.View)
	}
	after, err := os.ReadFile(config.Path(project))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("field errors changed the config file")
	}
}

func TestSaveReturnsSubmittedFieldErrorsBeforeLoadingConfig(t *testing.T) {
	project := t.TempDir()
	page := projectPage(project, readmodel.HealthOK)
	fieldErrors := configedit.FieldErrors{"loop.max_iterations": "loop.max_iterations: invalid value \"nope\"; want an integer"}

	result := configview.Save(page, "", configedit.Values{}, fieldErrors)
	if result.Outcome != configview.SaveFieldErrors {
		t.Fatalf("outcome = %v, error = %v, want submitted field errors", result.Outcome, result.Err)
	}
	if result.View.Errors["loop.max_iterations"] != fieldErrors["loop.max_iterations"] {
		t.Fatalf("field errors = %v, want %v", result.View.Errors, fieldErrors)
	}
}

func TestSaveReturnsConflictView(t *testing.T) {
	project := initializedProject(t)
	view := buildFormView(t, project)
	values := view.Values
	if err := os.WriteFile(config.Path(project), []byte("# changed on disk\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := configview.Save(view.Page, view.Version, values, nil)
	if result.Outcome != configview.SaveConflict {
		t.Fatalf("outcome = %v, error = %v, want conflict", result.Outcome, result.Err)
	}
	if result.View.State != configview.StateForm || !result.View.Conflict {
		t.Fatalf("conflict view = %+v", result.View)
	}
	contents, err := os.ReadFile(config.Path(project))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "# changed on disk\n" {
		t.Fatalf("conflicting save changed disk contents: %q", contents)
	}
}

func TestSaveReturnsUnexpectedError(t *testing.T) {
	project := initializedProject(t)
	view := buildFormView(t, project)
	if err := os.Remove(config.Path(project)); err != nil {
		t.Fatal(err)
	}

	result := configview.Save(view.Page, view.Version, view.Values, nil)
	if result.Outcome != configview.SaveUnexpectedError || !errors.Is(result.Err, os.ErrNotExist) {
		t.Fatalf("save result = %+v, want unexpected missing-file error", result)
	}
}

func initializedProject(t *testing.T) string {
	t.Helper()
	project := t.TempDir()
	if _, err := config.Init(project); err != nil {
		t.Fatal(err)
	}
	return project
}

func buildFormView(t *testing.T, project string) configview.View {
	t.Helper()
	view, err := configview.Build(projectPage(project, readmodel.HealthOK))
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func projectPage(path string, health readmodel.Health) readmodel.ProjectPage {
	return readmodel.ProjectPage{Project: readmodel.Project{
		Name: filepath.Base(path), Path: path, Health: health,
	}}
}
