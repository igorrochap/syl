package initializer

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type SyncOptions struct {
	Input  io.Reader
	Output io.Writer
	DryRun bool
	All    bool
}

type syncSkill struct {
	manifest  manifestSkill
	status    string
	installed skillSnapshot
	vendored  skillSnapshot
	present   bool
}

type syncReport struct {
	skills []syncSkill
	extra  []string
	lock   skillsLock
}

const (
	syncUnchanged            = "unchanged"
	syncMissingCore          = "missing core"
	syncMissingOptional      = "missing optional"
	syncVendoredMovedForward = "differing (vendored-moved-forward)"
	syncLocallyModified      = "differing (locally-modified)"
)

// Sync compares the project's installed skills with the embedded canonical set
// and applies only the decisions made for differing skills.
func Sync(projectRoot string, options SyncOptions) error {
	if options.Output == nil {
		options.Output = io.Discard
	}
	report, err := makeSyncReport(projectRoot)
	if err != nil {
		return err
	}
	writeSyncReport(options.Output, report)
	if options.DryRun {
		return nil
	}

	input := options.Input
	if input == nil {
		input = strings.NewReader("")
	}
	reader := bufio.NewReader(input)
	lockDirty := false
	changed := false
	for index := range report.skills {
		skill := &report.skills[index]
		if skill.status == syncUnchanged || skill.status == syncMissingOptional {
			continue
		}

		update := options.All && canAutoUpdate(*skill)
		if !update {
			decision, err := promptSyncDecision(reader, options.Output, *skill)
			if err != nil {
				return err
			}
			update = decision == syncUpdate
		}
		if !update {
			continue
		}

		if err := updateSkill(projectRoot, *skill); err != nil {
			return err
		}
		fmt.Fprintf(options.Output, "Updated skill %s to the vendored version.\n", skill.manifest.Name)
		skill.installed = skill.vendored
		skill.present = true
		skill.status = syncUnchanged
		report.lock.Skills[skill.manifest.Name] = skill.vendored.Hash
		if err := writeSkillsLock(projectRoot, report.lock); err != nil {
			return err
		}
		lockDirty = true
		changed = true
	}

	for _, skill := range report.skills {
		if skill.status == syncUnchanged && report.lock.Skills[skill.manifest.Name] != skill.vendored.Hash {
			report.lock.Skills[skill.manifest.Name] = skill.vendored.Hash
			lockDirty = true
		}
		if !skill.present {
			if _, ok := report.lock.Skills[skill.manifest.Name]; ok {
				delete(report.lock.Skills, skill.manifest.Name)
				lockDirty = true
			}
		}
	}
	if lockDirty {
		if err := writeSkillsLock(projectRoot, report.lock); err != nil {
			return err
		}
	}

	if !changed && allSkillsUnchanged(report) {
		fmt.Fprintln(options.Output, "Skills are up to date.")
	}
	return nil
}

func makeSyncReport(projectRoot string) (syncReport, error) {
	manifest, err := readSkillManifest()
	if err != nil {
		return syncReport{}, err
	}
	lock, _, err := readSkillsLock(projectRoot)
	if err != nil {
		return syncReport{}, err
	}
	installedNames, err := installedSkillNames(projectRoot)
	if err != nil {
		return syncReport{}, err
	}
	installed := make(map[string]bool, len(installedNames))
	for _, name := range installedNames {
		installed[name] = true
	}

	report := syncReport{lock: lock}
	vendoredNames := make(map[string]bool, len(manifest.Skills))
	for _, manifestSkill := range manifest.Skills {
		vendoredNames[manifestSkill.Name] = true
		vendored, err := vendoredSkillSnapshot(manifestSkill.Name)
		if err != nil {
			return syncReport{}, err
		}
		skill := syncSkill{manifest: manifestSkill, vendored: vendored}
		if !installed[manifestSkill.Name] {
			if manifestSkill.Classification == "core" {
				skill.status = syncMissingCore
			} else {
				skill.status = syncMissingOptional
			}
			report.skills = append(report.skills, skill)
			continue
		}

		installedSnapshot, err := installedSkillSnapshot(projectRoot, manifestSkill.Name)
		if err != nil {
			return syncReport{}, err
		}
		skill.installed = installedSnapshot
		skill.present = true
		switch {
		case snapshotsEqual(installedSnapshot, vendored):
			skill.status = syncUnchanged
		case lock.Skills[manifestSkill.Name] != "" && installedSnapshot.Hash == lock.Skills[manifestSkill.Name] && installedSnapshot.Hash != vendored.Hash:
			skill.status = syncVendoredMovedForward
		default:
			skill.status = syncLocallyModified
		}
		report.skills = append(report.skills, skill)
	}
	for _, name := range installedNames {
		if !vendoredNames[name] {
			report.extra = append(report.extra, name)
		}
	}
	return report, nil
}

func writeSyncReport(output io.Writer, report syncReport) {
	fmt.Fprintln(output, "Skill sync report:")
	for _, skill := range report.skills {
		fmt.Fprintf(output, "- %s: %s\n", skill.manifest.Name, skill.status)
		if skill.status == syncLocallyModified {
			fmt.Fprintln(output, renderSkillDiff(skill))
			fmt.Fprintf(output, "WARNING: %s is locally-modified; explicit confirmation is required before overwrite.\n", skill.manifest.Name)
		}
	}
	for _, name := range report.extra {
		fmt.Fprintf(output, "- %s: extra (project skill; untouched)\n", name)
	}
	if len(report.skills) == 0 && len(report.extra) == 0 {
		fmt.Fprintln(output, "- no installed skills")
	}
}

func allSkillsUnchanged(report syncReport) bool {
	for _, skill := range report.skills {
		if skill.status != syncUnchanged && skill.status != syncMissingOptional {
			return false
		}
	}
	return true
}

func canAutoUpdate(skill syncSkill) bool {
	if skill.status == syncLocallyModified {
		return false
	}
	if skill.manifest.Classification == "core" {
		return skill.status == syncMissingCore || skill.status == syncVendoredMovedForward
	}
	return skill.present && skill.status == syncVendoredMovedForward
}

type syncDecision string

const (
	syncUpdate syncDecision = "update"
	syncKeep   syncDecision = "keep"
)

func promptSyncDecision(reader *bufio.Reader, output io.Writer, skill syncSkill) (syncDecision, error) {
	if skill.status == syncLocallyModified {
		fmt.Fprintf(output, "WARNING: %s has local changes; choose explicitly.\n", skill.manifest.Name)
	}
	for {
		fmt.Fprintf(output, "Decision for %s [update/keep local/show diff]: ", skill.manifest.Name)
		line, err := reader.ReadString('\n')
		line = strings.ToLower(strings.TrimSpace(line))
		if line == "d" || line == "diff" || line == "show diff" || line == "show diff first" {
			fmt.Fprintln(output, renderSkillDiff(skill))
			if err != nil {
				return "", fmt.Errorf("sync decision for %s: %w", skill.manifest.Name, err)
			}
			continue
		}
		switch line {
		case "u", "update", "update to vendored":
			return syncUpdate, nil
		case "k", "keep", "keep local":
			return syncKeep, nil
		default:
			if err != nil {
				if errors.Is(err, io.EOF) {
					return "", fmt.Errorf("sync requires a decision for %s", skill.manifest.Name)
				}
				return "", fmt.Errorf("read sync decision for %s: %w", skill.manifest.Name, err)
			}
			fmt.Fprintln(output, "Choose update, keep local, or show diff first.")
		}
	}
}

func updateSkill(projectRoot string, skill syncSkill) error {
	if err := validateSkillsRoot(projectRoot); err != nil {
		return err
	}
	skillsRoot := filepath.Join(projectRoot, ".agents", "skills")
	if err := os.MkdirAll(skillsRoot, 0o755); err != nil {
		return fmt.Errorf("create .agents/skills: %w", err)
	}
	destination := filepath.Join(skillsRoot, skill.manifest.Name)
	if info, err := os.Lstat(destination); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("installed skill %s is not a directory; resolve it manually", skill.manifest.Name)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect installed skill %s: %w", skill.manifest.Name, err)
	}

	staging, err := os.MkdirTemp(skillsRoot, ".sync-")
	if err != nil {
		return fmt.Errorf("stage skill %s: %w", skill.manifest.Name, err)
	}
	defer os.RemoveAll(staging)
	for relative, contents := range skill.vendored.Files {
		path := filepath.Join(staging, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("stage skill %s: %w", skill.manifest.Name, err)
		}
		if err := os.WriteFile(path, contents, 0o644); err != nil {
			return fmt.Errorf("stage skill %s: %w", skill.manifest.Name, err)
		}
	}
	if err := os.RemoveAll(destination); err != nil {
		return fmt.Errorf("replace skill %s: %w", skill.manifest.Name, err)
	}
	if err := os.Rename(staging, destination); err != nil {
		return fmt.Errorf("install skill %s: %w", skill.manifest.Name, err)
	}
	return nil
}

func renderSkillDiff(skill syncSkill) string {
	paths := make(map[string]bool, len(skill.installed.Files)+len(skill.vendored.Files))
	for path := range skill.installed.Files {
		paths[path] = true
	}
	for path := range skill.vendored.Files {
		paths[path] = true
	}
	orderedPaths := make([]string, 0, len(paths))
	for path := range paths {
		orderedPaths = append(orderedPaths, path)
	}
	sort.Strings(orderedPaths)

	var diff strings.Builder
	fmt.Fprintf(&diff, "content diff for %s:\n", skill.manifest.Name)
	for _, path := range orderedPaths {
		installed, installedOK := skill.installed.Files[path]
		vendored, vendoredOK := skill.vendored.Files[path]
		if installedOK && vendoredOK && bytes.Equal(installed, vendored) {
			continue
		}
		fmt.Fprintf(&diff, "--- installed/.agents/skills/%s/%s\n", skill.manifest.Name, path)
		fmt.Fprintf(&diff, "+++ vendored/%s/%s\n", skill.manifest.Name, path)
		writeDiffLines(&diff, installed, installedOK, vendored, vendoredOK)
	}
	return strings.TrimRight(diff.String(), "\n")
}

func writeDiffLines(output *strings.Builder, installed []byte, installedOK bool, vendored []byte, vendoredOK bool) {
	if installedOK {
		for _, line := range strings.Split(strings.TrimSuffix(string(installed), "\n"), "\n") {
			fmt.Fprintf(output, "-%s\n", line)
		}
	}
	if vendoredOK {
		for _, line := range strings.Split(strings.TrimSuffix(string(vendored), "\n"), "\n") {
			fmt.Fprintf(output, "+%s\n", line)
		}
	}
}
