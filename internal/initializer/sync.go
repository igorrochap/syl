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

	"github.com/igorrochap/syl/internal/ui"
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
	renderer := ui.New(options.Output, ui.DetectCaps(options.Output))
	if err := writeSyncReport(renderer, options.Output, report); err != nil {
		return err
	}
	if options.DryRun {
		return nil
	}

	changed, err := applySyncDecisions(projectRoot, options, renderer, &report)
	if err != nil {
		return err
	}
	if err := reconcileSyncLock(projectRoot, &report); err != nil {
		return err
	}
	if !changed && allSkillsUnchanged(report) {
		if err := renderer.Text("Skills are up to date."); err != nil {
			return err
		}
	}
	return nil
}

func applySyncDecisions(projectRoot string, options SyncOptions, renderer *ui.Renderer, report *syncReport) (bool, error) {
	input := options.Input
	if input == nil {
		input = strings.NewReader("")
	}
	reader := bufio.NewReader(input)
	changed := false
	for index := range report.skills {
		skill := &report.skills[index]
		if !requiresSyncDecision(*skill) {
			continue
		}
		update, err := decideSyncUpdate(reader, renderer, options, *skill)
		if err != nil {
			return false, err
		}
		if !update {
			continue
		}
		if err := applySkillUpdate(projectRoot, renderer, report, skill); err != nil {
			return false, err
		}
		changed = true
	}
	return changed, nil
}

func requiresSyncDecision(skill syncSkill) bool {
	return skill.status != syncUnchanged && skill.status != syncMissingOptional
}

func decideSyncUpdate(reader *bufio.Reader, renderer *ui.Renderer, options SyncOptions, skill syncSkill) (bool, error) {
	if options.All && canAutoUpdate(skill) {
		return true, nil
	}
	decision, err := promptSyncDecision(reader, renderer, options.Output, skill)
	if err != nil {
		return false, err
	}
	return decision == syncUpdate, nil
}

func applySkillUpdate(projectRoot string, renderer *ui.Renderer, report *syncReport, skill *syncSkill) error {
	if err := updateSkill(projectRoot, *skill); err != nil {
		return err
	}
	if err := renderer.Text(fmt.Sprintf("Updated skill %s to the vendored version.", skill.manifest.Name)); err != nil {
		return err
	}
	skill.installed = skill.vendored
	skill.present = true
	skill.status = syncUnchanged
	report.lock.Skills[skill.manifest.Name] = skill.vendored.Hash
	return writeSkillsLock(projectRoot, report.lock)
}

func reconcileSyncLock(projectRoot string, report *syncReport) error {
	lockDirty := false
	for _, skill := range report.skills {
		if syncLockNeedsVendoredHash(report.lock, skill) {
			report.lock.Skills[skill.manifest.Name] = skill.vendored.Hash
			lockDirty = true
		}
		if syncLockNeedsRemoval(report.lock, skill) {
			delete(report.lock.Skills, skill.manifest.Name)
			lockDirty = true
		}
	}
	if !lockDirty {
		return nil
	}
	return writeSkillsLock(projectRoot, report.lock)
}

func syncLockNeedsVendoredHash(lock skillsLock, skill syncSkill) bool {
	return skill.status == syncUnchanged && lock.Skills[skill.manifest.Name] != skill.vendored.Hash
}

func syncLockNeedsRemoval(lock skillsLock, skill syncSkill) bool {
	if skill.present {
		return false
	}
	_, ok := lock.Skills[skill.manifest.Name]
	return ok
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

func writeSyncReport(renderer *ui.Renderer, output io.Writer, report syncReport) error {
	if err := renderer.Text("Skill sync report:"); err != nil {
		return err
	}
	if err := writeSyncSkillReport(renderer, output, report.skills); err != nil {
		return err
	}
	if err := writeSyncExtraSkills(renderer, report.extra); err != nil {
		return err
	}
	if len(report.skills) == 0 && len(report.extra) == 0 {
		return renderer.Text("- no installed skills")
	}
	return nil
}

func writeSyncSkillReport(renderer *ui.Renderer, output io.Writer, skills []syncSkill) error {
	for _, skill := range skills {
		if err := renderer.Text(fmt.Sprintf("- %s: %s", skill.manifest.Name, skill.status)); err != nil {
			return err
		}
		if skill.status == syncLocallyModified {
			if err := writeSkillDiff(output, skill); err != nil {
				return err
			}
			if err := renderer.Text(fmt.Sprintf("WARNING: %s is locally-modified; explicit confirmation is required before overwrite.", skill.manifest.Name)); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeSyncExtraSkills(renderer *ui.Renderer, names []string) error {
	for _, name := range names {
		if err := renderer.Text(fmt.Sprintf("- %s: extra (project skill; untouched)", name)); err != nil {
			return err
		}
	}
	return nil
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

func promptSyncDecision(reader *bufio.Reader, renderer *ui.Renderer, output io.Writer, skill syncSkill) (syncDecision, error) {
	if skill.status == syncLocallyModified {
		if err := renderer.Text(fmt.Sprintf("WARNING: %s has local changes; choose explicitly.", skill.manifest.Name)); err != nil {
			return "", err
		}
	}
	for {
		if err := renderer.PromptLine(fmt.Sprintf("Decision for %s [update/keep local/show diff]: ", skill.manifest.Name)); err != nil {
			return "", err
		}
		decision, handled, err := readSyncDecision(reader, output, skill)
		if err != nil {
			return "", err
		}
		if handled {
			return decision, nil
		}
		if err := renderer.Text("Choose update, keep local, or show diff first."); err != nil {
			return "", err
		}
	}
}

func readSyncDecision(reader *bufio.Reader, output io.Writer, skill syncSkill) (syncDecision, bool, error) {
	line, readErr := reader.ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	if isSyncDiffCommand(line) {
		if err := writeSkillDiff(output, skill); err != nil {
			return "", false, err
		}
		if readErr != nil {
			return "", false, fmt.Errorf("sync decision for %s: %w", skill.manifest.Name, readErr)
		}
		return "", false, nil
	}
	decision, ok := syncDecisionFor(line)
	if ok {
		return decision, true, nil
	}
	if readErr != nil {
		if errors.Is(readErr, io.EOF) {
			return "", false, fmt.Errorf("sync requires a decision for %s", skill.manifest.Name)
		}
		return "", false, fmt.Errorf("read sync decision for %s: %w", skill.manifest.Name, readErr)
	}
	return "", false, nil
}

func isSyncDiffCommand(line string) bool {
	switch line {
	case "d", "diff", "show diff", "show diff first":
		return true
	default:
		return false
	}
}

func syncDecisionFor(line string) (syncDecision, bool) {
	switch line {
	case "u", "update", "update to vendored":
		return syncUpdate, true
	case "k", "keep", "keep local":
		return syncKeep, true
	default:
		return "", false
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

func writeSkillDiff(output io.Writer, skill syncSkill) error {
	value := renderSkillDiff(skill) + "\n"
	written, err := io.WriteString(output, value)
	if err != nil {
		return err
	}
	if written != len(value) {
		return io.ErrShortWrite
	}
	return nil
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
