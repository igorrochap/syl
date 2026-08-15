// Package config loads and validates a project's rig configuration.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

const configRelativePath = ".rig/config.toml"

const (
	TrackerGitHub Tracker = "github"
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

var ErrNotFound = errors.New("rig config not found")

type Tracker string
type Harness string
type Effort string

type Config struct {
	Tracker       TrackerConfig
	Roles         RolesConfig
	Loop          LoopConfig
	Notifications NotificationsConfig
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
}

type LoopConfig struct {
	MaxIterations int
}

type NotificationsConfig struct {
	Enabled bool
}

type rawConfig struct {
	Tracker       rawTracker       `toml:"tracker"`
	Roles         rawRoles         `toml:"roles"`
	Loop          rawLoop          `toml:"loop"`
	Notifications rawNotifications `toml:"notifications"`
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
}

type rawLoop struct {
	MaxIterations int `toml:"max_iterations"`
}

type rawNotifications struct {
	Enabled bool `toml:"enabled"`
}

func Path(projectRoot string) string {
	return filepath.Join(projectRoot, configRelativePath)
}

func Load(projectRoot string) (Config, error) {
	path := Path(projectRoot)
	contents, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("%w: %s; run rig init", ErrNotFound, path)
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
		return Config{}, fmt.Errorf("unknown config key %q", unknown[0])
	}

	return validate(raw, metadata)
}

func Init(projectRoot string) (string, error) {
	path := Path(projectRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create .rig directory: %w", err)
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("config already exists at %s", path)
		}
		return "", fmt.Errorf("create config %s: %w", path, err)
	}
	defer file.Close()
	if _, err := file.WriteString(defaultConfig); err != nil {
		return "", fmt.Errorf("write config %s: %w", path, err)
	}
	return path, nil
}

func validate(raw rawConfig, metadata toml.MetaData) (Config, error) {
	issues, err := parseTracker("tracker.issues", raw.Tracker.Issues)
	if err != nil {
		return Config{}, err
	}
	reviews, err := parseTracker("tracker.reviews", raw.Tracker.Reviews)
	if err != nil {
		return Config{}, err
	}
	plan, err := parseRole("roles.plan", raw.Roles.Plan)
	if err != nil {
		return Config{}, err
	}
	implement, err := parseRole("roles.implement", raw.Roles.Implement)
	if err != nil {
		return Config{}, err
	}
	review, err := parseRole("roles.review", raw.Roles.Review)
	if err != nil {
		return Config{}, err
	}

	maxIterations := 3
	if metadata.IsDefined("loop", "max_iterations") {
		maxIterations = raw.Loop.MaxIterations
	}

	notificationsEnabled := true
	if metadata.IsDefined("notifications", "enabled") {
		notificationsEnabled = raw.Notifications.Enabled
	}

	return Config{
		Tracker: TrackerConfig{Issues: issues, Reviews: reviews},
		Roles: RolesConfig{
			Plan: plan, Implement: implement, Review: review,
		},
		Loop:          LoopConfig{MaxIterations: maxIterations},
		Notifications: NotificationsConfig{Enabled: notificationsEnabled},
	}, nil
}

func parseTracker(key, value string) (Tracker, error) {
	return parseEnum(key, value, []Tracker{TrackerGitHub, TrackerLocal}, "github or local")
}

func parseRole(prefix string, raw rawRole) (RoleConfig, error) {
	harness, err := parseEnum(prefix+".harness", raw.Harness,
		[]Harness{HarnessClaude, HarnessCodex, HarnessOpenCode}, "claude, codex, or opencode")
	if err != nil {
		return RoleConfig{}, err
	}
	if strings.TrimSpace(raw.Model) == "" {
		return RoleConfig{}, fmt.Errorf("%s.model: is required", prefix)
	}

	effort, err := parseEnum(prefix+".effort", raw.Effort,
		[]Effort{EffortLow, EffortMedium, EffortHigh, EffortXHigh}, "low, medium, high, or xhigh")
	if err != nil {
		return RoleConfig{}, err
	}

	return RoleConfig{Harness: harness, Model: raw.Model, Effort: effort}, nil
}

func parseEnum[T ~string](key, value string, valid []T, want string) (T, error) {
	var zero T
	if strings.TrimSpace(value) == "" {
		return zero, fmt.Errorf("%s: is required", key)
	}
	for _, candidate := range valid {
		if T(value) == candidate {
			return candidate, nil
		}
	}
	return zero, fmt.Errorf("%s: invalid value %q; want %s", key, value, want)
}

const defaultConfig = `# Generated by rig init.
[tracker]
issues = "github"
reviews = "local"

[roles.plan]
harness = "claude"
model = "opus-5"
effort = "high"

[roles.implement]
harness = "codex"
model = "gpt-5.6-luna"
effort = "xhigh"

[roles.review]
harness = "claude"
model = "sonnet-5"
effort = "medium"

[loop]
max_iterations = 3

[notifications]
enabled = true
`
