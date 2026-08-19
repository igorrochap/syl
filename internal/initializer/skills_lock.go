package initializer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	vendoredskills "github.com/igorrochap/syl/skills"
)

const skillsLockRelativePath = "skills-lock.json"

type skillsLock struct {
	Version int               `json:"version"`
	Skills  map[string]string `json:"skills"`
}

type skillSnapshot struct {
	Files map[string][]byte
	Hash  string
}

func skillsLockPath(projectRoot string) string {
	return filepath.Join(projectRoot, skillsLockRelativePath)
}

func readSkillsLock(projectRoot string) (skillsLock, bool, error) {
	path := skillsLockPath(projectRoot)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return skillsLock{Version: 1, Skills: make(map[string]string)}, false, nil
	}
	if err != nil {
		return skillsLock{}, false, fmt.Errorf("inspect %s: %w", skillsLockRelativePath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || info.IsDir() {
		return skillsLock{}, true, fmt.Errorf("%s is not a regular file; resolve it manually", skillsLockRelativePath)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return skillsLock{}, false, fmt.Errorf("read %s: %w", skillsLockRelativePath, err)
	}
	var lock skillsLock
	if err := json.Unmarshal(contents, &lock); err != nil {
		return skillsLock{}, true, fmt.Errorf("parse %s: %w", skillsLockRelativePath, err)
	}
	if lock.Version != 1 {
		return skillsLock{}, true, fmt.Errorf("unsupported %s version %d", skillsLockRelativePath, lock.Version)
	}
	if lock.Skills == nil {
		lock.Skills = make(map[string]string)
	}
	return lock, true, nil
}

func writeSkillsLock(projectRoot string, lock skillsLock) error {
	path := skillsLockPath(projectRoot)
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || info.IsDir() {
			return fmt.Errorf("%s is not a regular file; resolve it manually", skillsLockRelativePath)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect %s: %w", skillsLockRelativePath, err)
	}
	if lock.Version == 0 {
		lock.Version = 1
	}
	if lock.Skills == nil {
		lock.Skills = make(map[string]string)
	}
	contents, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", skillsLockRelativePath, err)
	}
	contents = append(contents, '\n')
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", skillsLockRelativePath, err)
	}
	return nil
}

func vendoredSkillSnapshot(name string) (skillSnapshot, error) {
	files := make(map[string][]byte)
	err := fs.WalkDir(vendoredskills.Assets, name, func(sourcePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative := strings.TrimPrefix(sourcePath, name+"/")
		contents, err := fs.ReadFile(vendoredskills.Assets, sourcePath)
		if err != nil {
			return err
		}
		files[relative] = contents
		return nil
	})
	if err != nil {
		return skillSnapshot{}, fmt.Errorf("read vendored skill %s: %w", name, err)
	}
	return makeSkillSnapshot(files), nil
}

func installedSkillSnapshot(projectRoot, name string) (skillSnapshot, error) {
	skillRoot := filepath.Join(projectRoot, ".agents", "skills", name)
	info, err := os.Lstat(skillRoot)
	if err != nil {
		return skillSnapshot{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return skillSnapshot{}, fmt.Errorf("installed skill %s is not a directory; resolve it manually", name)
	}

	files := make(map[string][]byte)
	err = filepath.WalkDir(skillRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("installed skill %s contains symlink %s; resolve it manually", name, path)
		}
		relative, err := filepath.Rel(skillRoot, path)
		if err != nil {
			return err
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(relative)] = contents
		return nil
	})
	if err != nil {
		return skillSnapshot{}, fmt.Errorf("read installed skill %s: %w", name, err)
	}
	return makeSkillSnapshot(files), nil
}

func makeSkillSnapshot(files map[string][]byte) skillSnapshot {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, path := range paths {
		contents := files[path]
		fmt.Fprintf(hash, "%s\x00%d\x00", path, len(contents))
		_, _ = hash.Write(contents)
	}
	return skillSnapshot{Files: files, Hash: hex.EncodeToString(hash.Sum(nil))}
}

func snapshotsEqual(installed, vendored skillSnapshot) bool {
	if installed.Hash != vendored.Hash || len(installed.Files) != len(vendored.Files) {
		return false
	}
	for path, installedContents := range installed.Files {
		if vendoredContents, ok := vendored.Files[path]; !ok || !bytes.Equal(installedContents, vendoredContents) {
			return false
		}
	}
	return true
}

func installedSkillNames(projectRoot string) ([]string, error) {
	root := filepath.Join(projectRoot, ".agents", "skills")
	if err := validateSkillsRoot(projectRoot); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read .agents/skills: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}

func validateSkillsRoot(projectRoot string) error {
	for _, relative := range []string{".agents", ".agents/skills"} {
		path := filepath.Join(projectRoot, filepath.FromSlash(relative))
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect %s: %w", relative, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink; resolve it manually before running syl sync", relative)
		}
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory; resolve it manually before running syl sync", relative)
		}
	}
	return nil
}
