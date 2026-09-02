package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
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
	app := New(root, root, Dependencies{Input: input})
	var stdout, stderr strings.Builder

	code := app.Run(context.Background(), []string{"init"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run() code = %d, want 0; stderr = %q", code, stderr.String())
	}

	for _, skill := range []string{"close-issue", "code-quality", "implement", "refactoring", "tdd"} {
		assertFileExists(t, filepath.Join(root, ".agents", "skills", skill, "SKILL.md"))
	}
	assertFileExists(t, filepath.Join(root, ".agents", "skills", "tdd", "tests.md"))
	assertFileExists(t, filepath.Join(root, ".agents", "skills", "code-quality", "references", "gate-contract.md"))
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
	assertExecutableFile(t, filepath.Join(root, "scripts", "quality.sh"))

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

func TestInitQualityScriptDispatchesGates(t *testing.T) {
	root := initQualityScriptProject(t)

	listOutput, listError, listCode := runQualityScript(t, root, "--list")
	if listCode != 0 || listError != "" {
		t.Fatalf("quality.sh --list = code %d, stderr %q; want success", listCode, listError)
	}
	wantGates := "format\nvet\nstyle\ncomplexity\ncoverage\ntests\narchitecture\n"
	if listOutput != wantGates {
		t.Fatalf("quality.sh --list = %q, want %q", listOutput, wantGates)
	}

	allOutput, allError, allCode := runQualityScript(t, root)
	if allCode != 0 || allError != "" {
		t.Fatalf("quality.sh = code %d, stderr %q; want success", allCode, allError)
	}
	if strings.Count(allOutput, "SKIP") != 7 {
		t.Fatalf("quality.sh output = %q, want seven skipped gates", allOutput)
	}

	oneOutput, oneError, oneCode := runQualityScript(t, root, "format")
	if oneCode != 0 || oneError != "" {
		t.Fatalf("quality.sh format = code %d, stderr %q; want success", oneCode, oneError)
	}
	if strings.Count(oneOutput, "SKIP") != 1 || strings.Contains(oneOutput, "  vet") {
		t.Fatalf("quality.sh format output = %q, want only format", oneOutput)
	}

	unknownOutput, unknownError, unknownCode := runQualityScript(t, root, "typo")
	if unknownCode == 0 || unknownError != "" {
		t.Fatalf("quality.sh typo = code %d, stderr %q; want non-zero stdout error", unknownCode, unknownError)
	}
	for _, gate := range []string{"format", "vet", "style", "complexity", "coverage", "tests", "architecture"} {
		if !strings.Contains(unknownOutput, gate) {
			t.Fatalf("quality.sh typo output = %q, want known gate %q", unknownOutput, gate)
		}
	}
}

func TestQualityScriptContinuesAfterFailureAndWritesGitHubSummary(t *testing.T) {
	root := initQualityScriptProject(t)
	qualityPath := filepath.Join(root, "scripts", "quality.sh")
	contents, err := os.ReadFile(qualityPath)
	if err != nil {
		t.Fatal(err)
	}
	contentsString := strings.Replace(string(contents),
		"format() {\n  # Put the format command for this project here.\n  printf '%s\\n' \"SKIP  not configured\"",
		"format() {\n  return 1", 1)
	if contentsString == string(contents) {
		t.Fatal("did not configure format gate to fail")
	}
	if err := os.WriteFile(qualityPath, []byte(contentsString), 0o755); err != nil {
		t.Fatal(err)
	}

	failureOutput, failureError, failureCode := runQualityScript(t, root)
	if failureCode == 0 || failureError != "" {
		t.Fatalf("quality.sh after format failure = code %d, stderr %q; want non-zero stdout report", failureCode, failureError)
	}
	if !strings.Contains(failureOutput, "format        FAIL") || !strings.Contains(failureOutput, "architecture  SKIP") {
		t.Fatalf("quality.sh after format failure = %q, want failure and final gate", failureOutput)
	}
	if strings.Count(failureOutput, "SKIP") != 6 {
		t.Fatalf("quality.sh after format failure = %q, want six skipped gates", failureOutput)
	}

	summaryPath := filepath.Join(root, "quality-summary.md")
	summaryOutput, summaryError, summaryCode := runQualityScriptWithEnvironment(t, root, []string{"GITHUB_STEP_SUMMARY=" + summaryPath})
	if summaryCode == 0 || summaryError != "" {
		t.Fatalf("quality.sh with GitHub summary = code %d, stderr %q; want non-zero stdout report", summaryCode, summaryError)
	}
	summaryContents, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("read GitHub summary: %v", err)
	}
	if string(summaryContents) != summaryOutput {
		t.Fatalf("GitHub summary = %q, stdout = %q; want identical reports", summaryContents, summaryOutput)
	}

	_, emptySummaryError, emptySummaryCode := runQualityScriptWithEnvironment(t, root, []string{"GITHUB_STEP_SUMMARY="})
	if emptySummaryCode == 0 || emptySummaryError != "" {
		t.Fatalf("quality.sh with empty GitHub summary = code %d, stderr %q; want non-zero gate result only", emptySummaryCode, emptySummaryError)
	}
}

func initQualityScriptProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	app := New(root, root, Dependencies{Input: defaultInitInput()})
	var stdout, stderr strings.Builder
	if code := app.Run(context.Background(), []string{"init"}, &stdout, &stderr); code != 0 {
		t.Fatalf("init code = %d; stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "write scripts/quality.sh") {
		t.Fatalf("init output = %q, want quality script in plan", stdout.String())
	}
	return root
}

func runQualityScript(t *testing.T, root string, args ...string) (string, string, int) {
	t.Helper()
	return runQualityScriptWithEnvironment(t, root, nil, args...)
}

func runQualityScriptWithEnvironment(t *testing.T, root string, environment []string, args ...string) (string, string, int) {
	t.Helper()
	command := exec.Command(filepath.Join(root, "scripts", "quality.sh"), args...)
	command.Dir = root
	if environment != nil {
		command.Env = append(os.Environ(), environment...)
	}
	var stdout, stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		return stdout.String(), stderr.String(), 0
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("run quality.sh: %v", err)
	}
	return stdout.String(), stderr.String(), exitError.ExitCode()
}

func TestInitKeyboardTUIAcceptsDefaultsAndTogglesSelections(t *testing.T) {
	root := t.TempDir()
	input := strings.NewReader(" \n\n\x1b[A\n" + strings.Repeat("\n", 9))
	app := New(root, root, Dependencies{Input: input})
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
	app := New(root, root, Dependencies{Input: strings.NewReader("")})
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
	app := New(root, root, Dependencies{Input: defaultInitInput()})
	var stdout, stderr strings.Builder

	code := app.Run(context.Background(), []string{"init"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run() code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if _, err := os.Lstat(filepath.Join(root, ".gitignore")); !os.IsNotExist(err) {
		t.Fatalf(".gitignore exists in non-git directory; want init to skip git handling")
	}
}

func TestInitDoesNotReplaceExistingQualityScript(t *testing.T) {
	root := t.TempDir()
	qualityPath := filepath.Join(root, "scripts", "quality.sh")
	if err := os.MkdirAll(filepath.Dir(qualityPath), 0o755); err != nil {
		t.Fatal(err)
	}
	want := []byte("#!/bin/sh\necho user gate\n")
	if err := os.WriteFile(qualityPath, want, 0o644); err != nil {
		t.Fatal(err)
	}

	app := New(root, root, Dependencies{Input: defaultInitInput()})
	var stdout, stderr strings.Builder
	if code := app.Run(context.Background(), []string{"init"}, &stdout, &stderr); code != 0 {
		t.Fatalf("init code = %d; stderr = %q", code, stderr.String())
	}
	got, err := os.ReadFile(qualityPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("quality script changed from %q to %q", want, got)
	}
	if strings.Contains(stdout.String(), "write scripts/quality.sh") {
		t.Fatalf("init output = %q, want no quality-script write", stdout.String())
	}
}

func TestInitRerunAddsMissingQualityScript(t *testing.T) {
	root := t.TempDir()
	first := New(root, root, Dependencies{Input: defaultInitInput()})
	var firstOut, firstErr strings.Builder
	if code := first.Run(context.Background(), []string{"init"}, &firstOut, &firstErr); code != 0 {
		t.Fatalf("initial init code = %d; stderr = %q", code, firstErr.String())
	}
	qualityPath := filepath.Join(root, "scripts", "quality.sh")
	if err := os.Remove(qualityPath); err != nil {
		t.Fatal(err)
	}

	again := New(root, root, Dependencies{Input: initInputWithConfirmation("yes")})
	var stdout, stderr strings.Builder
	if code := again.Run(context.Background(), []string{"init"}, &stdout, &stderr); code != 0 {
		t.Fatalf("rerun code = %d; stderr = %q", code, stderr.String())
	}
	assertExecutableFile(t, qualityPath)
	if !strings.Contains(stdout.String(), "write scripts/quality.sh") {
		t.Fatalf("rerun output = %q, want quality script in plan", stdout.String())
	}
}

func TestInitRerunShowsChangesAndDoesNotModifyWithoutConfirmation(t *testing.T) {
	root := t.TempDir()
	first := New(root, root, Dependencies{Input: defaultInitInput()})
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
	again := New(root, root, Dependencies{Input: runAgainInput})
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
	first := New(root, root, Dependencies{Input: defaultInitInput()})
	var firstOut, firstErr strings.Builder
	if code := first.Run(context.Background(), []string{"init"}, &firstOut, &firstErr); code != 0 {
		t.Fatalf("initial init code = %d; stderr = %q", code, firstErr.String())
	}

	skillPath := filepath.Join(root, ".agents", "skills", "close-issue", "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("user changes"), 0o644); err != nil {
		t.Fatal(err)
	}
	again := New(root, root, Dependencies{Input: initInputWithConfirmation("no")})
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
	app := New(root, root, Dependencies{Input: defaultInitInput()})
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

func assertExecutableFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		t.Fatalf("%s mode = %s, want executable file", path, info.Mode())
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
