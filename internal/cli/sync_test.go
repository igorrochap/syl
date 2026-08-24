package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type testSkillsLock struct {
	Version int               `json:"version"`
	Skills  map[string]string `json:"skills"`
}

func TestSyncReportsEveryDriftClassAtTheTopSeam(t *testing.T) {
	root := initProjectForSync(t)

	localPath := filepath.Join(root, ".agents", "skills", "implement", "SKILL.md")
	localContents, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(localPath, append(localContents, []byte("\nlocal edit\n")...), 0o644); err != nil {
		t.Fatal(err)
	}

	movedPath := filepath.Join(root, ".agents", "skills", "tdd", "SKILL.md")
	movedContents, err := os.ReadFile(movedPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(movedPath, append(movedContents, []byte("\nprevious vendored content\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	lock := readTestSkillsLock(t, root)
	lock.Skills["tdd"] = hashTestSkill(t, root, "tdd")
	writeTestSkillsLock(t, root, lock)

	missingPath := filepath.Join(root, ".agents", "skills", "close-issue")
	if err := os.RemoveAll(missingPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".agents", "skills", "project-only"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".agents", "skills", "project-only", "SKILL.md"), []byte("project skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := New(root, root, Dependencies{Input: strings.NewReader("k\nk\n")})
	var stdout, stderr strings.Builder
	if code := app.Run(context.Background(), []string{"sync", "--dry-run"}, &stdout, &stderr); code != 0 {
		t.Fatalf("sync --dry-run code = %d, stderr = %q", code, stderr.String())
	}
	output := stdout.String()
	for _, want := range []string{
		"close-issue: missing core",
		"go-style: missing optional",
		"implement: differing (locally-modified)",
		"tdd: differing (vendored-moved-forward)",
		"grilling: unchanged",
		"project-only: extra",
		"content diff",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("sync report = %q, want %q", output, want)
		}
	}
}

func TestSyncDryRunChangesZeroBytes(t *testing.T) {
	root := initProjectForSync(t)
	path := filepath.Join(root, ".agents", "skills", "implement", "SKILL.md")
	if err := os.WriteFile(path, []byte("local edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshotProject(t, root)

	app := New(root, root, Dependencies{Input: strings.NewReader("k\n")})
	var stdout, stderr strings.Builder
	if code := app.Run(context.Background(), []string{"sync", "--dry-run"}, &stdout, &stderr); code != 0 {
		t.Fatalf("sync --dry-run code = %d, stderr = %q", code, stderr.String())
	}
	after := snapshotProject(t, root)
	if !bytes.Equal(before, after) {
		t.Fatalf("sync --dry-run changed bytes\nbefore: %q\nafter: %q", before, after)
	}
}

func TestSyncOnUpToDateProjectSaysSo(t *testing.T) {
	root := initProjectForSync(t)
	app := New(root, root, Dependencies{})
	var stdout, stderr strings.Builder
	if code := app.Run(context.Background(), []string{"sync"}, &stdout, &stderr); code != 0 {
		t.Fatalf("sync code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Skills are up to date.") {
		t.Fatalf("sync output = %q, want up-to-date message", stdout.String())
	}
}

func TestSyncUpdatesApprovedSkillAndLockfileButKeepsRejectedSkill(t *testing.T) {
	root := initProjectForSync(t)
	updatedPath := filepath.Join(root, ".agents", "skills", "tdd", "SKILL.md")
	keptPath := filepath.Join(root, ".agents", "skills", "implement", "SKILL.md")
	updatedBefore, err := os.ReadFile(updatedPath)
	if err != nil {
		t.Fatal(err)
	}
	keptBefore, err := os.ReadFile(keptPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(updatedPath, append(updatedBefore, []byte("\nold vendored version\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keptPath, append(keptBefore, []byte("\nlocal modification\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	extraPath := filepath.Join(root, ".agents", "skills", "project-only", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(extraPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(extraPath, []byte("project skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lock := readTestSkillsLock(t, root)
	lock.Skills["tdd"] = hashTestSkill(t, root, "tdd")
	beforeKeptLock := lock.Skills["implement"]
	writeTestSkillsLock(t, root, lock)

	app := New(root, root, Dependencies{Input: strings.NewReader("k\nu\n")})
	var stdout, stderr strings.Builder
	if code := app.Run(context.Background(), []string{"sync"}, &stdout, &stderr); code != 0 {
		t.Fatalf("sync code = %d, stderr = %q", code, stderr.String())
	}
	updatedAfter, err := os.ReadFile(updatedPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(updatedAfter, updatedBefore) {
		t.Fatalf("approved skill was not restored to vendored content: %q", updatedAfter)
	}
	keptAfter, err := os.ReadFile(keptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(keptAfter, append(keptBefore, []byte("\nlocal modification\n")...)) {
		t.Fatalf("kept skill changed: %q", keptAfter)
	}
	updatedLock := readTestSkillsLock(t, root)
	if updatedLock.Skills["tdd"] == lock.Skills["tdd"] {
		t.Fatal("updated skill lock hash did not change")
	}
	if updatedLock.Skills["implement"] != beforeKeptLock {
		t.Fatalf("kept skill lock hash = %q, want %q", updatedLock.Skills["implement"], beforeKeptLock)
	}
	if !strings.Contains(stdout.String(), "content diff") {
		t.Fatalf("sync output = %q, want local-modification diff", stdout.String())
	}
	if contents, err := os.ReadFile(extraPath); err != nil || string(contents) != "project skill\n" {
		t.Fatalf("extra skill changed: %q, err = %v", contents, err)
	}
}

func TestSyncAllStillConfirmsLocallyModifiedCoreSkill(t *testing.T) {
	root := initProjectForSync(t)
	path := filepath.Join(root, ".agents", "skills", "implement", "SKILL.md")
	if err := os.WriteFile(path, []byte("do not overwrite\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	beforeLock := readTestSkillsLock(t, root)

	app := New(root, root, Dependencies{Input: strings.NewReader("d\nk\n")})
	var stdout, stderr strings.Builder
	if code := app.Run(context.Background(), []string{"sync", "--all"}, &stdout, &stderr); code != 0 {
		t.Fatalf("sync --all code = %d, stderr = %q", code, stderr.String())
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "do not overwrite\n" {
		t.Fatalf("locally modified skill overwritten by --all: %q", contents)
	}
	if got := readTestSkillsLock(t, root).Skills["implement"]; got != beforeLock.Skills["implement"] {
		t.Fatalf("locally modified lock hash = %q, want %q", got, beforeLock.Skills["implement"])
	}
	if strings.Count(stdout.String(), "content diff for implement") < 2 {
		t.Fatalf("sync --all output = %q, want report diff and show-diff output", stdout.String())
	}
	if !strings.Contains(stdout.String(), "explicit confirmation") {
		t.Fatalf("sync --all output = %q, want explicit confirmation warning", stdout.String())
	}
}

func initProjectForSync(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	app := New(root, root, Dependencies{Input: defaultInitInput()})
	var stdout, stderr strings.Builder
	if code := app.Run(context.Background(), []string{"init"}, &stdout, &stderr); code != 0 {
		t.Fatalf("init code = %d, stderr = %q, stdout = %q", code, stderr.String(), stdout.String())
	}
	return root
}

func readTestSkillsLock(t *testing.T, root string) testSkillsLock {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(root, "skills-lock.json"))
	if err != nil {
		t.Fatalf("read skills-lock.json: %v", err)
	}
	var lock testSkillsLock
	if err := json.Unmarshal(contents, &lock); err != nil {
		t.Fatalf("parse skills-lock.json: %v", err)
	}
	return lock
}

func writeTestSkillsLock(t *testing.T, root string, lock testSkillsLock) {
	t.Helper()
	contents, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	contents = append(contents, '\n')
	if err := os.WriteFile(filepath.Join(root, "skills-lock.json"), contents, 0o644); err != nil {
		t.Fatal(err)
	}
}

func hashTestSkill(t *testing.T, root, name string) string {
	t.Helper()
	var paths []string
	skillRoot := filepath.Join(root, ".agents", "skills", name)
	if err := filepath.WalkDir(skillRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(skillRoot, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, relative := range paths {
		contents, err := os.ReadFile(filepath.Join(skillRoot, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(hash, "%s\x00%d\x00", relative, len(contents))
		_, _ = hash.Write(contents)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func snapshotProject(t *testing.T, root string) []byte {
	t.Helper()
	var paths []string
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root || entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	var snapshot bytes.Buffer
	for _, relative := range paths {
		path := filepath.Join(root, filepath.FromSlash(relative))
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&snapshot, "%s\x00symlink:%s\x00", relative, target)
			continue
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&snapshot, "%s\x00%d\x00", relative, len(contents))
		_, _ = snapshot.Write(contents)
	}
	return snapshot.Bytes()
}
