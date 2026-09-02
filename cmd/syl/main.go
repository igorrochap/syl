package main

import (
	"context"
	"fmt"
	"os"

	"github.com/igorrochap/syl/internal/adapters/gh"
	"github.com/igorrochap/syl/internal/adapters/git"
	"github.com/igorrochap/syl/internal/adapters/glab"
	"github.com/igorrochap/syl/internal/cli"
	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/harness/claude"
	"github.com/igorrochap/syl/internal/harness/codex"
	"github.com/igorrochap/syl/internal/orchestration"
	"github.com/igorrochap/syl/internal/tracker"
)

func main() {
	originRoot, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "syl: determine project root: %s\n", err)
		os.Exit(1)
	}

	app := cli.New(originRoot, originRoot, cli.Dependencies{
		Input: os.Stdin,
		GH: func(root string) tracker.GHRunner {
			return gh.Runner{Dir: root}
		},
		GLab: newGLabRunner,
		Git: func(root string) orchestration.GitRunner {
			return git.ExecGitRunner{Dir: root}
		},
		Harnesses: func(root string) map[string]harness.Adapter {
			return map[string]harness.Adapter{
				"claude": claude.New(root),
				"codex":  codex.New(root),
			}
		},
	})
	os.Exit(app.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func newGLabRunner(root string) tracker.GLabRunner {
	return glab.Runner{Dir: root}
}
