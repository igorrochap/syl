// Package configedit owns the non-HTTP workflow for editing a Project config.
package configedit

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/igorrochap/syl/internal/config"
)

// ErrConflict means the config changed after a form was loaded.
var ErrConflict = errors.New("config changed on disk after form loaded")

// RoleValues are the editable values for one workflow Role.
type RoleValues struct {
	Harness string
	Model   string
	Effort  string
	MCP     bool
}

// Values are the editable fields shown by the structured Config form.
type Values struct {
	TrackerIssues  string
	TrackerReviews string

	Plan      RoleValues
	Implement RoleValues
	Review    RoleValues

	MaxIterations        int
	NotificationsEnabled bool

	WorktreeRoot  string
	WorktreeSetup string
	WorktreeCopy  []string
}

// Snapshot is the config and file version loaded into a form.
type Snapshot struct {
	Values  Values
	Version string
}

// FieldErrors maps config field names to the exact validation message for the
// value in that field.
type FieldErrors map[string]string

// LoadError preserves the config key associated with a load failure when one
// exists. Syntax and filesystem failures leave Key empty.
type LoadError struct {
	Key string
	Err error
}

// Error returns the exact error message produced while loading the config.
func (err LoadError) Error() string {
	return err.Err.Error()
}

// Unwrap preserves errors.Is and errors.As behavior for the underlying load error.
func (err LoadError) Unwrap() error {
	return err.Err
}

// Error returns the first validation message in a stable order.
func (errors FieldErrors) Error() string {
	if len(errors) == 0 {
		return ""
	}
	fields := make([]string, 0, len(errors))
	for field := range errors {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	return errors[fields[0]]
}

// Load reads and versions the current config for a form.
func Load(projectRoot string) (Snapshot, error) {
	contents, err := os.ReadFile(config.Path(projectRoot))
	if err != nil {
		return Snapshot{}, LoadError{Err: fmt.Errorf("read config for edit: %w", err)}
	}
	loaded, err := config.Load(projectRoot)
	if err != nil {
		return Snapshot{}, loadError(err)
	}
	return Snapshot{Values: valuesFromConfig(loaded), Version: version(contents)}, nil
}

func loadError(err error) LoadError {
	var fieldError config.FieldError
	if errors.As(err, &fieldError) {
		return LoadError{Key: fieldError.Field, Err: err}
	}
	return LoadError{Err: err}
}

// Parse converts submitted form values into editable config values.
func Parse(form url.Values) (Values, FieldErrors) {
	values := Values{
		TrackerIssues:        form.Get("tracker.issues"),
		TrackerReviews:       form.Get("tracker.reviews"),
		Plan:                 parseRole(form, "plan"),
		Implement:            parseRole(form, "implement"),
		Review:               parseRole(form, "review"),
		NotificationsEnabled: formBool(form, "notifications.enabled"),
		WorktreeRoot:         form.Get("worktree.root"),
		WorktreeSetup:        form.Get("worktree.setup"),
		WorktreeCopy:         nonEmptyValues(form["worktree.copy"]),
	}

	errors := FieldErrors{}
	maxIterations, err := strconv.Atoi(form.Get("loop.max_iterations"))
	if err != nil {
		errors["loop.max_iterations"] = fmt.Sprintf("loop.max_iterations: invalid value %q; want an integer", form.Get("loop.max_iterations"))
	} else {
		values.MaxIterations = maxIterations
	}
	for field, message := range Validate(values) {
		if _, exists := errors[field]; !exists {
			errors[field] = message
		}
	}
	return values, errors
}

func nonEmptyValues(values []string) []string {
	filtered := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		filtered = append(filtered, value)
	}
	return filtered
}

func parseRole(form url.Values, role string) RoleValues {
	prefix := "roles." + role + "."
	return RoleValues{
		Harness: form.Get(prefix + "harness"),
		Model:   form.Get(prefix + "model"),
		Effort:  form.Get(prefix + "effort"),
		MCP:     formBool(form, prefix+"mcp"),
	}
}

func formBool(form url.Values, field string) bool {
	switch form.Get(field) {
	case "true", "on", "1":
		return true
	default:
		return false
	}
}

// Validate applies the same field rules as config.Load to submitted values.
func Validate(values Values) FieldErrors {
	errors := FieldErrors{}
	for _, fieldError := range config.ValidationErrors(values.config()) {
		errors[fieldError.Field] = fieldError.Message
	}
	return errors
}

// Save validates values, checks the loaded file version, and atomically writes
// the new config when the file is unchanged.
func Save(projectRoot, expectedVersion string, values Values) error {
	if errors := Validate(values); len(errors) > 0 {
		return errors
	}
	contents, err := os.ReadFile(config.Path(projectRoot))
	if err != nil {
		return fmt.Errorf("read current config: %w", err)
	}
	if version(contents) != expectedVersion {
		return ErrConflict
	}
	if _, err := config.Write(projectRoot, values.config(), config.OverwriteExisting); err != nil {
		return err
	}
	return nil
}

// TrackerOptions returns the tracker values accepted by config.Load.
func TrackerOptions() []string {
	return stringOptions(config.Trackers())
}

// HarnessOptions returns the harness values accepted by config.Load.
func HarnessOptions() []string {
	return stringOptions(config.Harnesses())
}

// EffortOptions returns the effort values accepted by config.Load.
func EffortOptions() []string {
	return stringOptions(config.Efforts())
}

func stringOptions[T ~string](values []T) []string {
	options := make([]string, len(values))
	for index, value := range values {
		options[index] = string(value)
	}
	return options
}

func valuesFromConfig(loaded config.Config) Values {
	return Values{
		TrackerIssues:        string(loaded.Tracker.Issues),
		TrackerReviews:       string(loaded.Tracker.Reviews),
		Plan:                 roleValuesFromConfig(loaded.Roles.Plan),
		Implement:            roleValuesFromConfig(loaded.Roles.Implement),
		Review:               roleValuesFromConfig(loaded.Roles.Review),
		MaxIterations:        loaded.Loop.MaxIterations,
		NotificationsEnabled: loaded.Notifications.Enabled,
		WorktreeRoot:         loaded.Worktree.Root,
		WorktreeSetup:        loaded.Worktree.Setup,
		WorktreeCopy:         append([]string(nil), loaded.Worktree.Copy...),
	}
}

func roleValuesFromConfig(role config.RoleConfig) RoleValues {
	return RoleValues{Harness: string(role.Harness), Model: role.Model, Effort: string(role.Effort), MCP: role.MCP}
}

func (values Values) config() config.Config {
	return config.Config{
		Tracker: config.TrackerConfig{Issues: config.Tracker(values.TrackerIssues), Reviews: config.Tracker(values.TrackerReviews)},
		Roles: config.RolesConfig{
			Plan:      values.Plan.config(),
			Implement: values.Implement.config(),
			Review:    values.Review.config(),
		},
		Loop:          config.LoopConfig{MaxIterations: values.MaxIterations},
		Notifications: config.NotificationsConfig{Enabled: values.NotificationsEnabled},
		Worktree:      config.WorktreeConfig{Root: values.WorktreeRoot, Setup: values.WorktreeSetup, Copy: append([]string(nil), values.WorktreeCopy...)},
	}
}

func (role RoleValues) config() config.RoleConfig {
	return config.RoleConfig{Harness: config.Harness(role.Harness), Model: role.Model, Effort: config.Effort(role.Effort), MCP: role.MCP}
}

func version(contents []byte) string {
	hash := sha256.Sum256(contents)
	return hex.EncodeToString(hash[:])
}
