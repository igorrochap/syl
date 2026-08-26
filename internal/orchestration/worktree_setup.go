package orchestration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// RunWorktreeSetup runs one project's dependency setup command in a
// provisioned worktree. The command is intentionally passed as one shell
// string so projects can use pipelines, conditionals, and their native
// package-manager command without syl needing to parse it.
func RunWorktreeSetup(ctx context.Context, command, worktreePath string, stdout, stderr io.Writer) error {
	if strings.TrimSpace(command) == "" {
		return nil
	}
	if strings.TrimSpace(worktreePath) == "" {
		return errors.New("run worktree setup: worktree path is required")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("run worktree setup: %w", err)
	}

	process := exec.Command("sh", "-c", command)
	process.Dir = worktreePath
	process.Stdout = stdout
	process.Stderr = stderr
	configureSetupProcess(process)
	if err := process.Start(); err != nil {
		return fmt.Errorf("run worktree setup: %w", err)
	}

	wait := make(chan error, 1)
	go func() {
		wait <- process.Wait()
	}()
	select {
	case err := <-wait:
		if err != nil {
			return fmt.Errorf("run worktree setup: %w", err)
		}
		return nil
	case <-ctx.Done():
		_ = killSetupProcess(process)
		<-wait
		return fmt.Errorf("run worktree setup: %w", ctx.Err())
	}
}
