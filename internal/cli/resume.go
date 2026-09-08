package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/ui"
	"github.com/igorrochap/syl/internal/usage"
	"github.com/spf13/cobra"
)

const resumeRoles = "implement or review"

type resumeTarget struct {
	runDir     string
	iteration  int
	sessionID  string
	workRoot   string
	harness    config.Harness
	incomplete bool
}

type resumeMetadata struct {
	workRoot           string
	implementerHarness config.Harness
	reviewerHarness    config.Harness
}

func (a *App) resumeCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "resume <role>",
		Short: "re-enter the latest recorded Role session",
		Args:  resumeArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runResumeCommand(cmd, args[0])
		},
	}
	return command
}

func resumeArgs(_ *cobra.Command, args []string) error {
	if len(args) == 1 {
		return nil
	}
	return fmt.Errorf("resume requires exactly one Role; valid Roles: %s", resumeRoles)
}

func (a *App) runResumeCommand(cmd *cobra.Command, roleName string) error {
	role, roleConfig, err := loadResumeRole(a.originRoot, roleName)
	if err != nil {
		return err
	}
	target, err := resolveResumeTarget(a.originRoot, role)
	if err != nil {
		return err
	}
	adapter, err := a.harnessAtRoot(role, target.harness, target.workRoot)
	if err != nil {
		return err
	}
	if err := writeResumeBanner(cmd.OutOrStdout(), target, role); err != nil {
		return err
	}
	if target.incomplete {
		warning := fmt.Sprintf(
			"Warning: run %s did not complete; resuming its session may conflict with a run that is still executing.",
			filepath.Base(target.runDir),
		)
		if err := ui.New(cmd.OutOrStdout(), ui.DetectCaps(cmd.OutOrStdout())).Text(warning); err != nil {
			return fmt.Errorf("write resume warning: %w", err)
		}
	}
	request := harness.Request{MCP: roleConfig.MCP}
	if err := adapter.AttachSession(cmd.Context(), target.sessionID, request); err != nil {
		return fmt.Errorf("resume %s session: %w", role, err)
	}
	return nil
}

func loadResumeRole(originRoot, roleName string) (string, config.RoleConfig, error) {
	projectConfig, err := config.Load(originRoot)
	if err != nil {
		return "", config.RoleConfig{}, err
	}
	role, roleConfig, err := resumeRole(projectConfig, roleName)
	if err != nil {
		return "", config.RoleConfig{}, err
	}
	return role, roleConfig, nil
}

func resumeRole(projectConfig config.Config, roleName string) (string, config.RoleConfig, error) {
	switch strings.ToLower(strings.TrimSpace(roleName)) {
	case "implement":
		return "implement", projectConfig.Roles.Implement, nil
	case "review":
		return "review", projectConfig.Roles.Review, nil
	default:
		return "", config.RoleConfig{}, fmt.Errorf("unknown resume Role %q; valid Roles: %s", roleName, resumeRoles)
	}
}

func resolveResumeTarget(originRoot, role string) (resumeTarget, error) {
	runDirs, err := resumeRunDirectories(originRoot)
	if err != nil {
		return resumeTarget{}, err
	}
	for _, runDir := range runDirs {
		session, found, err := highestResumeSession(filepath.Join(runDir, "sessions.txt"), role)
		if err != nil {
			return resumeTarget{}, err
		}
		if !found {
			continue
		}
		return buildResumeTarget(runDir, role, session)
	}
	return resumeTarget{}, fmt.Errorf("no run has a recorded %s session", role)
}

func resumeRunDirectories(originRoot string) ([]string, error) {
	runsDir := filepath.Join(originRoot, ".syl", "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("no run directories found in %s", runsDir)
		}
		return nil, fmt.Errorf("read run directories %s: %w", runsDir, err)
	}

	var runDirs []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		runDirs = append(runDirs, filepath.Join(runsDir, entry.Name()))
	}
	if len(runDirs) == 0 {
		return nil, fmt.Errorf("no run directories found in %s", runsDir)
	}
	sort.Slice(runDirs, func(i, j int) bool { return runDirs[i] > runDirs[j] })
	return runDirs, nil
}

func highestResumeSession(path, role string) (usage.SessionRecord, bool, error) {
	records, err := usage.ReadSessionRecords(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return usage.SessionRecord{}, false, nil
		}
		return usage.SessionRecord{}, false, fmt.Errorf("read sessions %s: %w", path, err)
	}

	var selected usage.SessionRecord
	found := false
	for _, record := range records {
		if record.Role != role {
			continue
		}
		if found && record.Iteration <= selected.Iteration {
			continue
		}
		selected = record
		found = true
	}
	return selected, found, nil
}

func buildResumeTarget(runDir, role string, session usage.SessionRecord) (resumeTarget, error) {
	metadata, err := readResumeMetadata(filepath.Join(runDir, "metadata.txt"))
	if err != nil {
		return resumeTarget{}, err
	}
	if metadata.workRoot == "" {
		return resumeTarget{}, fmt.Errorf(
			"run %s has no Work root line; runs recorded before this feature cannot be resumed",
			runDir,
		)
	}
	workRoot := metadata.workRoot
	if err := checkResumeWorkRoot(runDir, workRoot); err != nil {
		return resumeTarget{}, err
	}
	harnessName := metadata.harnessFor(role)
	if harnessName == "" {
		return resumeTarget{}, fmt.Errorf("run %s has no recorded %s harness", runDir, role)
	}
	incomplete, err := resumeRunIsIncomplete(runDir)
	if err != nil {
		return resumeTarget{}, err
	}
	return resumeTarget{
		runDir: runDir, iteration: session.Iteration, sessionID: session.SessionID,
		workRoot: workRoot, harness: harnessName, incomplete: incomplete,
	}, nil
}

func readResumeMetadata(path string) (resumeMetadata, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return resumeMetadata{}, nil
		}
		return resumeMetadata{}, fmt.Errorf("read run metadata %s: %w", path, err)
	}

	var metadata resumeMetadata
	for _, line := range strings.Split(string(contents), "\n") {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "work root":
			metadata.workRoot = strings.TrimSpace(value)
		case "implementer harness":
			metadata.implementerHarness = config.Harness(strings.TrimSpace(value))
		case "reviewer harness":
			metadata.reviewerHarness = config.Harness(strings.TrimSpace(value))
		}
	}
	return metadata, nil
}

func (m resumeMetadata) harnessFor(role string) config.Harness {
	if role == "implement" {
		return m.implementerHarness
	}
	return m.reviewerHarness
}

func checkResumeWorkRoot(runDir, workRoot string) error {
	info, err := os.Stat(workRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("recorded Work root %q for run %s does not exist", workRoot, runDir)
		}
		return fmt.Errorf("check recorded Work root %q for run %s: %w", workRoot, runDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("recorded Work root %q for run %s is not a directory", workRoot, runDir)
	}
	return nil
}

func resumeRunIsIncomplete(runDir string) (bool, error) {
	_, err := os.Stat(filepath.Join(runDir, "summary.txt"))
	if err == nil {
		return false, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	return false, fmt.Errorf("check run summary %s: %w", runDir, err)
}

func writeResumeBanner(output io.Writer, target resumeTarget, role string) error {
	err := ui.New(output, ui.DetectCaps(output)).Banner(ui.Banner{
		Title: fmt.Sprintf("syl resume — %s", filepath.Base(target.runDir)),
		Rows: []ui.Field{
			{Label: "role", Value: role},
			{Label: "iteration", Value: fmt.Sprintf("%d", target.iteration)},
			{Label: "harness", Value: string(target.harness)},
			{Label: "session", Value: target.sessionID},
		},
	})
	if err != nil {
		return fmt.Errorf("write resume banner: %w", err)
	}
	return nil
}
