package configedit_test

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/configedit"
)

func TestOptionsComeFromConfigAcceptedValues(t *testing.T) {
	if got, want := configedit.TrackerOptions(), stringify(config.Trackers()); !reflect.DeepEqual(got, want) {
		t.Fatalf("tracker options = %#v, want %#v", got, want)
	}
	if got, want := configedit.HarnessOptions(), stringify(config.Harnesses()); !reflect.DeepEqual(got, want) {
		t.Fatalf("harness options = %#v, want %#v", got, want)
	}
	if got, want := configedit.EffortOptions(), stringify(config.Efforts()); !reflect.DeepEqual(got, want) {
		t.Fatalf("effort options = %#v, want %#v", got, want)
	}
}

func TestLoadAndSaveRoundTripsConfig(t *testing.T) {
	project := t.TempDir()
	if _, err := config.Init(project); err != nil {
		t.Fatal(err)
	}
	snapshot, err := configedit.Load(project)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Values.MaxIterations = 8
	snapshot.Values.WorktreeCopy = []string{".env", ".env.local"}

	if err := configedit.Save(project, snapshot.Version, snapshot.Values); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := config.Load(project)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Loop.MaxIterations != 8 || !reflect.DeepEqual(loaded.Worktree.Copy, snapshot.Values.WorktreeCopy) {
		t.Fatalf("loaded config = %#v, want edited values", loaded)
	}
	updated, err := configedit.Load(project)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version == snapshot.Version {
		t.Fatal("saved config kept the old version")
	}
}

func TestLoadPreservesInvalidConfigField(t *testing.T) {
	project := t.TempDir()
	if _, err := config.Init(project); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(config.Path(project))
	if err != nil {
		t.Fatal(err)
	}
	contents = []byte(strings.Replace(string(contents), `effort = "xhigh"`, `effort = "ultra"`, 1))
	if err := os.WriteFile(config.Path(project), contents, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = configedit.Load(project)
	var loadError configedit.LoadError
	if !errors.As(err, &loadError) || loadError.Key != "roles.implement.effort" {
		t.Fatalf("Load() error = %#v, want structured effort key", err)
	}
	if loadError.Error() != `roles.implement.effort: invalid value "ultra"; want low, medium, high, or xhigh` {
		t.Fatalf("Load() error = %q, want exact config error", loadError)
	}
	var fieldError config.FieldError
	if !errors.As(err, &fieldError) || fieldError.Field != loadError.Key {
		t.Fatalf("Load() error = %T, want underlying FieldError", err)
	}
}

func TestLoadPreservesUnkeyedAndFilesystemErrors(t *testing.T) {
	missingProject := t.TempDir()
	_, err := configedit.Load(missingProject)
	var missingError configedit.LoadError
	if !errors.As(err, &missingError) || !errors.Is(err, os.ErrNotExist) || missingError.Key != "" {
		t.Fatalf("missing config error = %#v, want unkeyed filesystem LoadError", err)
	}

	malformedProject := t.TempDir()
	if err := os.MkdirAll(filepath.Join(malformedProject, ".syl"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Path(malformedProject), []byte("[tracker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = configedit.Load(malformedProject)
	var malformedError configedit.LoadError
	if !errors.As(err, &malformedError) || malformedError.Key != "" || !strings.Contains(malformedError.Error(), "malformed TOML") {
		t.Fatalf("malformed config error = %#v, want unkeyed syntax LoadError", err)
	}
}

func TestSaveRejectsInvalidValueWithoutWriting(t *testing.T) {
	project := t.TempDir()
	if _, err := config.Init(project); err != nil {
		t.Fatal(err)
	}
	snapshot, err := configedit.Load(project)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(config.Path(project))
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Values.MaxIterations = 0

	err = configedit.Save(project, snapshot.Version, snapshot.Values)
	var fieldErrors configedit.FieldErrors
	if !errors.As(err, &fieldErrors) {
		t.Fatalf("Save() error = %v, want field errors", err)
	}
	if got := fieldErrors["loop.max_iterations"]; got != "loop.max_iterations must be positive; got 0" {
		t.Fatalf("max iteration error = %q", got)
	}
	after, err := os.ReadFile(config.Path(project))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("invalid save changed config file")
	}
}

func TestSaveRefusesChangedFile(t *testing.T) {
	project := t.TempDir()
	if _, err := config.Init(project); err != nil {
		t.Fatal(err)
	}
	snapshot, err := configedit.Load(project)
	if err != nil {
		t.Fatal(err)
	}
	external := snapshot.Values
	external.MaxIterations = 11
	if _, err := config.Write(project, externalConfig(external), config.OverwriteExisting); err != nil {
		t.Fatal(err)
	}
	snapshot.Values.MaxIterations = 9

	if err := configedit.Save(project, snapshot.Version, snapshot.Values); !errors.Is(err, configedit.ErrConflict) {
		t.Fatalf("Save() error = %v, want conflict", err)
	}
	loaded, err := config.Load(project)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Loop.MaxIterations != 11 {
		t.Fatalf("conflicting save changed on-disk value to %d", loaded.Loop.MaxIterations)
	}
}

func TestParseReportsLoadValidationMessages(t *testing.T) {
	form := url.Values{
		"tracker.issues":          {"github"},
		"tracker.reviews":         {"local"},
		"roles.plan.harness":      {"claude"},
		"roles.plan.model":        {"claude-planner"},
		"roles.plan.effort":       {"high"},
		"roles.implement.harness": {"codex"},
		"roles.implement.model":   {"gpt-implementer"},
		"roles.implement.effort":  {"xhigh"},
		"roles.review.harness":    {"claude"},
		"roles.review.model":      {"claude-reviewer"},
		"roles.review.effort":     {"medium"},
		"loop.max_iterations":     {"0"},
		"worktree.root":           {"~/.syl/worktrees"},
	}
	_, fieldErrors := configedit.Parse(form)
	if got := fieldErrors["loop.max_iterations"]; got != "loop.max_iterations must be positive; got 0" {
		t.Fatalf("Parse() error = %q", got)
	}
}

func stringify[T ~string](values []T) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	return result
}

func externalConfig(values configedit.Values) config.Config {
	return config.Config{
		Tracker: config.TrackerConfig{Issues: config.Tracker(values.TrackerIssues), Reviews: config.Tracker(values.TrackerReviews)},
		Roles: config.RolesConfig{
			Plan:      roleConfig(values.Plan),
			Implement: roleConfig(values.Implement),
			Review:    roleConfig(values.Review),
		},
		Loop:          config.LoopConfig{MaxIterations: values.MaxIterations},
		Notifications: config.NotificationsConfig{Enabled: values.NotificationsEnabled},
		Worktree:      config.WorktreeConfig{Root: values.WorktreeRoot, Setup: values.WorktreeSetup, Copy: values.WorktreeCopy},
	}
}

func roleConfig(values configedit.RoleValues) config.RoleConfig {
	return config.RoleConfig{Harness: config.Harness(values.Harness), Model: values.Model, Effort: config.Effort(values.Effort), MCP: values.MCP}
}
