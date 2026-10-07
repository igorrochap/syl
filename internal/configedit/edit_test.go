package configedit_test

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
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
	if got, want := configedit.SandboxOptions(), stringify(config.SandboxModes()); !reflect.DeepEqual(got, want) {
		t.Fatalf("sandbox options = %#v, want %#v", got, want)
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

func TestParseAndSaveKeepsLoadedSandboxWhenFormOmitsIt(t *testing.T) {
	project := t.TempDir()
	if _, err := config.Init(project); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(project)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Roles.Review.SandboxMode = config.SandboxModeWorkspaceWrite
	if _, err := config.Write(project, loaded, config.OverwriteExisting); err != nil {
		t.Fatal(err)
	}

	snapshot, err := configedit.Load(project)
	if err != nil {
		t.Fatal(err)
	}
	form := formFromValues(snapshot.Values)
	form.Del("roles.plan.sandbox")
	form.Del("roles.implement.sandbox")
	form.Del("roles.review.sandbox")
	values, fieldErrors := configedit.Parse(form, snapshot.Values)
	if len(fieldErrors) > 0 {
		t.Fatalf("Parse() field errors = %#v, want none", fieldErrors)
	}
	if values.Review.SandboxMode != string(config.SandboxModeWorkspaceWrite) {
		t.Fatalf("parsed review sandbox = %q, want workspace-write", values.Review.SandboxMode)
	}
	if err := configedit.Save(project, snapshot.Version, values); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	after, err := config.Load(project)
	if err != nil {
		t.Fatal(err)
	}
	if after.Roles.Review.SandboxMode != config.SandboxModeWorkspaceWrite {
		t.Fatalf("saved review sandbox = %q, want workspace-write", after.Roles.Review.SandboxMode)
	}
}

func TestParseValidateAndSaveSandboxMode(t *testing.T) {
	project := t.TempDir()
	if _, err := config.Init(project); err != nil {
		t.Fatal(err)
	}
	snapshot, err := configedit.Load(project)
	if err != nil {
		t.Fatal(err)
	}

	form := formFromValues(snapshot.Values)
	form.Set("roles.implement.sandbox", "read-only")
	values, fieldErrors := configedit.Parse(form, snapshot.Values)
	if len(fieldErrors) > 0 {
		t.Fatalf("Parse() field errors = %#v, want none", fieldErrors)
	}
	if got := configedit.Validate(values); len(got) > 0 {
		t.Fatalf("Validate() errors = %#v, want none", got)
	}
	if err := configedit.Save(project, snapshot.Version, values); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := config.Load(project)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Roles.Implement.SandboxMode != config.SandboxModeReadOnly {
		t.Fatalf("saved implement sandbox = %q, want read-only", loaded.Roles.Implement.SandboxMode)
	}

	snapshot, err = configedit.Load(project)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(config.Path(project))
	if err != nil {
		t.Fatal(err)
	}
	invalid := snapshot.Values
	invalid.Implement.SandboxMode = "none"
	if got := configedit.Validate(invalid)["roles.implement.sandbox"]; got != `roles.implement.sandbox: invalid value "none"; want full-access, workspace-write, or read-only` {
		t.Fatalf("Validate() sandbox error = %q", got)
	}
	form = formFromValues(snapshot.Values)
	form.Set("roles.implement.sandbox", "none")
	_, fieldErrors = configedit.Parse(form, snapshot.Values)
	if got := fieldErrors["roles.implement.sandbox"]; got != `roles.implement.sandbox: invalid value "none"; want full-access, workspace-write, or read-only` {
		t.Fatalf("Parse() sandbox error = %q", got)
	}
	err = configedit.Save(project, snapshot.Version, invalid)
	var saveErrors configedit.FieldErrors
	if !errors.As(err, &saveErrors) {
		t.Fatalf("Save() error = %v, want sandbox field errors", err)
	}
	if got := saveErrors["roles.implement.sandbox"]; got != `roles.implement.sandbox: invalid value "none"; want full-access, workspace-write, or read-only` {
		t.Fatalf("Save() sandbox error = %q", got)
	}
	after, err := os.ReadFile(config.Path(project))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("invalid sandbox save changed config file")
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
	_, fieldErrors := configedit.Parse(form, sandboxDefaults())
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
	return config.RoleConfig{Harness: config.Harness(values.Harness), Model: values.Model, Effort: config.Effort(values.Effort), MCP: values.MCP, SandboxMode: config.SandboxMode(values.SandboxMode)}
}

func sandboxDefaults() configedit.Values {
	return configedit.Values{
		Plan:      configedit.RoleValues{SandboxMode: string(config.SandboxModeFullAccess)},
		Implement: configedit.RoleValues{SandboxMode: string(config.SandboxModeFullAccess)},
		Review:    configedit.RoleValues{SandboxMode: string(config.SandboxModeFullAccess)},
	}
}

func formFromValues(values configedit.Values) url.Values {
	form := url.Values{
		"tracker.issues":      {values.TrackerIssues},
		"tracker.reviews":     {values.TrackerReviews},
		"loop.max_iterations": {strconv.Itoa(values.MaxIterations)},
		"worktree.root":       {values.WorktreeRoot},
		"worktree.setup":      {values.WorktreeSetup},
	}
	setRoleForm(form, "plan", values.Plan)
	setRoleForm(form, "implement", values.Implement)
	setRoleForm(form, "review", values.Review)
	if values.NotificationsEnabled {
		form.Set("notifications.enabled", "true")
	}
	for _, path := range values.WorktreeCopy {
		form.Add("worktree.copy", path)
	}
	return form
}

func setRoleForm(form url.Values, name string, values configedit.RoleValues) {
	prefix := "roles." + name + "."
	form.Set(prefix+"harness", values.Harness)
	form.Set(prefix+"model", values.Model)
	form.Set(prefix+"effort", values.Effort)
	form.Set(prefix+"sandbox", values.SandboxMode)
	if values.MCP {
		form.Set(prefix+"mcp", "true")
	}
}
