// Package cli contains the in-process command entrypoint used by rig and its tests.
package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/igorrochap/rig/internal/config"
	"github.com/igorrochap/rig/internal/harness"
	"github.com/spf13/cobra"
)

type Notifier interface {
	Notify(ctx context.Context, message string) error
}

type GHRunner interface {
	Run(ctx context.Context, args ...string) (string, error)
}

type Dependencies struct {
	Input     io.Reader
	Harnesses map[string]harness.Adapter
	Notifier  Notifier
	GH        GHRunner
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
		a.stubCommand("plan", "plan the next role"),
		a.implementCommand(),
		a.reviewCommand(),
	)
	return root
}

func (a *App) implementCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "implement #N",
		Short: "implement the current issue",
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
			if err := issueTracker.UpdateStatus(cmd.Context(), ticket.Number, "doing"); err != nil {
				return fmt.Errorf("mark ticket #%d as doing: %w", ticket.Number, err)
			}
			return nil
		},
	}
}

func (a *App) initCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "scaffold a project for the rig workflow",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInit(a.projectRoot, cmd.InOrStdin(), cmd.OutOrStdout())
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

type ExecGHRunner struct {
	Dir string
}

var _ GHRunner = ExecGHRunner{}

func (r ExecGHRunner) Run(ctx context.Context, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "gh", args...)
	if r.Dir != "" {
		command.Dir = r.Dir
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		errOutput := strings.TrimSpace(stderr.String())
		if errOutput == "" {
			return string(output), fmt.Errorf("run gh: %w", err)
		}
		return string(output), fmt.Errorf("run gh: %w: %s", err, errOutput)
	}
	return string(output), nil
}
