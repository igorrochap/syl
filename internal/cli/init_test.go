package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/igorrochap/syl/internal/config"
)

const defaultInitPromptCount = 13

func defaultInitInput() *strings.Reader {
	return strings.NewReader(strings.Repeat("\n", defaultInitPromptCount))
}

func initInputWithConfirmation(answer string) *strings.Reader {
	lines := make([]string, defaultInitPromptCount+1)
	lines[4] = answer
	return strings.NewReader(strings.Join(lines, "\n") + "\n")
}

func TestInitBlankDirectoryScaffoldsProject(t *testing.T) {
	root := t.TempDir()
	input := strings.NewReader(strings.Join([]string{
		"go-style",
		"github",
		"local",
		"configure",
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
	app := New(root, root, testSylHome(t, t.TempDir()), Dependencies{Input: input})
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
	for name, role := range map[string]config.RoleConfig{
		"plan": got.Roles.Plan, "implement": got.Roles.Implement, "review": got.Roles.Review,
	} {
		if role.SandboxMode != config.SandboxModeFullAccess {
			t.Errorf("%s role sandbox = %q, want full-access", name, role.SandboxMode)
		}
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
		`sandbox = "full-access"`,
		"Sandbox mode: full-access, workspace-write, or read-only (default: full-access).",
		"Claude ignores this field. syl applies this value over ~/.codex/config.toml.",
	} {
		if !strings.Contains(string(generated), expected) {
			t.Fatalf("generated config = %q, want %q", generated, expected)
		}
	}
}

func TestInitReturnsConfigLoadError(t *testing.T) {
	root := t.TempDir()
	input := strings.NewReader(strings.Join([]string{
		"",
		"github",
		"local",
		"configure",
		"claude",
		"not-a-claude-model",
		"high",
		"codex",
		"gpt-5.6-luna",
		"xhigh",
		"opencode",
		"reviewer",
		"medium",
	}, "\n") + "\n")
	app := New(root, root, testSylHome(t, t.TempDir()), Dependencies{Input: input})
	var stdout, stderr strings.Builder

	if code := app.Run(context.Background(), []string{"init"}, &stdout, &stderr); code == 0 {
		t.Fatalf("Run() code = 0, want config load failure; stdout = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "roles.plan.model") {
		t.Fatalf("stderr = %q, want config validation error", stderr.String())
	}
}

func TestInitQualityScriptDispatchesGates(t *testing.T) {
	root := initQualityScriptProject(t)
	gateNames := []string{"format", "vet", "style", "complexity", "coverage", "tests", "architecture"}

	listOutput, listError, listCode := runQualityScript(t, root, "--list")
	if listCode != 0 || listError != "" {
		t.Fatalf("quality.sh --list = code %d, stderr %q; want success", listCode, listError)
	}
	wantGates := strings.Join(gateNames, "\n") + "\n"
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
	summaryLines := strings.Split(strings.TrimSuffix(allOutput, "\n"), "\n")
	hasSummaryHeader := strings.HasPrefix(allOutput, "QUALITY GATE\n")
	headerLineCount := 1
	hasOneResultLinePerGate := len(summaryLines) == headerLineCount+len(gateNames)
	hasValidSummaryBlock := hasSummaryHeader && hasOneResultLinePerGate
	if !hasValidSummaryBlock {
		t.Fatalf("quality.sh output = %q, want a header and one line for each gate", allOutput)
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

func TestInitQualityScriptRunsGatesSequentiallyByDefault(t *testing.T) {
	root := initQualityScriptProject(t)
	configureQualityGate(t, root, "format", `
  touch format.finished
  printf '%s\n' 'PASS  format completed'
`)
	configureQualityGate(t, root, "vet", `
  if [[ ! -f format.finished ]]; then
    printf '%s\n' 'vet ran before format finished'
    return 1
  fi
  printf '%s\n' 'PASS  vet completed'
`)

	output, stderr, code := runQualityScript(t, root)
	if code != 0 || stderr != "" {
		t.Fatalf("quality.sh = code %d, stderr %q; want sequential gates to pass", code, stderr)
	}
	if !strings.Contains(output, "format        PASS") || !strings.Contains(output, "vet           PASS") {
		t.Fatalf("quality.sh output = %q, want both configured gates to pass", output)
	}
	if strings.Count(output, "SKIP") != 5 {
		t.Fatalf("quality.sh output = %q, want five unconfigured gates to be skipped", output)
	}
}

func TestInitQualityScriptRunsDeclaredGatesConcurrentlyInFixedOrder(t *testing.T) {
	root := initQualityScriptProject(t)
	declareConcurrentGates(t, root, "format", "vet")
	barrierPath := filepath.Join(root, "vet.finished")
	if err := syscall.Mkfifo(barrierPath, 0o600); err != nil {
		t.Fatal(err)
	}
	configureQualityGate(t, root, "format", `
  IFS= read -r gate_finished < vet.finished
  if [[ "$gate_finished" != done ]]; then
    printf '%s\n' 'vet did not finish while format was running'
    return 1
  fi
  printf '%s\n' 'PASS  format completed late'
`)
	configureQualityGate(t, root, "vet", `
  printf '%s\n' done > vet.finished
  printf '%s\n' 'vet failed'
  return 1
`)

	commandContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(commandContext, filepath.Join(root, "scripts", "quality.sh"))
	command.Dir = root
	command.Cancel = func() error {
		return command.Process.Signal(syscall.SIGTERM)
	}
	command.WaitDelay = 5 * time.Second
	var output, stderr strings.Builder
	command.Stdout = &output
	command.Stderr = &stderr
	runErr := command.Run()
	if commandContext.Err() != nil {
		t.Fatalf("quality.sh did not open the concurrent barrier before timeout; stdout = %q", output.String())
	}
	var exitError *exec.ExitError
	if !errors.As(runErr, &exitError) || exitError.ExitCode() == 0 {
		t.Fatalf("quality.sh error = %v, stderr %q, stdout %q; want the failing Gate to return non-zero", runErr, stderr.String(), output.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("quality.sh stderr = %q; want no stderr", stderr.String())
	}
	if !strings.Contains(output.String(), "format        PASS") || !strings.Contains(output.String(), "vet           FAIL") {
		t.Fatalf("quality.sh output = %q, want late format pass and vet failure", output.String())
	}
	if strings.Index(output.String(), "format        PASS") > strings.Index(output.String(), "vet           FAIL") {
		t.Fatalf("quality.sh output = %q, want summary in fixed Gate order", output.String())
	}
}

func TestInitQualityScriptDoesNotPassCallerStdinToConcurrentGates(t *testing.T) {
	root := initQualityScriptProject(t)
	qualityPath := filepath.Join(root, "scripts", "quality.sh")
	declareConcurrentGates(t, root, "format", "vet")
	for _, gate := range []string{"format", "vet"} {
		configureQualityGate(t, root, gate, `
  if IFS= read -r value; then
    printf '%s\n' 'Gate received caller input'
    return 1
  fi
  printf '%s\n' 'PASS  Gate did not read caller input'
`)
	}

	command := exec.Command(qualityPath)
	command.Dir = root
	command.Stdin = bytes.NewBufferString("caller input\n")
	var stdout, stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("quality.sh = %v, stderr %q, stdout %q; want concurrent Gates to receive no stdin", err, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "format        PASS") || !strings.Contains(stdout.String(), "vet           PASS") {
		t.Fatalf("quality.sh output = %q, want both concurrent Gates to pass", stdout.String())
	}
}

func TestInitQualityScriptStopsConcurrentGatesOnTermination(t *testing.T) {
	root := initQualityScriptProject(t)
	qualityPath := filepath.Join(root, "scripts", "quality.sh")
	declareConcurrentGates(t, root, "format", "vet")
	configureQualityGate(t, root, "format", `
  touch format.started
  while [[ ! -f format.release ]]; do sleep 0.01; done
  touch format.finished
`)
	configureQualityGate(t, root, "vet", `
  touch vet.started
  while [[ ! -f vet.release ]]; do sleep 0.01; done
  touch vet.finished
`)

	temporaryDirectory := filepath.Join(root, "quality-tmp")
	if err := os.Mkdir(temporaryDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(qualityPath)
	command.Dir = root
	command.Env = append(os.Environ(), "TMPDIR="+temporaryDirectory)
	if err := command.Start(); err != nil {
		t.Fatalf("start quality.sh: %v", err)
	}
	waited := false
	t.Cleanup(func() {
		if waited {
			return
		}
		_ = command.Process.Signal(syscall.SIGTERM)
		_ = command.Wait()
	})

	startedPaths := []string{filepath.Join(root, "format.started"), filepath.Join(root, "vet.started")}
	if err := waitForFiles(startedPaths); err != nil {
		t.Fatal(err)
	}
	workerPIDs := readWorkerPIDs(t, temporaryDirectory, len(startedPaths))

	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send TERM to quality.sh: %v", err)
	}
	waitErr := command.Wait()
	waited = true
	if waitErr == nil {
		t.Fatal("quality.sh exited successfully after TERM")
	}
	for _, path := range []string{filepath.Join(root, "format.finished"), filepath.Join(root, "vet.finished")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("Gate continued after TERM; found %s", path)
		}
	}
	for _, pid := range workerPIDs {
		if err := waitForProcessExit(pid); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(temporaryDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary result directory leaked after TERM: %v", entries)
	}
}

func declareConcurrentGates(t *testing.T, root string, gates ...string) {
	t.Helper()
	qualityPath := filepath.Join(root, "scripts", "quality.sh")
	contents, err := os.ReadFile(qualityPath)
	if err != nil {
		t.Fatal(err)
	}
	script := string(contents)
	declaration := "concurrent_gates=(" + strings.Join(gates, " ") + ")"
	updated := strings.Replace(script, "concurrent_gates=()", declaration, 1)
	if updated == script {
		t.Fatal("did not declare concurrent Gates")
	}
	if err := os.WriteFile(qualityPath, []byte(updated), 0o755); err != nil {
		t.Fatal(err)
	}
}

func waitForFiles(paths []string) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		allExist := true
		for _, path := range paths {
			if _, err := os.Stat(path); err != nil {
				allExist = false
				break
			}
		}
		if allExist {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for files %v", paths)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func readWorkerPIDs(t *testing.T, temporaryDirectory string, workerCount int) []int {
	t.Helper()
	directories, err := os.ReadDir(temporaryDirectory)
	if err != nil {
		t.Fatal(err)
	}
	resultDirectory := ""
	for _, entry := range directories {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "quality-gates.") {
			resultDirectory = filepath.Join(temporaryDirectory, entry.Name())
			break
		}
	}
	if resultDirectory == "" {
		t.Fatal("quality result directory is missing")
	}
	pids := make([]int, 0, workerCount)
	for index := 0; index < workerCount; index++ {
		contents, err := os.ReadFile(filepath.Join(resultDirectory, "pid."+strconv.Itoa(index)))
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(contents)))
		if err != nil {
			t.Fatalf("parse worker %d PID: %v", index, err)
		}
		pids = append(pids, pid)
	}
	return pids
}

func waitForProcessExit(pid int) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := syscall.Kill(-pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("check worker process group %d: %w", pid, err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("worker process group %d did not exit", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func configureQualityGate(t *testing.T, root, gate, body string) {
	t.Helper()
	qualityPath := filepath.Join(root, "scripts", "quality.sh")
	contents, err := os.ReadFile(qualityPath)
	if err != nil {
		t.Fatal(err)
	}
	script := string(contents)
	functionStart := strings.Index(script, gate+"() {\n")
	if functionStart < 0 {
		t.Fatalf("quality.sh does not define %s", gate)
	}
	bodyStart := functionStart + len(gate+"() {\n")
	functionEnd := strings.Index(script[bodyStart:], "\n}\n")
	if functionEnd < 0 {
		t.Fatalf("quality.sh has no end for %s", gate)
	}
	updated := script[:bodyStart] + strings.Trim(body, "\n") + script[bodyStart+functionEnd:]
	if err := os.WriteFile(qualityPath, []byte(updated), 0o755); err != nil {
		t.Fatal(err)
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
	app := New(root, root, testSylHome(t, t.TempDir()), Dependencies{Input: defaultInitInput()})
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
	app := New(root, root, testSylHome(t, t.TempDir()), Dependencies{Input: input})
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
	app := New(root, root, testSylHome(t, t.TempDir()), Dependencies{Input: strings.NewReader("")})
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
	app := New(root, root, testSylHome(t, t.TempDir()), Dependencies{Input: defaultInitInput()})
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

	app := New(root, root, testSylHome(t, t.TempDir()), Dependencies{Input: defaultInitInput()})
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
	first := New(root, root, testSylHome(t, t.TempDir()), Dependencies{Input: defaultInitInput()})
	var firstOut, firstErr strings.Builder
	if code := first.Run(context.Background(), []string{"init"}, &firstOut, &firstErr); code != 0 {
		t.Fatalf("initial init code = %d; stderr = %q", code, firstErr.String())
	}
	qualityPath := filepath.Join(root, "scripts", "quality.sh")
	if err := os.Remove(qualityPath); err != nil {
		t.Fatal(err)
	}

	again := New(root, root, testSylHome(t, t.TempDir()), Dependencies{Input: initInputWithConfirmation("yes")})
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
	first := New(root, root, testSylHome(t, t.TempDir()), Dependencies{Input: defaultInitInput()})
	var firstOut, firstErr strings.Builder
	if code := first.Run(context.Background(), []string{"init"}, &firstOut, &firstErr); code != 0 {
		t.Fatalf("initial init code = %d; stderr = %q", code, firstErr.String())
	}
	original, err := os.ReadFile(config.Path(root))
	if err != nil {
		t.Fatal(err)
	}

	runAgainInput := strings.NewReader(strings.Join([]string{
		"", "local", "github", "configure", "codex", "new-model", "low",
		"", "", "", "", "", "", "no",
	}, "\n") + "\n")
	again := New(root, root, testSylHome(t, t.TempDir()), Dependencies{Input: runAgainInput})
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
	first := New(root, root, testSylHome(t, t.TempDir()), Dependencies{Input: defaultInitInput()})
	var firstOut, firstErr strings.Builder
	if code := first.Run(context.Background(), []string{"init"}, &firstOut, &firstErr); code != 0 {
		t.Fatalf("initial init code = %d; stderr = %q", code, firstErr.String())
	}

	skillPath := filepath.Join(root, ".agents", "skills", "close-issue", "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("user changes"), 0o644); err != nil {
		t.Fatal(err)
	}
	again := New(root, root, testSylHome(t, t.TempDir()), Dependencies{Input: initInputWithConfirmation("no")})
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
	app := New(root, root, testSylHome(t, t.TempDir()), Dependencies{Input: defaultInitInput()})
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
