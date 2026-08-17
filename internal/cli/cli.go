// Package cli contains the in-process command entrypoint used by rig and its tests.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/igorrochap/rig/internal/adapters/git"
	"github.com/igorrochap/rig/internal/adapters/notify"
	"github.com/igorrochap/rig/internal/config"
	"github.com/igorrochap/rig/internal/harness"
	"github.com/igorrochap/rig/internal/initializer"
	"github.com/igorrochap/rig/internal/orchestration"
	"github.com/igorrochap/rig/internal/tracker"
	"github.com/spf13/cobra"
)

type Dependencies struct {
	Input     io.Reader
	Harnesses map[string]harness.Adapter
	Notifier  orchestration.Notifier
	GH        tracker.GHRunner
	Git       orchestration.GitRunner
}

type App struct {
	projectRoot string
	deps        Dependencies
}

func New(projectRoot string, deps Dependencies) *App {
	if projectRoot == "" {
		projectRoot = "."
	}
	return &App{projectRoot: projectRoot, deps: deps}
}

func (a *App) Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}

	command := a.Command()
	command.SetArgs(args)
	command.SetOut(stdout)
	command.SetErr(stderr)
	if a.deps.Input != nil {
		command.SetIn(a.deps.Input)
	}
	if err := command.ExecuteContext(ctx); err != nil {
		fmt.Fprintf(stderr, "rig: %s\n", err)
		return 1
	}
	return 0
}

func (a *App) Command() *cobra.Command {
	root := &cobra.Command{
		Use:           "rig",
		Short:         "orchestrate an agentic coding workflow",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	root.AddCommand(
		a.initCommand(),
		a.stubCommand("sync", "synchronize tracker data"),
		a.planCommand(),
		a.implementCommand(),
		a.reviewCommand(),
	)
	return root
}

func (a *App) implementCommand() *cobra.Command {
	var verbose bool
	command := &cobra.Command{
		Use:   "implement #N",
		Short: "implement the current issue",
		Long:  "implement the current issue\n\n" + orchestration.QuestionInputHelp,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectConfig, err := config.Load(a.projectRoot)
			if err != nil {
				return err
			}
			if len(args) == 0 {
				return errors.New("implement requires an issue reference (#N)")
			}
			issueTracker, err := a.newIssueTracker(projectConfig)
			if err != nil {
				return err
			}
			ticket, err := issueTracker.Resolve(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			implementer, err := a.harness("implement", projectConfig.Roles.Implement.Harness)
			if err != nil {
				return err
			}
			reviewer, err := a.harness("review", projectConfig.Roles.Review.Harness)
			if err != nil {
				return err
			}
			return orchestration.RunImplement(cmd.Context(), orchestration.ImplementOptions{
				ProjectRoot: a.projectRoot, ProjectConfig: projectConfig, IssueTracker: issueTracker, Ticket: ticket,
				Implementer: implementer, Reviewer: reviewer, Git: a.gitRunner(),
				Notifier: a.notifier(projectConfig.Notifications.Enabled), Input: cmd.InOrStdin(), Output: cmd.OutOrStdout(),
				Verbose: verbose,
				IdentificationBanner: func(artifactDir string) error {
					return writeImplementBanner(cmd.OutOrStdout(), a.projectRoot, projectConfig, ticket, artifactDir)
				},
			})
		},
	}
	command.Flags().BoolVar(&verbose, "verbose", false, "stream assistant prose in addition to progress output")
	return command
}

func (a *App) reviewCommand() *cobra.Command {
	var raw bool
	var verbose bool
	command := &cobra.Command{
		Use:   "review [#N]",
		Short: "review the current working-tree changes",
		Long:  "review the current working-tree changes\n\n" + orchestration.QuestionInputHelp,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectConfig, err := config.Load(a.projectRoot)
			if err != nil {
				return err
			}
			if projectConfig.Tracker.Reviews == config.TrackerGitHub && len(args) == 0 {
				return errors.New("github review logging requires an issue reference (#N)")
			}

			var ticket *tracker.Ticket
			ticketRef := ""
			var issueTracker tracker.Tracker
			if len(args) == 1 {
				issueTracker, err = a.newIssueTracker(projectConfig)
				if err != nil {
					return err
				}
				resolved, err := issueTracker.Resolve(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				ticket = &resolved
				ticketRef = strings.TrimSpace(args[0])
			}
			adapter, err := a.harness("review", projectConfig.Roles.Review.Harness)
			if err != nil {
				return err
			}
			return orchestration.RunReview(cmd.Context(), orchestration.ReviewOptions{
				ProjectRoot: a.projectRoot, ProjectConfig: projectConfig, IssueTracker: issueTracker,
				Ticket: ticket, TicketRef: ticketRef, Adapter: adapter, Input: cmd.InOrStdin(), Output: cmd.OutOrStdout(),
				Raw: raw, Verbose: verbose, Notifier: a.notifier(projectConfig.Notifications.Enabled), Git: a.gitRunner(),
				IdentificationBanner: func() error {
					return writeReviewBanner(cmd.OutOrStdout(), projectConfig, ticketRef, ticket)
				},
			})
		},
	}
	command.Flags().BoolVar(&raw, "raw", false, "pass the harness output through untouched")
	command.Flags().BoolVar(&verbose, "verbose", false, "stream assistant prose in addition to progress output")
	command.MarkFlagsMutuallyExclusive("raw", "verbose")
	return command
}

func (a *App) planCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "plan [target]",
		Short: "plan the next role",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectConfig, err := config.Load(a.projectRoot)
			if err != nil {
				return err
			}
			if _, err := a.harness("plan", projectConfig.Roles.Plan.Harness); err != nil {
				return err
			}
			target := ""
			if len(args) == 1 {
				target = args[0]
			}
			if err := writePlanBanner(cmd.OutOrStdout(), projectConfig, target); err != nil {
				return err
			}
			return errors.New("plan: not implemented yet")
		},
	}
}

func (a *App) initCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "scaffold a project for the rig workflow",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return initializer.Run(a.projectRoot, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
}

func (a *App) stubCommand(name, description string) *cobra.Command {
	return &cobra.Command{
		Use:   name,
		Short: description,
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if _, err := config.Load(a.projectRoot); err != nil {
				return err
			}
			return fmt.Errorf("%s: not implemented yet", name)
		},
	}
}

func (a *App) harness(role string, name config.Harness) (harness.Adapter, error) {
	adapter, ok := a.deps.Harnesses[string(name)]
	if !ok || adapter == nil {
		return nil, fmt.Errorf("%s harness %q is not configured", role, name)
	}
	return adapter, nil
}

func (a *App) gitRunner() orchestration.GitRunner {
	if a.deps.Git != nil {
		return a.deps.Git
	}
	return git.ExecGitRunner{Dir: a.projectRoot}
}

func (a *App) notifier(enabled bool) orchestration.Notifier {
	if !enabled {
		return nil
	}
	if a.deps.Notifier != nil {
		return a.deps.Notifier
	}
	return notify.New()
}

func (a *App) newIssueTracker(projectConfig config.Config) (tracker.Tracker, error) {
	if projectConfig.Tracker.Issues == config.TrackerGitHub {
		return tracker.NewGitHub(a.deps.GH)
	}
	return tracker.NewLocal(a.projectRoot, "")
}
