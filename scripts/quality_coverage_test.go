package scripts

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCoveragePassesForChangedFunctionWithTest(t *testing.T) {
	root := newCoverageFixture(t)

	writeCoverageFixtureFile(t, root, "feature.go", `package fixture

func Existing() int {
	return 1
}

func Added() int {
	return 42
}
`)
	writeCoverageFixtureFile(t, root, "feature_test.go", `package fixture

import "testing"

func TestExisting(t *testing.T) {
	if Existing() != 1 {
		t.Fatal("Existing() returned the wrong value")
	}
}

func TestAdded(t *testing.T) {
	if Added() != 42 {
		t.Fatal("Added() returned the wrong value")
	}
}
`)
	commitCoverageFixture(t, root, "add tested function")

	output, code := runCoverageGate(t, root, "main")
	if code != 0 {
		t.Fatalf("coverage gate code = %d, output = %q; want success", code, output)
	}
	if !strings.Contains(output, "base main") {
		t.Fatalf("coverage gate output = %q, want selected base", output)
	}
	if !strings.Contains(output, "Changed lines examined: 2") {
		t.Fatalf("coverage gate output = %q, want two examined changed lines", output)
	}
	if !strings.Contains(output, "PASS") {
		t.Fatalf("coverage gate output = %q, want PASS", output)
	}
}

func TestCoverageFailsForChangedFunctionWithoutTest(t *testing.T) {
	root := newCoverageFixture(t)

	writeCoverageFixtureFile(t, root, "feature.go", `package fixture

func Existing() int {
	return 1
}

func Added() int {
	return 42
}
`)
	commitCoverageFixture(t, root, "add untested function")

	output, code := runCoverageGate(t, root, "main")
	if code == 0 {
		t.Fatalf("coverage gate code = %d, output = %q; want failure", code, output)
	}
	for _, expected := range []string{
		"Coverage of changed lines: 0.00% (0/2), required 80%.",
		"Lines without tests:",
		"feature.go:7",
		"feature.go:8",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("coverage gate output = %q, want %q", output, expected)
		}
	}
	if strings.Contains(output, "PASS") {
		t.Fatalf("coverage gate output = %q, must not report PASS", output)
	}
}

func TestCoveragePassesForCommentOnlyChange(t *testing.T) {
	root := newCoverageFixture(t)

	writeCoverageFixtureFile(t, root, "feature.go", `package fixture

func Existing() int {
	return 1
}

// This comment is not an executable statement.
`)
	commitCoverageFixture(t, root, "change comment")

	output, code := runCoverageGate(t, root, "main")
	if code != 0 {
		t.Fatalf("coverage gate code = %d, output = %q; want success", code, output)
	}
	if !strings.Contains(output, "Changed lines examined: 0") || !strings.Contains(output, "PASS") {
		t.Fatalf("coverage gate output = %q, want zero examined lines and PASS", output)
	}
}

func TestCoveragePassesForEmptyLineOnlyChange(t *testing.T) {
	root := newCoverageFixture(t)

	writeCoverageFixtureFile(t, root, "feature.go", `package fixture

func Existing() int {
	return 1
}

`)
	commitCoverageFixture(t, root, "change empty line")

	output, code := runCoverageGate(t, root, "main")
	if code != 0 {
		t.Fatalf("coverage gate code = %d, output = %q; want success", code, output)
	}
	if !strings.Contains(output, "Changed lines examined: 0") || !strings.Contains(output, "PASS") {
		t.Fatalf("coverage gate output = %q, want zero examined lines and PASS", output)
	}
}

func TestCoveragePassesForClosingBraceOnlyChange(t *testing.T) {
	root := newCoverageFixture(t)

	writeCoverageFixtureFile(t, root, "feature.go", `package fixture

func Existing() int {
	return 1
}

func Untested() int {
	return 2
}
`)
	commitCoverageFixture(t, root, "add untested baseline function")
	runGit(t, root, "branch", "brace-base")

	writeCoverageFixtureFile(t, root, "feature.go", `package fixture

func Existing() int {
	return 1
}

func Untested() int {
	return 2
 }
`)
	commitCoverageFixture(t, root, "change closing brace")

	output, code := runCoverageGate(t, root, "brace-base")
	if code != 0 {
		t.Fatalf("coverage gate code = %d, output = %q; want success", code, output)
	}
	if !strings.Contains(output, "Changed lines examined: 0") || !strings.Contains(output, "PASS") {
		t.Fatalf("coverage gate output = %q, want zero examined lines and PASS", output)
	}
}

func TestCoverageUsesMergeBaseForConfiguredBaseRef(t *testing.T) {
	root := newCoverageFixture(t)

	runGit(t, root, "checkout", "-b", "release")
	writeCoverageFixtureFile(t, root, "feature.go", `package fixture

func Existing() int {
	return 1
}

func ReleaseOnly() int {
	return 2
}
`)
	commitCoverageFixture(t, root, "add release-only function")
	runGit(t, root, "checkout", "feature")

	output, code := runCoverageGate(t, root, "release")
	if code != 0 {
		t.Fatalf("coverage gate code = %d, output = %q; want success", code, output)
	}
	for _, expected := range []string{
		"base release, 0 changed files, Changed lines examined: 0",
		"PASS",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("coverage gate output = %q, want %q", output, expected)
		}
	}
}

func TestCoverageFailsWhenChangedGoFileHasNoChangedLines(t *testing.T) {
	root := newCoverageFixture(t)

	writeCoverageFixtureFile(t, root, "feature.go", `package fixture

func Existing() int {
	return 1
}

func Removed() int {
	return 2
}
`)
	commitCoverageFixture(t, root, "add removable function")
	runGit(t, root, "branch", "deletion-base")

	writeCoverageFixtureFile(t, root, "feature.go", `package fixture

func Existing() int {
	return 1
}
`)
	commitCoverageFixture(t, root, "remove function")

	output, code := runCoverageGate(t, root, "deletion-base")
	if code == 0 {
		t.Fatalf("coverage gate code = %d, output = %q; want failure", code, output)
	}
	if !strings.Contains(output, "changed Go files produced no changed lines for coverage") {
		t.Fatalf("coverage gate output = %q, want no-changed-lines failure", output)
	}
	if strings.Contains(output, "PASS") {
		t.Fatalf("coverage gate output = %q, must not report PASS", output)
	}
}

func TestCoverageFailsWhenChangedCodeHasNoCoverageProfileEntry(t *testing.T) {
	root := newCoverageFixture(t)

	writeCoverageFixtureFile(t, root, filepath.Join("untested", "go.mod"), "module example.com/untested\n\ngo 1.23.0\n")
	writeCoverageFixtureFile(t, root, filepath.Join("untested", "feature.go"), `package untested

func Added() int {
	return 42
}
`)
	commitCoverageFixture(t, root, "add function in untested package")

	output, code := runCoverageGate(t, root, "main")
	if code == 0 {
		t.Fatalf("coverage gate code = %d, output = %q; want failure", code, output)
	}
	if !strings.Contains(output, "changed code lines produced no coverage profile entries") {
		t.Fatalf("coverage gate output = %q, want no-profile-entry failure", output)
	}
	if strings.Contains(output, "PASS") {
		t.Fatalf("coverage gate output = %q, must not report PASS", output)
	}
}

func newCoverageFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	writeCoverageFixtureFile(t, root, "go.mod", "module example.com/fixture\n\ngo 1.23.0\n")
	writeCoverageFixtureFile(t, root, "feature.go", `package fixture

func Existing() int {
	return 1
}
`)
	writeCoverageFixtureFile(t, root, "feature_test.go", `package fixture

import "testing"

func TestExisting(t *testing.T) {
	if Existing() != 1 {
		t.Fatal("Existing() returned the wrong value")
	}
}
`)

	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("find quality script source")
	}
	sourceScript := filepath.Join(filepath.Dir(sourceFile), "quality.sh")
	contents, err := os.ReadFile(sourceScript)
	if err != nil {
		t.Fatalf("read quality script: %v", err)
	}
	writeCoverageFixtureFile(t, root, filepath.Join("scripts", "quality.sh"), string(contents))
	if err := os.Chmod(filepath.Join(root, "scripts", "quality.sh"), 0o755); err != nil {
		t.Fatalf("make quality script executable: %v", err)
	}

	runGit(t, root, "init")
	runGit(t, root, "checkout", "-b", "main")
	runGit(t, root, "config", "user.email", "coverage-test@example.com")
	runGit(t, root, "config", "user.name", "Coverage Test")
	commitCoverageFixture(t, root, "base")
	runGit(t, root, "checkout", "-b", "feature")
	return root
}

func writeCoverageFixtureFile(t *testing.T, root, name, contents string) {
	t.Helper()

	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create directory for %s: %v", name, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func commitCoverageFixture(t *testing.T, root, message string) {
	t.Helper()

	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", message)
}

func runCoverageGate(t *testing.T, root, baseRef string) (string, int) {
	t.Helper()

	command := exec.Command(filepath.Join(root, "scripts", "quality.sh"), "coverage")
	command.Dir = root
	command.Env = append(os.Environ(), "QUALITY_BASE_REF="+baseRef)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	if err == nil {
		return output.String(), 0
	}

	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("run coverage gate: %v; output = %q", err, output.String())
	}
	return output.String(), exitError.ExitCode()
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()

	command := exec.Command("git", args...)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}
