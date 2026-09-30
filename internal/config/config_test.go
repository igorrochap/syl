package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestTrackerIsRemote(t *testing.T) {
	tests := []struct {
		name    string
		tracker Tracker
		want    bool
	}{
		{name: "GitHub", tracker: TrackerGitHub, want: true},
		{name: "GitLab", tracker: TrackerGitLab, want: true},
		{name: "local", tracker: TrackerLocal, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.tracker.IsRemote(); got != test.want {
				t.Fatalf("Tracker(%q).IsRemote() = %t, want %t", test.tracker, got, test.want)
			}
		})
	}
}

func TestAcceptedValuesAndFieldError(t *testing.T) {
	if got, want := Trackers(), []Tracker{TrackerGitHub, TrackerLocal, TrackerGitLab}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Trackers() = %#v, want %#v", got, want)
	}
	if got, want := Harnesses(), []Harness{HarnessClaude, HarnessCodex, HarnessOpenCode}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Harnesses() = %#v, want %#v", got, want)
	}
	if got, want := Efforts(), []Effort{EffortLow, EffortMedium, EffortHigh, EffortXHigh}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Efforts() = %#v, want %#v", got, want)
	}
	if got := (FieldError{Field: "loop.max_iterations", Message: "must be positive"}).Error(); got != "must be positive" {
		t.Fatalf("FieldError.Error() = %q, want message", got)
	}
}

func TestValidationErrorsMatchesLoadRules(t *testing.T) {
	valid := defaultConfigValue()
	if got := ValidationErrors(valid); len(got) != 0 {
		t.Fatalf("ValidationErrors(valid) = %#v, want no errors", got)
	}

	invalid := valid
	invalid.Tracker.Issues = Tracker("jira")
	invalid.Tracker.Reviews = Tracker("github")
	invalid.Roles.Plan = RoleConfig{Harness: Harness("gemini"), Effort: Effort("extreme")}
	invalid.Roles.Implement = RoleConfig{Harness: HarnessClaude, Model: "gpt-5", Effort: EffortLow}
	invalid.Roles.Review = RoleConfig{Harness: HarnessCodex, Model: "reviewer", Effort: Effort("extreme")}
	invalid.Loop.MaxIterations = 0
	invalid.Worktree.Root = "  "

	got := ValidationErrors(invalid)
	want := map[string]string{
		"tracker.issues":        `tracker.issues: invalid value "jira"; want github, local, or gitlab`,
		"roles.plan.harness":    `roles.plan.harness: invalid value "gemini"; want claude, codex, or opencode`,
		"roles.plan.model":      "roles.plan.model: is required",
		"roles.plan.effort":     `roles.plan.effort: invalid value "extreme"; want low, medium, high, or xhigh`,
		"roles.implement.model": `roles.implement.model: invalid value "gpt-5"; want a model starting with "claude-"`,
		"roles.review.effort":   `roles.review.effort: invalid value "extreme"; want low, medium, high, or xhigh`,
		"loop.max_iterations":   "loop.max_iterations must be positive; got 0",
		"worktree.root":         "worktree.root: is required",
	}
	if len(got) != len(want) {
		t.Fatalf("ValidationErrors() returned %d errors, want %d: %#v", len(got), len(want), got)
	}
	for _, fieldError := range got {
		if fieldError.Message != want[fieldError.Field] {
			t.Errorf("ValidationErrors()[%q] = %q, want %q", fieldError.Field, fieldError.Message, want[fieldError.Field])
		}
		delete(want, fieldError.Field)
	}
	if len(want) != 0 {
		t.Fatalf("ValidationErrors() omitted fields: %#v", want)
	}

	remoteMismatch := valid
	remoteMismatch.Tracker.Issues = TrackerLocal
	remoteMismatch.Tracker.Reviews = TrackerGitHub
	got = ValidationErrors(remoteMismatch)
	if len(got) != 1 || got[0].Field != "tracker.reviews" || !strings.Contains(got[0].Message, "incompatible") {
		t.Fatalf("ValidationErrors(remote mismatch) = %#v, want tracker.reviews incompatibility", got)
	}
}

func TestLoadAcceptsGitLabForIssuesAndRemoteReviews(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, strings.Replace(
		configWithRoleValue("plan", "model", "claude-planner"),
		`issues = "github"`, `issues = "gitlab"`, 1,
	))

	got, err := Load(root)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Tracker.Issues != TrackerGitLab || got.Tracker.Reviews != TrackerLocal {
		t.Fatalf("Tracker = %#v, want gitlab/local", got.Tracker)
	}

	contents := strings.Replace(
		configWithRoleValue("plan", "model", "claude-planner"),
		`issues = "github"`, `issues = "gitlab"`, 1,
	)
	contents = strings.Replace(contents, `reviews = "local"`, `reviews = "gitlab"`, 1)
	writeConfig(t, root, contents)
	got, err = Load(root)
	if err != nil {
		t.Fatalf("Load() with GitLab review error = %v", err)
	}
	if got.Tracker.Issues != TrackerGitLab || got.Tracker.Reviews != TrackerGitLab {
		t.Fatalf("Tracker = %#v, want gitlab/gitlab", got.Tracker)
	}
}

func TestLoadRejectsMismatchedGitHubAndGitLabTrackers(t *testing.T) {
	root := t.TempDir()
	contents := strings.Replace(
		configWithRoleValue("plan", "model", "claude-planner"),
		`issues = "github"`, `issues = "gitlab"`, 1,
	)
	contents = strings.Replace(contents, `reviews = "local"`, `reviews = "github"`, 1)
	writeConfig(t, root, contents)

	_, err := Load(root)
	if err == nil || !strings.Contains(err.Error(), `tracker.reviews = "github" and tracker.issues = "gitlab" are incompatible`) {
		t.Fatalf("Load() error = %v, want mismatched remote tracker error", err)
	}
}

func TestLoadAppliesDefaultsAndKeepsTrackerSourcesIndependent(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
[tracker]
issues = "github"
reviews = "local"

[roles.plan]
harness = "claude"
model = "claude-opus-5"
effort = "high"

[roles.implement]
harness = "codex"
model = "gpt-5.6-luna"
effort = "xhigh"

[roles.review]
harness = "claude"
model = "claude-sonnet-5"
effort = "medium"
`)

	got, err := Load(root)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got.Tracker.Issues != TrackerGitHub {
		t.Fatalf("Tracker.Issues = %q, want %q", got.Tracker.Issues, TrackerGitHub)
	}
	if got.Tracker.Reviews != TrackerLocal {
		t.Fatalf("Tracker.Reviews = %q, want %q", got.Tracker.Reviews, TrackerLocal)
	}
	if got.Loop.MaxIterations != 3 {
		t.Fatalf("Loop.MaxIterations = %d, want default 3", got.Loop.MaxIterations)
	}
	if !got.Notifications.Enabled {
		t.Fatal("Notifications.Enabled = false, want default true")
	}
	if got.Roles.Implement.Harness != HarnessCodex {
		t.Fatalf("Roles.Implement.Harness = %q, want %q", got.Roles.Implement.Harness, HarnessCodex)
	}
	if !got.Roles.Plan.MCP {
		t.Fatal("Roles.Plan.MCP = false, want true by default")
	}
	if !got.Roles.Implement.MCP {
		t.Fatal("Roles.Implement.MCP = false, want true by default")
	}
	if got.Roles.Review.MCP {
		t.Fatal("Roles.Review.MCP = true, want false by default")
	}
	if got.Worktree.Root != DefaultWorktreeRoot {
		t.Fatalf("Worktree.Root = %q, want default %q", got.Worktree.Root, DefaultWorktreeRoot)
	}
}

func TestLoadHonorsWorktreeRoot(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, configWithRoleValue("plan", "model", "claude-planner")+"\n[worktree]\nroot = \"/tmp/syl-worktrees\"\n")

	got, err := Load(root)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Worktree.Root != "/tmp/syl-worktrees" {
		t.Fatalf("Worktree.Root = %q, want explicit root", got.Worktree.Root)
	}
}

func TestLoadHonorsWorktreeSetupCommand(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, configWithRoleValue("plan", "model", "claude-planner")+
		"\n[worktree]\nsetup = \"npm ci && echo dependencies-ready\"\n")

	got, err := Load(root)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Worktree.Setup != "npm ci && echo dependencies-ready" {
		t.Fatalf("Worktree.Setup = %q, want one shell command string", got.Worktree.Setup)
	}
}

func TestLoadHonorsWorktreeCopyPaths(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, configWithRoleValue("plan", "model", "claude-planner")+
		"\n[worktree]\ncopy = [\".env\", \".env.local\", \"fixtures/seed.db\"]\n")

	got, err := Load(root)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := []string{".env", ".env.local", "fixtures/seed.db"}
	if !reflect.DeepEqual(got.Worktree.Copy, want) {
		t.Fatalf("Worktree.Copy = %#v, want %#v", got.Worktree.Copy, want)
	}
}

func TestLoadLeavesAbsentWorktreeSetupEmpty(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, configWithRoleValue("plan", "model", "claude-planner"))

	got, err := Load(root)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Worktree.Setup != "" {
		t.Fatalf("Worktree.Setup = %q, want empty when absent", got.Worktree.Setup)
	}
}

func TestLoadRejectsEmptyWorktreeRoot(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, configWithRoleValue("plan", "model", "claude-planner")+"\n[worktree]\nroot = \"   \"\n")

	_, err := Load(root)
	if err == nil || !strings.Contains(err.Error(), "worktree.root: is required") {
		t.Fatalf("Load() error = %v, want required worktree root", err)
	}
}

func TestLoadHonorsExplicitOptionalValues(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
[tracker]
issues = "github"
reviews = "github"

[roles.plan]
harness = "opencode"
model = "planner"
effort = "low"

[roles.implement]
harness = "codex"
model = "implementer"
effort = "medium"

[roles.review]
harness = "claude"
model = "claude-reviewer"
effort = "xhigh"
mcp = true

[loop]
max_iterations = 7

[notifications]
enabled = false
`)

	got, err := Load(root)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Loop.MaxIterations != 7 {
		t.Fatalf("Loop.MaxIterations = %d, want 7", got.Loop.MaxIterations)
	}
	if got.Notifications.Enabled {
		t.Fatal("Notifications.Enabled = true, want false")
	}
	if !got.Roles.Review.MCP {
		t.Fatal("Roles.Review.MCP = false, want true when explicitly enabled")
	}
}

func TestLoadHonorsExplicitMCPDisableForNonReviewRole(t *testing.T) {
	root := t.TempDir()
	contents := strings.Replace(configWithRoleValue("plan", "model", "claude-planner"),
		"effort = \"high\"", "effort = \"high\"\nmcp = false", 1)
	writeConfig(t, root, contents)

	got, err := Load(root)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Roles.Plan.MCP {
		t.Fatal("Roles.Plan.MCP = true, want false when explicitly disabled")
	}
}

func TestLoadRejectsClaudeModelWithoutProviderPrefix(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, configWithRoleValue("review", "model", "sonnet-5"))

	_, err := Load(root)
	if err == nil || !strings.Contains(err.Error(), `roles.review.model: invalid value "sonnet-5"; want a model starting with "claude-"`) {
		t.Fatalf("Load() error = %v, want Claude model prefix validation", err)
	}
}

func TestLoadRejectsLocalIssuesWithGitHubReviewLog(t *testing.T) {
	root := t.TempDir()
	contents := strings.Replace(configWithRoleValue("plan", "model", "planner"), `issues = "github"`, `issues = "local"`, 1)
	contents = strings.Replace(contents, `reviews = "local"`, `reviews = "github"`, 1)
	writeConfig(t, root, contents)

	_, err := Load(root)
	if err == nil || !strings.Contains(err.Error(), `tracker.reviews = "github" and tracker.issues = "local" are incompatible; a remote review log needs a matching remote issue tracker`) {
		t.Fatalf("Load() error = %v, want clear incompatible tracker error", err)
	}
}

func TestLoadRejectsInvalidValuesWithOffendingKey(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		wantKey string
	}{
		{
			name: "issues tracker",
			config: `
[tracker]
issues = "jira"
reviews = "local"
`,
			wantKey: "tracker.issues",
		},
		{
			name: "reviews tracker",
			config: `
[tracker]
issues = "github"
reviews = "jira"
`,
			wantKey: "tracker.reviews",
		},
		{
			name: "harness",
			config: `
[tracker]
issues = "github"
reviews = "local"

[roles.plan]
harness = "gemini"
model = "opus-5"
effort = "high"
`,
			wantKey: "roles.plan.harness",
		},
		{
			name: "effort",
			config: `
[tracker]
issues = "github"
reviews = "local"

[roles.plan]
harness = "claude"
model = "claude-opus-5"
effort = "extreme"
`,
			wantKey: "roles.plan.effort",
		},
		{
			name: "mcp",
			config: `
[tracker]
issues = "github"
reviews = "local"

[roles.plan]
harness = "claude"
model = "claude-opus-5"
effort = "high"
mcp = "sometimes"
`,
			wantKey: "roles.plan.mcp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeConfig(t, root, tt.config)

			_, err := Load(root)
			if err == nil {
				t.Fatal("Load() error = nil, want validation error")
			}
			if !strings.Contains(err.Error(), tt.wantKey) {
				t.Fatalf("Load() error = %q, want key %q", err, tt.wantKey)
			}
			var fieldError FieldError
			if !errors.As(err, &fieldError) || fieldError.Field != tt.wantKey {
				t.Fatalf("Load() error = %T, want FieldError for %q", err, tt.wantKey)
			}
			if strings.Contains(tt.wantKey, "tracker.") {
				for _, trackerName := range []string{"github", "local", "gitlab"} {
					if !strings.Contains(err.Error(), trackerName) {
						t.Fatalf("Load() error = %q, want accepted tracker %q", err, trackerName)
					}
				}
			}
		})
	}
}

func TestLoadRejectsInvalidHarnessAndEffortForEveryRole(t *testing.T) {
	for _, role := range []string{"plan", "implement", "review"} {
		for _, test := range []struct {
			field string
			value string
		}{
			{field: "harness", value: "gemini"},
			{field: "effort", value: "extreme"},
		} {
			t.Run(role+"/"+test.field, func(t *testing.T) {
				root := t.TempDir()
				writeConfig(t, root, configWithRoleValue(role, test.field, test.value))

				_, err := Load(root)
				if err == nil {
					t.Fatal("Load() error = nil, want validation error")
				}
				wantKey := "roles." + role + "." + test.field
				if !strings.Contains(err.Error(), wantKey) {
					t.Fatalf("Load() error = %q, want key %q", err, wantKey)
				}
			})
		}
	}
}

func TestLoadRejectsMissingRoleValuesAndInvalidLoop(t *testing.T) {
	tests := []struct {
		name    string
		before  string
		after   string
		wantKey string
	}{
		{name: "missing model", before: `model = "gpt-5.6-luna"`, after: `model = ""`, wantKey: "roles.implement.model"},
		{name: "missing effort", before: `effort = "xhigh"`, after: `effort = ""`, wantKey: "roles.implement.effort"},
		{name: "invalid loop", before: "max_iterations = 3", after: "max_iterations = 0", wantKey: "loop.max_iterations"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if _, err := Init(root); err != nil {
				t.Fatal(err)
			}
			contents, err := os.ReadFile(Path(root))
			if err != nil {
				t.Fatal(err)
			}
			contents = []byte(strings.Replace(string(contents), tt.before, tt.after, 1))
			if err := os.WriteFile(Path(root), contents, 0o644); err != nil {
				t.Fatal(err)
			}

			_, err = Load(root)
			var fieldError FieldError
			if !errors.As(err, &fieldError) || fieldError.Field != tt.wantKey {
				t.Fatalf("Load() error = %#v, want FieldError for %q", err, tt.wantKey)
			}
		})
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
[tracker]
issues = "github"
reviews = "local"
unknown = true
`)

	_, err := Load(root)
	if err == nil {
		t.Fatal("Load() error = nil, want unknown-key error")
	}
	if !strings.Contains(err.Error(), "tracker.unknown") {
		t.Fatalf("Load() error = %q, want unknown key", err)
	}
}

func TestLoadReportsMalformedTOML(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "[tracker\nissues = \"github\"\n")

	_, err := Load(root)
	if err == nil {
		t.Fatal("Load() error = nil, want parse error")
	}
	if !strings.Contains(err.Error(), "malformed TOML") {
		t.Fatalf("Load() error = %q, want malformed TOML", err)
	}
}

func TestOverwriteWriteNeverExposesPartialConfig(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root); err != nil {
		t.Fatal(err)
	}
	first, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	second := first
	second.Loop.MaxIterations = first.Loop.MaxIterations + 1

	started := make(chan struct{})
	done := make(chan struct{})
	writeErrors := make(chan error, 1)
	go func() {
		close(started)
		for index := 0; index < 100; index++ {
			value := first
			if index%2 == 1 {
				value = second
			}
			if _, err := Write(root, value, OverwriteExisting); err != nil {
				writeErrors <- err
				return
			}
		}
		close(done)
	}()
	<-started
	for {
		contents, err := os.ReadFile(Path(root))
		if err != nil {
			t.Fatal(err)
		}
		if len(contents) == 0 {
			t.Fatal("atomic overwrite exposed an empty config")
		}
		if _, err := Load(root); err != nil {
			t.Fatalf("atomic overwrite exposed invalid config: %v", err)
		}
		select {
		case err := <-writeErrors:
			t.Fatal(err)
		case <-done:
			return
		default:
		}
	}
}

func TestWriteRejectsExistingConfigWithoutOverwrite(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(root, defaultConfigValue(), NoOverwrite); err == nil || !strings.Contains(err.Error(), "config already exists") {
		t.Fatalf("Write(NoOverwrite) error = %v, want existing-config error", err)
	}
}

func TestOverwriteWriteReportsReplacementError(t *testing.T) {
	root := t.TempDir()
	path := Path(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(root, defaultConfigValue(), OverwriteExisting); err == nil || !strings.Contains(err.Error(), "replace config") {
		t.Fatalf("Write(OverwriteExisting) error = %v, want replacement error", err)
	}
}

func writeConfig(t *testing.T, root, contents string) {
	t.Helper()
	path := filepath.Join(root, ".syl", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(contents)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func configWithRoleValue(role, field, value string) string {
	roles := map[string]map[string]string{
		"plan": {
			"harness": "claude",
			"model":   "claude-planner",
			"effort":  "high",
		},
		"implement": {
			"harness": "codex",
			"model":   "implementer",
			"effort":  "xhigh",
		},
		"review": {
			"harness": "claude",
			"model":   "claude-reviewer",
			"effort":  "medium",
		},
	}
	roles[role][field] = value

	return fmt.Sprintf(`[tracker]
issues = "github"
reviews = "local"

[roles.plan]
harness = %q
model = %q
effort = %q

[roles.implement]
harness = %q
model = %q
effort = %q

[roles.review]
harness = %q
model = %q
effort = %q
`,
		roles["plan"]["harness"], roles["plan"]["model"], roles["plan"]["effort"],
		roles["implement"]["harness"], roles["implement"]["model"], roles["implement"]["effort"],
		roles["review"]["harness"], roles["review"]["model"], roles["review"]["effort"],
	)
}
