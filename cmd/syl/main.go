package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"

	"github.com/igorrochap/syl/internal/adapters/commitgen"
	"github.com/igorrochap/syl/internal/adapters/gh"
	"github.com/igorrochap/syl/internal/adapters/git"
	"github.com/igorrochap/syl/internal/adapters/glab"
	"github.com/igorrochap/syl/internal/cli"
	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/harness/claude"
	"github.com/igorrochap/syl/internal/harness/codex"
	"github.com/igorrochap/syl/internal/orchestration"
	"github.com/igorrochap/syl/internal/sylhome"
	"github.com/igorrochap/syl/internal/tracker"
)

func main() {
	originRoot, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "syl: determine project root: %s\n", err)
		os.Exit(1)
	}
	sylHome, err := sylhome.Open(resolveSylHome())
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "syl: open syl home: %s\n", err)
		os.Exit(1)
	}
	app := cli.New(originRoot, originRoot, sylHome, cli.Dependencies{
		Input: os.Stdin,
		GH: func(root string) tracker.GHRunner {
			return gh.Runner{Dir: root}
		},
		GLab: newGLabRunner,
		Git: func(root string) orchestration.GitRunner {
			return git.ExecGitRunner{Dir: root}
		},
		Commitgen: commitgen.Runner{},
		Harnesses: func(root string) map[string]harness.Adapter {
			return map[string]harness.Adapter{
				"claude": claude.New(root),
				"codex":  codex.New(root),
			}
		},
	})
	exitCode := runWithInterruptContext(func(ctx context.Context) int {
		return app.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	})
	os.Exit(exitCode)
}

func runWithInterruptContext(run func(context.Context) int) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	return runUntilContextDone(ctx, stop, run)
}

func runUntilContextDone(ctx context.Context, stop func(), run func(context.Context) int) int {
	var stopOnce sync.Once
	stopSignal := func() { stopOnce.Do(stop) }
	finished := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			stopSignal()
		case <-finished:
		}
	}()
	exitCode := run(ctx)
	close(finished)
	stopSignal()
	return exitCode
}

func resolveSylHome() string {
	if configured := os.Getenv("SYL_HOME"); configured != "" {
		return configured
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".syl")
	}
	return filepath.Join(home, ".syl")
}

func newGLabRunner(root string) tracker.GLabRunner {
	return glab.Runner{Dir: root}
}
