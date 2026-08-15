// Package cli contains the in-process command entrypoint used by rig and its tests.
package cli

import (
	"context"
	"fmt"
	"io"
	"os/exec"

	"github.com/igorrochap/rig/internal/config"
	"github.com/spf13/cobra"
)

type HarnessAdapter interface {
	Run(ctx context.Context, model, effort, prompt string) (string, error)
}

type Notifier interface {
	Notify(ctx context.Context, message string) error
}

type GHRunner interface {
	Run(ctx context.Context, args ...string) (string, error)
}

type Dependencies struct {
	Harnesses map[string]HarnessAdapter
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
		a.stubCommand("implement", "implement the current issue"),
		a.stubCommand("review", "review the implementation"),
	)
	return root
}

func (a *App) initCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "initialize .rig/config.toml",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := config.Init(a.projectRoot)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Created %s\n", path)
			return nil
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

type ExecGHRunner struct{}

var _ GHRunner = ExecGHRunner{}

func (ExecGHRunner) Run(ctx context.Context, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "gh", args...)
	output, err := command.Output()
	if err != nil {
		return string(output), fmt.Errorf("run gh: %w", err)
	}
	return string(output), nil
}
