package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	if err == nil || !strings.Contains(err.Error(), `tracker.reviews = "github" requires tracker.issues = "github"`) {
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
