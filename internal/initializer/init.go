package initializer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/tui"
	vendoredskills "github.com/igorrochap/syl/skills"
)

const (
	gitignoreEntry = ".syl/runs/"
	minimalAgents  = "# Project instructions\n\nThis project is managed with syl.\n"
)

type skillManifest struct {
	Version int             `json:"version"`
	Skills  []manifestSkill `json:"skills"`
}

type manifestSkill struct {
	Name           string `json:"name"`
	Classification string `json:"classification"`
}

type initPlan struct {
	root                 string
	config               config.Config
	installedSkills      []string
	createAgents         bool
	claudeLink           linkPlan
	claudeFileLink       linkPlan
	writeConfig          bool
	updateGitignore      bool
	changes              []string
	initialized          bool
	requiresConfirmation bool
}

type linkPlan struct {
	linkPath string
	target   string
	label    string
	replace  bool
	needed   bool
}

func initPromptSpecs(manifest skillManifest) []tui.PromptSpec {
	optional := optionalSkillNames(manifest)
	specs := make([]tui.PromptSpec, 0, 12)
	if len(optional) > 0 {
		specs = append(specs, tui.PromptSpec{Key: "optional", Label: "Optional skills", Kind: tui.MultiPrompt, Options: optional})
	}
	specs = append(specs,
		tui.PromptSpec{Key: "tracker.issues", Label: "Issues tracker", Kind: tui.ChoicePrompt, Options: []string{"github", "local"}, DefaultValue: "github"},
		tui.PromptSpec{Key: "tracker.reviews", Label: "Review log", Kind: tui.ChoicePrompt, Options: []string{"github", "local"}, DefaultValue: "local"},
	)
	for _, role := range []struct {
		name, harness, model, effort string
	}{
		{name: "plan", harness: "claude", model: "claude-opus-5", effort: "high"},
		{name: "implement", harness: "codex", model: "gpt-5.6-luna", effort: "xhigh"},
		{name: "review", harness: "claude", model: "claude-sonnet-5", effort: "medium"},
	} {
		specs = append(specs,
			tui.PromptSpec{Key: role.name + ".harness", Label: role.name + " harness", Kind: tui.ChoicePrompt, Options: []string{"claude", "codex", "opencode"}, DefaultValue: role.harness},
			tui.PromptSpec{Key: role.name + ".model", Label: role.name + " model", Kind: tui.TextPrompt, DefaultValue: role.model},
			tui.PromptSpec{Key: role.name + ".effort", Label: role.name + " effort", Kind: tui.ChoicePrompt, Options: []string{"low", "medium", "high", "xhigh"}, DefaultValue: role.effort},
		)
	}
	return specs
}

func runInitWizard(projectRoot string, input io.Reader, output io.Writer, manifest skillManifest) (initPlan, bool, error) {
	var plan initPlan
	finalizer := func(answers map[string]tui.Answer) (tui.PromptSpec, bool, error) {
		projectConfig, optional, err := configFromAnswers(answers)
		if err != nil {
			return tui.PromptSpec{}, false, err
		}
		plan, err = makeInitPlan(projectRoot, projectConfig, manifest, optional)
		if err != nil {
			return tui.PromptSpec{}, false, err
		}
		if len(plan.changes) == 0 || !plan.requiresConfirmation {
			return tui.PromptSpec{}, false, nil
		}
		return tui.PromptSpec{Key: "confirmation", Label: changesPrompt(plan), Kind: tui.ChoicePrompt, Options: []string{"yes", "no"}, DefaultValue: "no"}, true, nil
	}
	answers, err := tui.Run(input, output, initPromptSpecs(manifest), finalizer)
	if err != nil {
		return initPlan{}, false, err
	}
	confirmed := !plan.requiresConfirmation || answers["confirmation"].Value == "yes"
	return plan, confirmed, nil
}

func configFromAnswers(answers map[string]tui.Answer) (config.Config, []string, error) {
	value := func(key string) string { return answers[key].Value }
	roles := config.RolesConfig{
		Plan:      config.RoleConfig{Harness: config.Harness(value("plan.harness")), Model: value("plan.model"), Effort: config.Effort(value("plan.effort"))},
		Implement: config.RoleConfig{Harness: config.Harness(value("implement.harness")), Model: value("implement.model"), Effort: config.Effort(value("implement.effort"))},
		Review:    config.RoleConfig{Harness: config.Harness(value("review.harness")), Model: value("review.model"), Effort: config.Effort(value("review.effort"))},
	}
	return config.Config{
		Tracker: config.TrackerConfig{Issues: config.Tracker(value("tracker.issues")), Reviews: config.Tracker(value("tracker.reviews"))},
		Roles:   roles, Loop: config.LoopConfig{MaxIterations: 3}, Notifications: config.NotificationsConfig{Enabled: true},
	}, answers["optional"].Selected, nil
}

func optionalSkillNames(manifest skillManifest) []string {
	optional := make([]string, 0)
	for _, skill := range manifest.Skills {
		if skill.Classification == "optional" {
			optional = append(optional, skill.Name)
		}
	}
	sort.Strings(optional)
	return optional
}

func changesPrompt(plan initPlan) string {
	var builder strings.Builder
	builder.WriteString("Changes:\n")
	for _, change := range plan.changes {
		fmt.Fprintf(&builder, "- %s\n", change)
	}
	builder.WriteString("\nApply these changes?")
	return builder.String()
}

// Run initializes a project using the vendored skill set and interactive prompt engine.
func Run(projectRoot string, input io.Reader, output io.Writer) error {
	if err := rejectRealClaudePath(projectRoot); err != nil {
		return err
	}

	manifest, err := readSkillManifest()
	if err != nil {
		return err
	}
	plan, confirmed, err := runInitWizard(projectRoot, input, output, manifest)
	if err != nil {
		return err
	}
	if len(plan.changes) == 0 {
		fmt.Fprintln(output, "Project is already initialized; no changes needed.")
		return nil
	}

	if plan.requiresConfirmation {
		fmt.Fprintln(output, "Changes:")
		for _, change := range plan.changes {
			fmt.Fprintf(output, "- %s\n", change)
		}
		if !confirmed {
			fmt.Fprintln(output, "No changes made.")
			return nil
		}
	}

	if err := applyInitPlan(plan); err != nil {
		return err
	}
	fmt.Fprintf(output, "Initialized project in %s (config %s).\n", projectRoot, filepath.ToSlash(filepath.Join(".syl", "config.toml")))
	return nil
}

func readSkillManifest() (skillManifest, error) {
	contents, err := fs.ReadFile(vendoredskills.Assets, "manifest.json")
	if err != nil {
		return skillManifest{}, fmt.Errorf("read vendored skill manifest: %w", err)
	}
	var manifest skillManifest
	if err := json.Unmarshal(contents, &manifest); err != nil {
		return skillManifest{}, fmt.Errorf("parse vendored skill manifest: %w", err)
	}
	seen := make(map[string]bool, len(manifest.Skills))
	for _, skill := range manifest.Skills {
		if skill.Name == "" || seen[skill.Name] {
			return skillManifest{}, fmt.Errorf("invalid vendored skill manifest entry %q", skill.Name)
		}
		if skill.Classification != "core" && skill.Classification != "optional" {
			return skillManifest{}, fmt.Errorf("skill %q has invalid classification %q", skill.Name, skill.Classification)
		}
		if _, err := fs.Stat(vendoredskills.Assets, path.Join(skill.Name, "SKILL.md")); err != nil {
			return skillManifest{}, fmt.Errorf("skill %q is missing SKILL.md: %w", skill.Name, err)
		}
		seen[skill.Name] = true
	}
	return manifest, nil
}

func makeInitPlan(projectRoot string, projectConfig config.Config, manifest skillManifest, optional []string) (initPlan, error) {
	plan := initPlan{root: projectRoot, config: projectConfig}
	plan.initialized = hasInitMarker(projectRoot)
	installedSkills, skillChanges, err := planSkills(projectRoot, manifest, optional)
	if err != nil {
		return initPlan{}, err
	}
	plan.installedSkills = installedSkills
	plan.changes = append(plan.changes, skillChanges...)

	plan.createAgents, err = needsAgentsFile(projectRoot)
	if err != nil {
		return initPlan{}, err
	}
	if plan.createAgents {
		plan.changes = append(plan.changes, "create AGENTS.md")
	}

	claudeLink, claudeFileLink, linkChanges, err := planLayout(projectRoot)
	if err != nil {
		return initPlan{}, err
	}
	plan.claudeLink = claudeLink
	plan.claudeFileLink = claudeFileLink
	plan.changes = append(plan.changes, linkChanges...)
	plan.requiresConfirmation = claudeLink.replace || claudeFileLink.replace

	var configChange string
	var configRequiresConfirmation bool
	plan.writeConfig, configChange, configRequiresConfirmation, err = planConfig(projectRoot, projectConfig)
	if err != nil {
		return initPlan{}, err
	}
	if configChange != "" {
		plan.changes = append(plan.changes, configChange)
		plan.requiresConfirmation = plan.requiresConfirmation || configRequiresConfirmation
	}

	plan.updateGitignore, err = gitignoreNeedsUpdate(projectRoot)
	if err != nil {
		return initPlan{}, err
	}
	if plan.updateGitignore {
		plan.changes = append(plan.changes, "ensure .syl/runs/ is gitignored")
	}
	if plan.initialized && len(plan.changes) > 0 {
		plan.requiresConfirmation = true
	}

	return plan, nil
}

func planSkills(projectRoot string, manifest skillManifest, optional []string) ([]string, []string, error) {
	selected := make(map[string]bool, len(optional))
	for _, name := range optional {
		selected[name] = true
	}
	var installed []string
	var changes []string
	for _, skill := range manifest.Skills {
		if skill.Classification != "core" && !selected[skill.Name] {
			continue
		}
		differs, err := installedSkillDiffers(projectRoot, skill.Name)
		if err != nil {
			return nil, nil, err
		}
		if !differs {
			continue
		}
		installed = append(installed, skill.Name)
		if skillExists(projectRoot, skill.Name) {
			changes = append(changes, "overwrite skill "+skill.Name)
		} else {
			changes = append(changes, "install skill "+skill.Name)
		}
	}
	return installed, changes, nil
}

func needsAgentsFile(projectRoot string) (bool, error) {
	agentsPath := filepath.Join(projectRoot, "AGENTS.md")
	_, err := os.Lstat(agentsPath)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect %s: %w", agentsPath, err)
	}
	return false, nil
}

func planLayout(projectRoot string) (linkPlan, linkPlan, []string, error) {
	claudeLink, err := planLink(projectRoot, ".claude", ".agents", ".claude symlink")
	if err != nil {
		return linkPlan{}, linkPlan{}, nil, err
	}
	claudeFileLink, err := planLink(projectRoot, "CLAUDE.md", "AGENTS.md", "CLAUDE.md symlink")
	if err != nil {
		return linkPlan{}, linkPlan{}, nil, err
	}
	var changes []string
	if claudeLink.needed {
		changes = append(changes, linkDescription(claudeLink))
	}
	if claudeFileLink.needed {
		changes = append(changes, linkDescription(claudeFileLink))
	}
	return claudeLink, claudeFileLink, changes, nil
}

func planConfig(projectRoot string, projectConfig config.Config) (bool, string, bool, error) {
	configPath := config.Path(projectRoot)
	configInfo, err := os.Lstat(configPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return true, "write " + filepath.ToSlash(filepath.Join(".syl", "config.toml")), false, nil
	case err != nil:
		return false, "", false, fmt.Errorf("inspect config %s: %w", configPath, err)
	case configInfo.IsDir():
		return false, "", false, fmt.Errorf("config path %s is a directory", configPath)
	}
	contents, err := os.ReadFile(configPath)
	if err != nil {
		return false, "", false, fmt.Errorf("read config %s: %w", configPath, err)
	}
	if bytes.Equal(contents, []byte(config.Render(projectConfig))) {
		return false, "", false, nil
	}
	return true, "update " + filepath.ToSlash(filepath.Join(".syl", "config.toml")), true, nil
}

func applyInitPlan(plan initPlan) error {
	if err := os.MkdirAll(filepath.Join(plan.root, ".agents", "skills"), 0o755); err != nil {
		return fmt.Errorf("create .agents/skills: %w", err)
	}
	for _, skill := range plan.installedSkills {
		if err := copySkill(plan.root, skill); err != nil {
			return err
		}
	}
	if plan.createAgents {
		if err := os.WriteFile(filepath.Join(plan.root, "AGENTS.md"), []byte(minimalAgents), 0o644); err != nil {
			return fmt.Errorf("create AGENTS.md: %w", err)
		}
	}
	if err := applyLink(plan.claudeLink); err != nil {
		return err
	}
	if err := applyLink(plan.claudeFileLink); err != nil {
		return err
	}
	if plan.writeConfig {
		if _, err := config.Write(plan.root, plan.config, config.OverwriteExisting); err != nil {
			return err
		}
	}
	if plan.updateGitignore {
		if err := ensureGitignore(plan.root); err != nil {
			return err
		}
	}
	return nil
}

func copySkill(projectRoot, name string) error {
	err := walkSkillFiles(projectRoot, name, func(sourcePath, destination string) error {
		contents, err := fs.ReadFile(vendoredskills.Assets, sourcePath)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return fmt.Errorf("create skill directory %s: %w", name, err)
		}
		if err := os.WriteFile(destination, contents, 0o644); err != nil {
			return fmt.Errorf("write skill %s: %w", name, err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("copy skill %s: %w", name, err)
	}
	return nil
}

func installedSkillDiffers(projectRoot, name string) (bool, error) {
	destinationRoot := filepath.Join(projectRoot, ".agents", "skills", name)
	info, err := os.Lstat(destinationRoot)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect installed skill %s: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, fmt.Errorf("installed skill %s is not a directory; resolve it manually", name)
	}
	differs := false
	err = walkSkillFiles(projectRoot, name, func(sourcePath, destination string) error {
		want, err := fs.ReadFile(vendoredskills.Assets, sourcePath)
		if err != nil {
			return err
		}
		got, err := os.ReadFile(destination)
		if errors.Is(err, os.ErrNotExist) {
			differs = true
			return fs.SkipAll
		}
		if err != nil {
			return err
		}
		if !bytes.Equal(got, want) {
			differs = true
			return fs.SkipAll
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return false, fmt.Errorf("compare installed skill %s: %w", name, err)
	}
	return differs, nil
}

func walkSkillFiles(projectRoot, name string, visit func(sourcePath, destination string) error) error {
	return fs.WalkDir(vendoredskills.Assets, name, func(sourcePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative := strings.TrimPrefix(sourcePath, name+"/")
		destination := filepath.Join(projectRoot, ".agents", "skills", name, filepath.FromSlash(relative))
		return visit(sourcePath, destination)
	})
}

func skillExists(projectRoot, name string) bool {
	_, err := os.Lstat(filepath.Join(projectRoot, ".agents", "skills", name))
	return err == nil
}

func rejectRealClaudePath(projectRoot string) error {
	claudePath := filepath.Join(projectRoot, ".claude")
	info, err := os.Lstat(claudePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect %s: %w", claudePath, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		if info.IsDir() {
			return fmt.Errorf("%s already exists as a real directory; resolve it manually before running syl init", claudePath)
		}
		return fmt.Errorf("%s already exists and is not a symlink; resolve it manually before running syl init", claudePath)
	}
	return nil
}

func planLink(projectRoot, relativePath, target, label string) (linkPlan, error) {
	link := linkPlan{
		linkPath: filepath.Join(projectRoot, relativePath),
		target:   target,
		label:    label,
	}
	info, err := os.Lstat(link.linkPath)
	if errors.Is(err, os.ErrNotExist) {
		link.needed = true
		return link, nil
	}
	if err != nil {
		return linkPlan{}, fmt.Errorf("inspect %s: %w", link.linkPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		got, err := os.Readlink(link.linkPath)
		if err != nil {
			return linkPlan{}, fmt.Errorf("readlink %s: %w", link.linkPath, err)
		}
		if got == target {
			return link, nil
		}
		link.needed = true
		link.replace = true
		return link, nil
	}
	if info.IsDir() {
		return linkPlan{}, fmt.Errorf("%s is a real directory; resolve it manually before running syl init", link.linkPath)
	}
	link.needed = true
	link.replace = true
	return link, nil
}

func linkDescription(link linkPlan) string {
	if link.replace {
		return fmt.Sprintf("replace %s with symlink to %s", link.label, link.target)
	}
	return fmt.Sprintf("create %s -> %s", link.label, link.target)
}

func applyLink(link linkPlan) error {
	if !link.needed {
		return nil
	}
	if link.replace {
		if err := os.Remove(link.linkPath); err != nil {
			return fmt.Errorf("replace %s: %w", link.linkPath, err)
		}
	}
	if err := os.Symlink(link.target, link.linkPath); err != nil {
		return fmt.Errorf("create symlink %s: %w", link.linkPath, err)
	}
	return nil
}

func hasInitMarker(projectRoot string) bool {
	for _, relativePath := range []string{".syl/config.toml", ".claude", "CLAUDE.md", ".agents/skills"} {
		if _, err := os.Lstat(filepath.Join(projectRoot, filepath.FromSlash(relativePath))); err == nil {
			return true
		}
	}
	return false
}

func gitignoreNeedsUpdate(projectRoot string) (bool, error) {
	if _, err := os.Lstat(filepath.Join(projectRoot, ".git")); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("inspect .git: %w", err)
	}
	contents, err := os.ReadFile(filepath.Join(projectRoot, ".gitignore"))
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("read .gitignore: %w", err)
	}
	count := 0
	for _, line := range strings.Split(strings.ReplaceAll(string(contents), "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == gitignoreEntry {
			count++
		}
	}
	return count != 1, nil
}

func ensureGitignore(projectRoot string) error {
	gitignorePath := filepath.Join(projectRoot, ".gitignore")
	contents, err := os.ReadFile(gitignorePath)
	if errors.Is(err, os.ErrNotExist) {
		contents = nil
	} else if err != nil {
		return fmt.Errorf("read .gitignore: %w", err)
	}
	lines := strings.Split(strings.ReplaceAll(string(contents), "\r\n", "\n"), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	result := make([]string, 0, len(lines)+1)
	found := false
	for _, line := range lines {
		if strings.TrimSpace(line) == gitignoreEntry {
			if found {
				continue
			}
			found = true
		}
		result = append(result, line)
	}
	if !found {
		result = append(result, gitignoreEntry)
	}
	if err := os.WriteFile(gitignorePath, []byte(strings.Join(result, "\n")+"\n"), 0o644); err != nil {
		return fmt.Errorf("write .gitignore: %w", err)
	}
	return nil
}
