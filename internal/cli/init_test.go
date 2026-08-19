package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/igorrochap/syl/internal/config"
)

const defaultInitPromptCount = 12

func defaultInitInput() *strings.Reader {
	return strings.NewReader(strings.Repeat("\n", defaultInitPromptCount))
}

func initInputWithConfirmation(answer string) *strings.Reader {
	lines := make([]string, defaultInitPromptCount+1)
	lines[defaultInitPromptCount] = answer
	return strings.NewReader(strings.Join(lines, "\n") + "\n")
}

func TestInitBlankDirectoryScaffoldsProject(t *testing.T) {
	root := t.TempDir()
	input := strings.NewReader(strings.Join([]string{
		"go-style",
		"github",
		"local",
		"claude",
		"claude-opus-5",
		"high",
		"codex",
		"gpt-5.6-luna",
		"xhigh",
		"opencode",
		"reviewer",
		"medium",
	}, "\n") + "\n")
	app := New(root, Dependencies{Input: input})
	var stdout, stderr strings.Builder

	code := app.Run(context.Background(), []string{"init"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run() code = %d, want 0; stderr = %q", code, stderr.String())
	}

	for _, skill := range []string{"close-issue", "implement", "refactoring", "tdd"} {
		assertFileExists(t, filepath.Join(root, ".agents", "skills", skill, "SKILL.md"))
	}
	assertFileExists(t, filepath.Join(root, ".agents", "skills", "tdd", "tests.md"))
	assertFileExists(t, filepath.Join(root, ".agents", "skills", "go-style", "SKILL.md"))
	lock := readTestSkillsLock(t, root)
	if lock.Version != 1 || lock.Skills["close-issue"] == "" || lock.Skills["go-style"] == "" {
		t.Fatalf("skills lock = %#v, want hashes for installed core and optional skills", lock)
	}
	if _, ok := lock.Skills["prototype"]; ok {
		t.Fatal("skills lock contains an uninstalled optional skill")
	}

	assertSymlink(t, filepath.Join(root, ".claude"), ".agents")
	assertSymlink(t, filepath.Join(root, "CLAUDE.md"), "AGENTS.md")
	assertFileExists(t, filepath.Join(root, "AGENTS.md"))

	got, err := config.Load(root)
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	if got.Tracker.Issues != config.TrackerGitHub || got.Tracker.Reviews != config.TrackerLocal {
		t.Fatalf("tracker = %#v, want github/local", got.Tracker)
	}
	if got.Roles.Plan.Harness != config.HarnessClaude || got.Roles.Plan.Model != "claude-opus-5" || got.Roles.Plan.Effort != config.EffortHigh {
		t.Fatalf("plan role = %#v, want configured values", got.Roles.Plan)
	}
	if !got.Roles.Plan.MCP {
		t.Fatal("plan role MCP = false, want true in generated config")
	}
	if got.Roles.Implement.Harness != config.HarnessCodex || got.Roles.Implement.Model != "gpt-5.6-luna" || got.Roles.Implement.Effort != config.EffortXHigh {
		t.Fatalf("implement role = %#v, want configured values", got.Roles.Implement)
	}
	if !got.Roles.Implement.MCP {
		t.Fatal("implement role MCP = false, want true in generated config")
	}
	if got.Roles.Review.Harness != config.HarnessOpenCode || got.Roles.Review.Model != "reviewer" || got.Roles.Review.Effort != config.EffortMedium {
		t.Fatalf("review role = %#v, want configured values", got.Roles.Review)
	}
	if got.Roles.Review.MCP {
		t.Fatal("review role MCP = true, want false in generated config")
	}
	generated, err := os.ReadFile(config.Path(root))
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}
	for _, expected := range []string{
		"mcp = true",
		"mcp = false",
		"user/project MCP configuration",
		"Codex ignores this field",
		"blocked-then-retried tool call",
	} {
		if !strings.Contains(string(generated), expected) {
			t.Fatalf("generated config = %q, want %q", generated, expected)
		}
	}
}

func TestInitKeyboardTUIAcceptsDefaultsAndTogglesSelections(t *testing.T) {
	root := t.TempDir()
	input := strings.NewReader(" \n\n\x1b[B\n" + strings.Repeat("\n", 9))
	app := New(root, Dependencies{Input: input})
	var stdout, stderr strings.Builder

	code := app.Run(context.Background(), []string{"init"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run() code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".agents", "skills", "go-style", "SKILL.md")); err != nil {
		t.Fatalf("keyboard multi-select did not install go-style: %v", err)
	}
	got, err := config.Load(root)
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	if got.Tracker.Reviews != config.TrackerGitHub {
		t.Fatalf("Tracker.Reviews = %q, want github after arrow-key selection", got.Tracker.Reviews)
	}
}

func TestInitAbortsWithoutChangesWhenClaudeDirectoryExists(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	app := New(root, Dependencies{Input: strings.NewReader("")})
	var stdout, stderr strings.Builder

	code := app.Run(context.Background(), []string{"init"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("Run() code = 0, want failure")
	}
	if !strings.Contains(stderr.String(), "real directory") {
		t.Fatalf("stderr = %q, want real-directory guidance", stderr.String())
	}
	for _, path := range []string{".agents", ".syl", "AGENTS.md", "CLAUDE.md"} {
		if _, err := os.Lstat(filepath.Join(root, path)); !os.IsNotExist(err) {
			t.Fatalf("%s exists after abort; want no changes", path)
		}
	}
}

func TestInitCompletesInNonGitDirectoryWithoutGitignore(t *testing.T) {
	root := t.TempDir()
	app := New(root, Dependencies{Input: defaultInitInput()})
	var stdout, stderr strings.Builder

	code := app.Run(context.Background(), []string{"init"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run() code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if _, err := os.Lstat(filepath.Join(root, ".gitignore")); !os.IsNotExist(err) {
		t.Fatalf(".gitignore exists in non-git directory; want init to skip git handling")
	}
}

func TestInitRerunShowsChangesAndDoesNotModifyWithoutConfirmation(t *testing.T) {
	root := t.TempDir()
	first := New(root, Dependencies{Input: defaultInitInput()})
	var firstOut, firstErr strings.Builder
	if code := first.Run(context.Background(), []string{"init"}, &firstOut, &firstErr); code != 0 {
		t.Fatalf("initial init code = %d; stderr = %q", code, firstErr.String())
	}
	original, err := os.ReadFile(config.Path(root))
	if err != nil {
		t.Fatal(err)
	}

	runAgainInput := strings.NewReader(strings.Join([]string{
		"", "local", "github", "codex", "new-model", "low",
		"", "", "", "", "", "", "no",
	}, "\n") + "\n")
	again := New(root, Dependencies{Input: runAgainInput})
	var stdout, stderr strings.Builder
	code := again.Run(context.Background(), []string{"init"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("rerun code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Changes:") || !strings.Contains(stdout.String(), ".syl/config.toml") {
		t.Fatalf("stdout = %q, want changed config and confirmation prompt", stdout.String())
	}
	current, err := os.ReadFile(config.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, original) {
		t.Fatalf("config changed after rejecting rerun; before %q, after %q", original, current)
	}
}

func TestInitRerunConfirmsSkillOverwriteBeforeChangingIt(t *testing.T) {
	root := t.TempDir()
	first := New(root, Dependencies{Input: defaultInitInput()})
	var firstOut, firstErr strings.Builder
	if code := first.Run(context.Background(), []string{"init"}, &firstOut, &firstErr); code != 0 {
		t.Fatalf("initial init code = %d; stderr = %q", code, firstErr.String())
	}

	skillPath := filepath.Join(root, ".agents", "skills", "close-issue", "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("user changes"), 0o644); err != nil {
		t.Fatal(err)
	}
	again := New(root, Dependencies{Input: initInputWithConfirmation("no")})
	var stdout, stderr strings.Builder
	code := again.Run(context.Background(), []string{"init"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("rerun code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "overwrite skill close-issue") {
		t.Fatalf("stdout = %q, want skill overwrite in change summary", stdout.String())
	}
	contents, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(contents, []byte("user changes")) {
		t.Fatalf("skill changed after rejecting overwrite: %q", contents)
	}
}

func TestInitEnsuresRunsAreGitignoredExactlyOnce(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("dist/\n.syl/runs/\n.syl/runs/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := New(root, Dependencies{Input: defaultInitInput()})
	var stdout, stderr strings.Builder

	code := app.Run(context.Background(), []string{"init"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run() code = %d, want 0; stderr = %q", code, stderr.String())
	}
	contents, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(contents), ".syl/runs/"); got != 1 {
		t.Fatalf(".gitignore contains .syl/runs/ %d times, want exactly once: %q", got, contents)
	}
	if !strings.Contains(string(contents), "dist/") {
		t.Fatalf(".gitignore lost existing entry: %q", contents)
	}
}

func assertFileExists(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.IsDir() {
		t.Fatalf("%s is a directory, want file", path)
	}
}

func assertSymlink(t *testing.T, path, wantTarget string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is not a symlink", path)
	}
	gotTarget, err := os.Readlink(path)
	if err != nil {
		t.Fatalf("readlink %s: %v", path, err)
	}
	if gotTarget != wantTarget {
		t.Fatalf("readlink %s = %q, want %q", path, gotTarget, wantTarget)
	}
}
