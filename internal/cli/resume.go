package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/runrecord"
	"github.com/igorrochap/syl/internal/ui"
	"github.com/spf13/cobra"
)

const resumeRoles = "implement or review"

var errNoResumeRunDirectories = errors.New("no run directories found")

type resumeTarget struct {
	runDir     string
	iteration  int
	sessionID  string
	workRoot   string
	harness    config.Harness
	incomplete bool
}

type resumeSelection struct {
	ticketReference string
	iteration       int
	hasIteration    bool
}

func (a *App) resumeCommand() *cobra.Command {
	var iteration int
	command := &cobra.Command{
		Use:   "resume <role> [ticket]",
		Short: "re-enter the latest recorded Role session",
		Args:  resumeArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("iteration") && iteration < 0 {
				return errors.New("--iteration must be zero or greater")
			}
			ticketReference := ""
			if len(args) == 2 {
				ticketReference = canonicalIssueReference(args[1])
			}
			return a.runResumeCommand(cmd, args[0], resumeSelection{
				ticketReference: ticketReference,
				iteration:       iteration,
				hasIteration:    cmd.Flags().Changed("iteration"),
			})
		},
	}
	command.Flags().IntVar(&iteration, "iteration", -1, "resume the session recorded for this iteration")
	return command
}

func resumeArgs(_ *cobra.Command, args []string) error {
	if len(args) >= 1 && len(args) <= 2 {
		return nil
	}
	return fmt.Errorf("resume requires a Role and optional ticket; valid Roles: %s", resumeRoles)
}

func (a *App) runResumeCommand(cmd *cobra.Command, roleName string, selection resumeSelection) error {
	projectConfig, err := a.loadProjectConfig(cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	role, roleConfig, err := resumeRole(projectConfig, roleName)
	if err != nil {
		return err
	}
	target, err := resolveResumeTargetWithSelection(a.originRoot, role, selection)
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
	request := harness.Request{MCP: roleConfig.MCP, SandboxMode: roleConfig.SandboxMode}
	if err := adapter.AttachSession(cmd.Context(), target.sessionID, request); err != nil {
		return fmt.Errorf("resume %s session: %w", role, err)
	}
	return nil
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
	return resolveResumeTargetWithSelection(originRoot, role, resumeSelection{})
}

func resolveResumeTargetWithSelection(originRoot, role string, selection resumeSelection) (resumeTarget, error) {
	runDirs, err := resumeCandidateRunDirectories(originRoot, selection)
	if err != nil {
		return resumeTarget{}, err
	}
	for _, runDir := range runDirs {
		target, found, err := resumeTargetFromRun(runDir, role, selection)
		if err != nil {
			return resumeTarget{}, err
		}
		if found {
			return target, nil
		}
	}
	return resumeNoRoleSessionError(role, selection)
}

func resumeCandidateRunDirectories(originRoot string, selection resumeSelection) ([]string, error) {
	ticketNumber, err := resumeTicketNumber(selection.ticketReference)
	if err != nil {
		return nil, err
	}
	runDirs, err := resumeRunDirectories(originRoot)
	if err != nil {
		if ticketNumber != "" && errors.Is(err, errNoResumeRunDirectories) {
			return nil, noResumeRunsForTicket(selection.ticketReference)
		}
		return nil, err
	}
	if ticketNumber != "" {
		runDirs = filterResumeRunDirectories(runDirs, ticketNumber)
		if len(runDirs) == 0 {
			return nil, noResumeRunsForTicket(selection.ticketReference)
		}
	}
	return runDirs, nil
}

func resumeTargetFromRun(runDir, role string, selection resumeSelection) (resumeTarget, bool, error) {
	records, err := resumeSessionsForRole(runDir, role)
	if err != nil {
		return resumeTarget{}, false, err
	}
	if len(records) == 0 {
		return resumeTarget{}, false, nil
	}
	session, found := selectResumeSession(records, selection)
	if !found {
		return resumeTarget{}, false, resumeIterationError(runDir, role, selection.iteration, records)
	}
	target, err := buildResumeTarget(runDir, role, session)
	if err != nil {
		return resumeTarget{}, false, err
	}
	return target, true, nil
}

func resumeNoRoleSessionError(role string, selection resumeSelection) (resumeTarget, error) {
	if selection.ticketReference != "" {
		return resumeTarget{}, fmt.Errorf("no run for ticket %s has a recorded %s session", selection.ticketReference, role)
	}
	return resumeTarget{}, fmt.Errorf("no run has a recorded %s session", role)
}

func filterResumeRunDirectories(runDirs []string, ticketNumber string) []string {
	suffix := "-" + ticketNumber
	matching := make([]string, 0, len(runDirs))
	for _, runDir := range runDirs {
		if strings.HasSuffix(filepath.Base(runDir), suffix) {
			matching = append(matching, runDir)
		}
	}
	return matching
}

func resumeRunDirectories(originRoot string) ([]string, error) {
	runsDir := runrecord.RunsDirectory(originRoot)
	runDirs, err := runrecord.Directories(originRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w in %s", errNoResumeRunDirectories, runsDir)
		}
		return nil, fmt.Errorf("read run directories %s: %w", runsDir, err)
	}

	if len(runDirs) == 0 {
		return nil, fmt.Errorf("%w in %s", errNoResumeRunDirectories, runsDir)
	}
	sort.Slice(runDirs, func(i, j int) bool { return runDirs[i] > runDirs[j] })
	return runDirs, nil
}

func resumeTicketNumber(reference string) (string, error) {
	if strings.TrimSpace(reference) == "" {
		return "", nil
	}
	canonical := canonicalIssueReference(reference)
	number, err := strconv.Atoi(strings.TrimPrefix(canonical, "#"))
	if err != nil || number < 1 {
		return "", fmt.Errorf("invalid resume ticket reference %q; want N or #N", reference)
	}
	return strconv.Itoa(number), nil
}

func noResumeRunsForTicket(ticketReference string) error {
	return fmt.Errorf("no run directories found for ticket %s", ticketReference)
}

func resumeSessionsForRole(runDir, role string) ([]runrecord.Session, error) {
	records, err := runrecord.NewReader(nil).ReadSessions(runDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read sessions %s: %w", runDir, err)
	}

	matching := make([]runrecord.Session, 0, len(records))
	for _, record := range records {
		if record.Role == role {
			matching = append(matching, record)
		}
	}
	return matching, nil
}

func selectResumeSession(records []runrecord.Session, selection resumeSelection) (runrecord.Session, bool) {
	if selection.hasIteration {
		for _, record := range records {
			if record.Iteration == selection.iteration {
				return record, true
			}
		}
		return runrecord.Session{}, false
	}

	var selected runrecord.Session
	for index, record := range records {
		if index > 0 && record.Iteration <= selected.Iteration {
			continue
		}
		selected = record
	}
	return selected, len(records) > 0
}

func resumeIterationError(runDir, role string, iteration int, records []runrecord.Session) error {
	return fmt.Errorf(
		"run %s has no recorded %s session at iteration %d; recorded %s iterations: %s",
		filepath.Base(runDir), role, iteration, role, recordedResumeIterations(records),
	)
}

func recordedResumeIterations(records []runrecord.Session) string {
	seen := make(map[int]struct{}, len(records))
	iterations := make([]int, 0, len(records))
	for _, record := range records {
		if _, exists := seen[record.Iteration]; exists {
			continue
		}
		seen[record.Iteration] = struct{}{}
		iterations = append(iterations, record.Iteration)
	}
	sort.Ints(iterations)
	values := make([]string, 0, len(iterations))
	for _, iteration := range iterations {
		values = append(values, strconv.Itoa(iteration))
	}
	return strings.Join(values, ", ")
}

func buildResumeTarget(runDir, role string, session runrecord.Session) (resumeTarget, error) {
	metadata, err := runrecord.NewReader(nil).ReadMetadata(runDir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return resumeTarget{}, fmt.Errorf("read run metadata %s: %w", runDir, err)
		}
	}
	if metadata.WorkRoot == "" {
		return resumeTarget{}, fmt.Errorf(
			"run %s has no Work root line; runs recorded before this feature cannot be resumed",
			runDir,
		)
	}
	workRoot := metadata.WorkRoot
	if err := checkResumeWorkRoot(runDir, workRoot); err != nil {
		return resumeTarget{}, err
	}
	harnessName := config.Harness(metadata.HarnessFor(resumeRunKind(role)))
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

func resumeRunKind(role string) runrecord.Kind {
	if role == "implement" {
		return runrecord.Implement
	}
	return runrecord.Review
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
	exists, err := runrecord.NewReader(nil).HasSummary(runDir)
	if err != nil {
		return false, fmt.Errorf("check run summary %s: %w", runDir, err)
	}
	return !exists, nil
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
