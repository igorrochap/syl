package scripts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestQualityRunsIndependentGatesConcurrentlyAndKeepsSummaryOrder(t *testing.T) {
	root, events := newQualityFixture(t)

	output, code := runQuality(t, root, events, false)
	if code != 0 {
		t.Fatalf("quality gate code = %d, output = %q; want success", code, output)
	}
	if !strings.HasPrefix(output, "QUALITY GATE\n") {
		t.Fatalf("quality output = %q, want the summary header first", output)
	}
	if _, err := os.Stat(filepath.Join(events, "format-vet-overlap")); err != nil {
		entries, _ := os.ReadDir(events)
		t.Fatalf("format and vet did not overlap: %v; events = %v; output = %q", err, entries, output)
	}
	for _, marker := range []string{"vet-reader-overlap", "test-coverage-overlap"} {
		if _, err := os.Stat(filepath.Join(events, marker)); err == nil {
			t.Fatalf("unexpected overlap marker %q; output = %q", marker, output)
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("check overlap marker %q: %v", marker, err)
		}
	}
	for _, marker := range []string{"style-lint-called", "architecture-lint-called"} {
		if _, err := os.Stat(filepath.Join(events, marker)); err != nil {
			t.Fatalf("expected lint marker %q: %v; output = %q", marker, err, output)
		}
	}
	if _, err := os.Stat(filepath.Join(events, "lint-lock-overlap")); err == nil {
		t.Fatalf("style and architecture contended for the golangci-lint lock; output = %q", output)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("check linter lock marker: %v", err)
	}

	assertSummaryOrder(t, output, []string{"format", "vet", "style", "complexity", "coverage", "tests", "architecture"})
}

func TestQualityReportsFailingGateDetailsAndReturnsNonzero(t *testing.T) {
	root, events := newQualityFixture(t)

	output, code := runQuality(t, root, events, true)
	if code == 0 {
		t.Fatalf("quality gate code = %d, output = %q; want failure", code, output)
	}
	if !strings.Contains(output, "  tests         FAIL") {
		t.Fatalf("quality gate output = %q, want a tests FAIL line", output)
	}
	for _, expected := range []string{"synthetic test failure", "installer test report detail"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("quality gate output = %q, want failure detail %q", output, expected)
		}
	}
}

func TestQualityExplainsSequentialFallbackWhenTempDirectoryCannotBeCreated(t *testing.T) {
	root, events := newQualityFixture(t)

	output, code := runQualityWithEnv(t, root, events, false, map[string]string{
		"QUALITY_FAIL_RESULT_MKTEMP":         "1",
		"QUALITY_SKIP_CONCURRENCY_HANDSHAKE": "1",
	})
	if code != 0 {
		t.Fatalf("quality gate code = %d, output = %q; want sequential fallback to pass", code, output)
	}
	if !strings.Contains(output, "Could not create the Gate result directory; running Gates sequentially.") {
		t.Fatalf("quality gate output = %q, want a sequential fallback note", output)
	}
}

func TestQualityStopsConcurrentGatesOnTermination(t *testing.T) {
	root, events := newQualityFixture(t)
	command := exec.Command(filepath.Join(root, "scripts", "quality.sh"))
	command.Dir = root
	command.Env = qualityEnvironment(root, events, false, map[string]string{"QUALITY_STALL_VET": "1"})
	if err := command.Start(); err != nil {
		t.Fatalf("start quality script: %v", err)
	}
	finished := false
	t.Cleanup(func() {
		if finished {
			return
		}
		_ = command.Process.Signal(syscall.SIGTERM)
		_ = command.Wait()
	})
	awaitQualityMarker(t, filepath.Join(events, "go-vet-running"))
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM to quality script: %v", err)
	}
	err := command.Wait()
	finished = true
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 143 {
		t.Fatalf("quality exit = %v, want status 143", err)
	}
	awaitQualityMarker(t, filepath.Join(events, "go-vet-exited"))
}

func newQualityFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	events := filepath.Join(root, "events")
	if err := os.MkdirAll(events, 0o755); err != nil {
		t.Fatalf("create event directory: %v", err)
	}
	writeQualityFixtureFile(t, root, "go.mod", "module fixture\n\ngo 1.23.0\n")
	writeQualityFixtureFile(t, root, "main.go", "package fixture\n\nfunc Value() int { return 1 }\n")
	writeQualityFixtureFile(t, root, "main_test.go", "package fixture\n\nimport \"testing\"\n\nfunc TestValue(t *testing.T) {\n\tif Value() != 1 {\n\t\tt.Fatal(\"wrong value\")\n\t}\n}\n")
	writeQualityFixtureFile(t, root, "scripts/install_test.sh", "#!/usr/bin/env bash\necho 'installer test report detail'\n")
	writeQualityFixtureFile(t, root, "scripts/quality.sh", qualityScriptSource(t))
	if err := os.Chmod(filepath.Join(root, "scripts/quality.sh"), 0o755); err != nil {
		t.Fatalf("make quality script executable: %v", err)
	}
	if err := os.Chmod(filepath.Join(root, "scripts/install_test.sh"), 0o755); err != nil {
		t.Fatalf("make installer test executable: %v", err)
	}

	bin := filepath.Join(root, "fake-bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("create fake command directory: %v", err)
	}
	writeFakeCommand(t, bin, "gofmt", `touch "$QUALITY_EVENTS/format.running"
touch "$QUALITY_EVENTS/format.called"
if [[ "${QUALITY_SKIP_CONCURRENCY_HANDSHAKE:-0}" != "1" ]]; then
  until [[ -e "$QUALITY_EVENTS/vet.running" ]]; do
    sleep 0.01
  done
  touch "$QUALITY_EVENTS/format-vet-overlap"
fi
rm -f "$QUALITY_EVENTS/format.running"
`)
	writeFakeCommand(t, bin, "shellcheck", `touch "$QUALITY_EVENTS/vet.running"
touch "$QUALITY_EVENTS/vet.called"
if [[ "${QUALITY_SKIP_CONCURRENCY_HANDSHAKE:-0}" != "1" ]]; then
  until [[ -e "$QUALITY_EVENTS/format-vet-overlap" ]]; do
    sleep 0.01
  done
fi
`)
	writeFakeCommand(t, bin, "go", `case "$1" in
  vet)
    if [[ "${QUALITY_STALL_VET:-0}" == "1" ]]; then
      touch "$QUALITY_EVENTS/go-vet-running"
      trap 'touch "$QUALITY_EVENTS/go-vet-exited"' EXIT
      sleep 30
    else
      sleep 0.08
    fi
    ;;
  mod)
    sleep 0.08
    rm -f "$QUALITY_EVENTS/vet.running"
    ;;
  test)
    if [[ -e "$QUALITY_EVENTS/vet.running" ]]; then
      touch "$QUALITY_EVENTS/vet-reader-overlap"
    fi
    if [[ "$2" == "-race" ]]; then
      touch "$QUALITY_EVENTS/tests.running"
      sleep 0.12
      rm -f "$QUALITY_EVENTS/tests.running"
      if [[ "${QUALITY_TEST_FAIL:-0}" == "1" ]]; then
        echo "synthetic test failure"
        exit 1
      fi
    else
      if [[ -e "$QUALITY_EVENTS/tests.running" ]]; then
        touch "$QUALITY_EVENTS/test-coverage-overlap"
      fi
      sleep 0.04
      for argument in "$@"; do
        case "$argument" in
          -coverprofile=*) : >"${argument#*=}" ;;
        esac
      done
    fi
    ;;
esac
`)
	writeFakeCommand(t, bin, "golangci-lint", `if [[ "$1" == "version" ]]; then
  echo 2.13.1
  exit 0
fi
if [[ -e "$QUALITY_EVENTS/vet.running" ]]; then
  touch "$QUALITY_EVENTS/vet-reader-overlap"
fi
if [[ "$*" == *"--new-from-merge-base="* ]]; then
  touch "$QUALITY_EVENTS/style-lint-called"
fi
if [[ "$*" == *"--enable-only=depguard"* ]]; then
  touch "$QUALITY_EVENTS/architecture-lint-called"
fi
if ! mkdir "$QUALITY_EVENTS/lint.lock" 2>/dev/null; then
  touch "$QUALITY_EVENTS/lint-lock-overlap"
  echo "golangci-lint lock is held"
  exit 1
fi
sleep 0.4
rmdir "$QUALITY_EVENTS/lint.lock"
`)
	realMktemp, err := exec.LookPath("mktemp")
	if err != nil {
		t.Fatalf("find mktemp: %v", err)
	}
	writeFakeCommand(t, bin, "mktemp", fmt.Sprintf(`if [[ "${QUALITY_FAIL_RESULT_MKTEMP:-0}" == "1" && "$*" == *syl-quality.* ]]; then
  echo "synthetic result directory failure" >&2
  exit 1
fi
exec %q "$@"
`, realMktemp))
	writeFakeCommand(t, bin, "gocyclo", "exit 0\n")

	runGit(t, root, "init")
	runGit(t, root, "checkout", "-b", "main")
	runGit(t, root, "config", "user.email", "quality-test@example.com")
	runGit(t, root, "config", "user.name", "Quality Test")
	commitCoverageFixture(t, root, "quality fixture")
	trackedCommand := exec.Command("git", "ls-files", "*.go")
	trackedCommand.Dir = root
	trackedGoFiles, err := trackedCommand.Output()
	if err != nil || strings.TrimSpace(string(trackedGoFiles)) != "main.go\nmain_test.go" {
		t.Fatalf("fixture tracked Go files = %q, error = %v", trackedGoFiles, err)
	}
	writeQualityFixtureFile(t, root, "main_test.go", "package fixture\n\nimport \"testing\"\n\nfunc TestValue(t *testing.T) {\n\tif Value() != 1 {\n\t\tt.Fatal(\"changed wrong value\")\n\t}\n}\n")
	return root, events
}

func qualityScriptSource(t *testing.T) string {
	t.Helper()

	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("find quality script source")
	}
	contents, err := os.ReadFile(filepath.Join(filepath.Dir(sourceFile), "quality.sh"))
	if err != nil {
		t.Fatalf("read quality script: %v", err)
	}
	return string(contents)
}

func writeQualityFixtureFile(t *testing.T, root, name, contents string) {
	t.Helper()

	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create directory for %s: %v", name, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func writeFakeCommand(t *testing.T, bin, name, body string) {
	t.Helper()

	path := filepath.Join(bin, name)
	contents := "#!/usr/bin/env bash\n" + body
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatalf("write fake %s command: %v", name, err)
	}
}

func runQuality(t *testing.T, root, events string, failTests bool) (string, int) {
	return runQualityWithEnv(t, root, events, failTests, nil)
}

func runQualityWithEnv(t *testing.T, root, events string, failTests bool, extraEnv map[string]string) (string, int) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, filepath.Join(root, "scripts", "quality.sh"))
	command.Dir = root
	command.Env = qualityEnvironment(root, events, failTests, extraEnv)
	command.Cancel = func() error {
		return command.Process.Signal(syscall.SIGTERM)
	}
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	if err == nil {
		return output.String(), 0
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("run quality script: %v; output = %q", err, output.String())
	}
	return output.String(), exitError.ExitCode()
}

func qualityEnvironment(root, events string, failTests bool, extraEnv map[string]string) []string {
	bin := filepath.Join(root, "fake-bin")
	qualityTestFailure := "0"
	if failTests {
		qualityTestFailure = "1"
	}
	values := map[string]string{
		"QUALITY_EVENTS":                     events,
		"QUALITY_BASE_REF":                   "main",
		"QUALITY_TEST_FAIL":                  qualityTestFailure,
		"QUALITY_FAIL_RESULT_MKTEMP":         "0",
		"QUALITY_SKIP_CONCURRENCY_HANDSHAKE": "0",
		"QUALITY_STALL_VET":                  "0",
	}
	for key, value := range extraEnv {
		values[key] = value
	}
	environment := make([]string, 0, len(os.Environ())+len(values)+1)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "PATH=") {
			continue
		}
		if isOverriddenQualityValue(entry, values) {
			continue
		}
		environment = append(environment, entry)
	}
	environment = append(environment, "PATH="+bin+":"+os.Getenv("PATH"))
	for key, value := range values {
		environment = append(environment, key+"="+value)
	}
	return environment
}

func isOverriddenQualityValue(entry string, values map[string]string) bool {
	for key := range values {
		if strings.HasPrefix(entry, key+"=") {
			return true
		}
	}
	return false
}

func awaitQualityMarker(t *testing.T, marker string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for quality marker %q", marker)
}

func assertSummaryOrder(t *testing.T, output string, gates []string) {
	t.Helper()

	previous := -1
	for _, gate := range gates {
		position := strings.Index(output, fmt.Sprintf("  %-14s", gate))
		if position < 0 {
			t.Fatalf("quality output = %q, missing summary line for %s", output, gate)
		}
		if position <= previous {
			t.Fatalf("quality output = %q, summary line for %s is out of order", output, gate)
		}
		previous = position
	}
}
