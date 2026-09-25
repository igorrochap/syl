// Package config loads and validates a project's syl configuration.
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

const configRelativePath = ".syl/config.toml"

const (
	TrackerGitHub Tracker = "github"
	TrackerGitLab Tracker = "gitlab"
	TrackerLocal  Tracker = "local"
)

const (
	HarnessClaude   Harness = "claude"
	HarnessCodex    Harness = "codex"
	HarnessOpenCode Harness = "opencode"
)

const (
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
	EffortXHigh  Effort = "xhigh"
)

var (
	acceptedTrackers  = []Tracker{TrackerGitHub, TrackerLocal, TrackerGitLab}
	acceptedHarnesses = []Harness{HarnessClaude, HarnessCodex, HarnessOpenCode}
	acceptedEfforts   = []Effort{EffortLow, EffortMedium, EffortHigh, EffortXHigh}
)

const (
	defaultPlanMCP      = true
	defaultImplementMCP = true
	defaultReviewMCP    = false
)

// DefaultWorktreeRoot is the user-level directory where syl creates
// implementation worktrees when a project does not configure another root.
const DefaultWorktreeRoot = "~/.syl/worktrees"

var ErrNotFound = errors.New("syl config not found")

type Tracker string
type Harness string
type Effort string

// IsRemote reports whether the tracker stores tickets outside the local filesystem.
func (t Tracker) IsRemote() bool {
	return t == TrackerGitHub || t == TrackerGitLab
}

// WriteMode controls whether Write creates a new config or replaces an
// existing one.
type WriteMode bool

const (
	NoOverwrite       WriteMode = false
	OverwriteExisting WriteMode = true
)

type Config struct {
	Tracker       TrackerConfig
	Roles         RolesConfig
	Loop          LoopConfig
	Notifications NotificationsConfig
	Worktree      WorktreeConfig
}

type TrackerConfig struct {
	Issues  Tracker
	Reviews Tracker
}

type RolesConfig struct {
	Plan      RoleConfig
	Implement RoleConfig
	Review    RoleConfig
}

type RoleConfig struct {
	Harness Harness
	Model   string
	Effort  Effort
	MCP     bool
}

type LoopConfig struct {
	MaxIterations int
}

type NotificationsConfig struct {
	Enabled bool
}

type WorktreeConfig struct {
	Root  string
	Setup string
	Copy  []string
}

// FieldError describes one configuration validation failure. Message is the
// same text returned by Load for the corresponding invalid value.
type FieldError struct {
	Field   string
	Message string
}

// Error implements error for callers that need to return one field failure.
func (e FieldError) Error() string {
	return e.Message
}

type rawConfig struct {
	Tracker       rawTracker       `toml:"tracker"`
	Roles         rawRoles         `toml:"roles"`
	Loop          rawLoop          `toml:"loop"`
	Notifications rawNotifications `toml:"notifications"`
	Worktree      rawWorktree      `toml:"worktree"`
}

type rawTracker struct {
	Issues  string `toml:"issues"`
	Reviews string `toml:"reviews"`
}

type rawRoles struct {
	Plan      rawRole `toml:"plan"`
	Implement rawRole `toml:"implement"`
	Review    rawRole `toml:"review"`
}

type rawRole struct {
	Harness string `toml:"harness"`
	Model   string `toml:"model"`
	Effort  string `toml:"effort"`
	MCP     any    `toml:"mcp"`
}

type rawLoop struct {
	MaxIterations int `toml:"max_iterations"`
}

type rawNotifications struct {
	Enabled bool `toml:"enabled"`
}

type rawWorktree struct {
	Root  string   `toml:"root"`
	Setup string   `toml:"setup"`
	Copy  []string `toml:"copy"`
}

func Path(projectRoot string) string {
	return filepath.Join(projectRoot, configRelativePath)
}

// Trackers returns the tracker values accepted by Load.
func Trackers() []Tracker {
	return append([]Tracker(nil), acceptedTrackers...)
}

// Harnesses returns the harness values accepted by Load.
func Harnesses() []Harness {
	return append([]Harness(nil), acceptedHarnesses...)
}

// Efforts returns the effort values accepted by Load.
func Efforts() []Effort {
	return append([]Effort(nil), acceptedEfforts...)
}

func Load(projectRoot string) (Config, error) {
	path := Path(projectRoot)
	contents, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("%w: %s; run syl init", ErrNotFound, path)
		}
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}

	var raw rawConfig
	metadata, err := toml.Decode(string(contents), &raw)
	if err != nil {
		return Config{}, fmt.Errorf("malformed TOML in %s: %w", path, err)
	}
	if keys := metadata.Undecoded(); len(keys) > 0 {
		unknown := make([]string, 0, len(keys))
		for _, key := range keys {
			unknown = append(unknown, key.String())
		}
		sort.Strings(unknown)
		return Config{}, FieldError{Field: unknown[0], Message: fmt.Sprintf("unknown config key %q", unknown[0])}
	}

	return validate(raw, metadata)
}

func Init(projectRoot string) (string, error) {
	return Write(projectRoot, defaultConfigValue(), NoOverwrite)
}

// Write writes the project configuration using the exact schema consumed by
// Load. NoOverwrite leaves an existing config untouched.
func Write(projectRoot string, cfg Config, mode WriteMode) (string, error) {
	path := Path(projectRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create .syl directory: %w", err)
	}
	if mode == OverwriteExisting {
		if err := writeExisting(path, Render(cfg)); err != nil {
			return "", err
		}
		return path, nil
	}

	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	file, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("config already exists at %s", path)
		}
		return "", fmt.Errorf("create config %s: %w", path, err)
	}
	defer file.Close()
	if _, err := file.WriteString(Render(cfg)); err != nil {
		return "", fmt.Errorf("write config %s: %w", path, err)
	}
	return path, nil
}

func writeExisting(path, contents string) error {
	fileMode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		fileMode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat config %s: %w", path, err)
	}

	temporary, err := os.CreateTemp(filepath.Dir(path), ".config.toml-*")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	temporaryPath := temporary.Name()
	keepTemporary := false
	defer func() {
		if !keepTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := temporary.Chmod(fileMode); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set temporary config permissions: %w", err)
	}
	if _, err := io.WriteString(temporary, contents); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace config %s: %w", path, err)
	}
	keepTemporary = true
	return nil
}

// Render formats a validated configuration as a committable TOML file.
func Render(cfg Config) string {
	return fmt.Sprintf(`# Generated by syl init.
[tracker]
issues = %q
reviews = %q

[roles.plan]
harness = %q
model = %q
effort = %q
# mcp = true inherits user/project MCP configuration; false strips it for Claude.
# Codex ignores this field. Omitted defaults are true for plan and implement, and false for review.
mcp = %t

[roles.implement]
harness = %q
model = %q
effort = %q
# mcp = true inherits user/project MCP configuration; false strips it for Claude.
# Codex ignores this field. Omitted defaults are true for plan and implement, and false for review.
mcp = %t

[roles.review]
harness = %q
model = %q
effort = %q
# mcp = true inherits user/project MCP configuration; false strips it for Claude.
# Codex ignores this field. Omitted defaults are true for plan and implement, and false for review.
# Hooks that require MCP may cause one blocked-then-retried tool call in lean sessions.
mcp = %t

[loop]
max_iterations = %d

[notifications]
enabled = %t

[worktree]
root = %q
# A shell command used to provision dependencies in a fresh worktree.
# Example: setup = "npm ci"
setup = %q
# Additional origin paths to copy into a fresh worktree.
# Example: copy = [".env", ".env.local"]
copy = %s
`, cfg.Tracker.Issues, cfg.Tracker.Reviews,
		cfg.Roles.Plan.Harness, cfg.Roles.Plan.Model, cfg.Roles.Plan.Effort,
		cfg.Roles.Plan.MCP,
		cfg.Roles.Implement.Harness, cfg.Roles.Implement.Model, cfg.Roles.Implement.Effort,
		cfg.Roles.Implement.MCP,
		cfg.Roles.Review.Harness, cfg.Roles.Review.Model, cfg.Roles.Review.Effort,
		cfg.Roles.Review.MCP,
		cfg.Loop.MaxIterations, cfg.Notifications.Enabled,
		worktreeRoot(cfg.Worktree.Root), cfg.Worktree.Setup, renderStringList(cfg.Worktree.Copy))
}

func renderStringList(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = strconv.Quote(value)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func worktreeRoot(root string) string {
	if strings.TrimSpace(root) == "" {
		return DefaultWorktreeRoot
	}
	return root
}

func defaultConfigValue() Config {
	return Config{
		Tracker: TrackerConfig{Issues: TrackerGitHub, Reviews: TrackerLocal},
		Roles: RolesConfig{
			Plan:      RoleConfig{Harness: HarnessClaude, Model: "claude-opus-5", Effort: EffortHigh, MCP: true},
			Implement: RoleConfig{Harness: HarnessCodex, Model: "gpt-5.6-luna", Effort: EffortXHigh, MCP: true},
			Review:    RoleConfig{Harness: HarnessClaude, Model: "claude-sonnet-5", Effort: EffortMedium},
		},
		Loop:          LoopConfig{MaxIterations: 3},
		Notifications: NotificationsConfig{Enabled: true},
		Worktree:      WorktreeConfig{Root: DefaultWorktreeRoot},
	}
}

func validate(raw rawConfig, metadata toml.MetaData) (Config, error) {
	trackerConfig, err := parseTrackerConfig(raw.Tracker)
	if err != nil {
		return Config{}, err
	}
	rolesConfig, err := parseRolesConfig(raw.Roles)
	if err != nil {
		return Config{}, err
	}

	maxIterations := 3
	if metadata.IsDefined("loop", "max_iterations") {
		maxIterations = raw.Loop.MaxIterations
	}
	if maxIterations < 1 {
		return Config{}, FieldError{
			Field:   "loop.max_iterations",
			Message: fmt.Sprintf("loop.max_iterations must be positive; got %d", maxIterations),
		}
	}

	notificationsEnabled := true
	if metadata.IsDefined("notifications", "enabled") {
		notificationsEnabled = raw.Notifications.Enabled
	}

	worktreeRoot := DefaultWorktreeRoot
	if metadata.IsDefined("worktree", "root") {
		worktreeRoot = strings.TrimSpace(raw.Worktree.Root)
		if worktreeRoot == "" {
			return Config{}, FieldError{Field: "worktree.root", Message: "worktree.root: is required"}
		}
	}

	return Config{
		Tracker:       trackerConfig,
		Roles:         rolesConfig,
		Loop:          LoopConfig{MaxIterations: maxIterations},
		Notifications: NotificationsConfig{Enabled: notificationsEnabled},
		Worktree:      WorktreeConfig{Root: worktreeRoot, Setup: raw.Worktree.Setup, Copy: raw.Worktree.Copy},
	}, nil
}

// ValidationErrors validates a complete configuration and returns every
// field failure in the order Load checks the schema.
func ValidationErrors(cfg Config) []FieldError {
	errors := make([]FieldError, 0)
	issues, issuesErr := parseTracker("tracker.issues", string(cfg.Tracker.Issues))
	if issuesErr != nil {
		errors = append(errors, fieldError("tracker.issues", issuesErr))
	}
	reviews, reviewsErr := parseTracker("tracker.reviews", string(cfg.Tracker.Reviews))
	if reviewsErr != nil {
		errors = append(errors, fieldError("tracker.reviews", reviewsErr))
	}
	if issuesErr == nil && reviewsErr == nil && reviews.IsRemote() && reviews != issues {
		errors = append(errors, FieldError{
			Field:   "tracker.reviews",
			Message: fmt.Sprintf("tracker.reviews = %q and tracker.issues = %q are incompatible; a remote review log needs a matching remote issue tracker", reviews, issues),
		})
	}

	errors = append(errors, validateRoleConfig("roles.plan", cfg.Roles.Plan)...)
	errors = append(errors, validateRoleConfig("roles.implement", cfg.Roles.Implement)...)
	errors = append(errors, validateRoleConfig("roles.review", cfg.Roles.Review)...)
	if cfg.Loop.MaxIterations < 1 {
		errors = append(errors, FieldError{
			Field:   "loop.max_iterations",
			Message: fmt.Sprintf("loop.max_iterations must be positive; got %d", cfg.Loop.MaxIterations),
		})
	}
	if strings.TrimSpace(cfg.Worktree.Root) == "" {
		errors = append(errors, FieldError{Field: "worktree.root", Message: "worktree.root: is required"})
	}
	return errors
}

func fieldError(field string, err error) FieldError {
	return FieldError{Field: field, Message: err.Error()}
}

func validateRoleConfig(prefix string, role RoleConfig) []FieldError {
	errors := make([]FieldError, 0, 3)
	harness, harnessErr := parseEnum(prefix+".harness", string(role.Harness), acceptedHarnesses, "claude, codex, or opencode")
	if harnessErr != nil {
		errors = append(errors, fieldError(prefix+".harness", harnessErr))
	}
	if strings.TrimSpace(role.Model) == "" {
		errors = append(errors, FieldError{Field: prefix + ".model", Message: prefix + ".model: is required"})
	} else if harnessErr == nil && harness == HarnessClaude && !strings.HasPrefix(role.Model, "claude-") {
		errors = append(errors, FieldError{
			Field:   prefix + ".model",
			Message: fmt.Sprintf(`%s.model: invalid value %q; want a model starting with "claude-"`, prefix, role.Model),
		})
	}
	if _, effortErr := parseEnum(prefix+".effort", string(role.Effort), acceptedEfforts, "low, medium, high, or xhigh"); effortErr != nil {
		errors = append(errors, fieldError(prefix+".effort", effortErr))
	}
	return errors
}

func parseTrackerConfig(raw rawTracker) (TrackerConfig, error) {
	issues, err := parseTracker("tracker.issues", raw.Issues)
	if err != nil {
		return TrackerConfig{}, err
	}
	reviews, err := parseTracker("tracker.reviews", raw.Reviews)
	if err != nil {
		return TrackerConfig{}, err
	}
	if reviews.IsRemote() && reviews != issues {
		return TrackerConfig{}, FieldError{
			Field:   "tracker.reviews",
			Message: fmt.Sprintf("tracker.reviews = %q and tracker.issues = %q are incompatible; a remote review log needs a matching remote issue tracker", reviews, issues),
		}
	}
	return TrackerConfig{Issues: issues, Reviews: reviews}, nil
}

func parseRolesConfig(raw rawRoles) (RolesConfig, error) {
	plan, err := parseRole("roles.plan", raw.Plan, defaultPlanMCP)
	if err != nil {
		return RolesConfig{}, err
	}
	implement, err := parseRole("roles.implement", raw.Implement, defaultImplementMCP)
	if err != nil {
		return RolesConfig{}, err
	}
	review, err := parseRole("roles.review", raw.Review, defaultReviewMCP)
	if err != nil {
		return RolesConfig{}, err
	}
	return RolesConfig{Plan: plan, Implement: implement, Review: review}, nil
}

func parseTracker(key, value string) (Tracker, error) {
	return parseEnum(key, value, acceptedTrackers, "github, local, or gitlab")
}

func parseRole(prefix string, raw rawRole, defaultMCP bool) (RoleConfig, error) {
	harness, err := parseEnum(prefix+".harness", raw.Harness,
		acceptedHarnesses, "claude, codex, or opencode")
	if err != nil {
		return RoleConfig{}, err
	}
	if strings.TrimSpace(raw.Model) == "" {
		return RoleConfig{}, FieldError{Field: prefix + ".model", Message: prefix + ".model: is required"}
	}
	if harness == HarnessClaude && !strings.HasPrefix(raw.Model, "claude-") {
		return RoleConfig{}, FieldError{
			Field:   prefix + ".model",
			Message: fmt.Sprintf(`%s.model: invalid value %q; want a model starting with "claude-"`, prefix, raw.Model),
		}
	}

	effort, err := parseEnum(prefix+".effort", raw.Effort,
		acceptedEfforts, "low, medium, high, or xhigh")
	if err != nil {
		return RoleConfig{}, err
	}

	mcp, err := parseOptionalBool(prefix+".mcp", raw.MCP, defaultMCP)
	if err != nil {
		return RoleConfig{}, err
	}

	return RoleConfig{Harness: harness, Model: raw.Model, Effort: effort, MCP: mcp}, nil
}

func parseOptionalBool(key string, value any, defaultValue bool) (bool, error) {
	if value == nil {
		return defaultValue, nil
	}
	enabled, ok := value.(bool)
	if !ok {
		return false, FieldError{Field: key, Message: fmt.Sprintf("%s: invalid value %q; want true or false", key, fmt.Sprint(value))}
	}
	return enabled, nil
}

func parseEnum[T ~string](key, value string, valid []T, want string) (T, error) {
	var zero T
	if strings.TrimSpace(value) == "" {
		return zero, FieldError{Field: key, Message: key + ": is required"}
	}
	for _, candidate := range valid {
		if T(value) == candidate {
			return candidate, nil
		}
	}
	return zero, FieldError{Field: key, Message: fmt.Sprintf("%s: invalid value %q; want %s", key, value, want)}
}
