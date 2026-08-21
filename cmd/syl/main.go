package main

import (
	"context"
	"fmt"
	"os"

	"github.com/igorrochap/syl/internal/adapters/gh"
	"github.com/igorrochap/syl/internal/adapters/git"
	"github.com/igorrochap/syl/internal/cli"
	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/harness/claude"
	"github.com/igorrochap/syl/internal/harness/codex"
)

func main() {
	projectRoot, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "syl: determine project root: %s\n", err)
		os.Exit(1)
	}

	app := cli.New(projectRoot, cli.Dependencies{
		Input: os.Stdin,
		GH:    gh.Runner{Dir: projectRoot},
		Git:   git.ExecGitRunner{Dir: projectRoot},
		Harnesses: map[string]harness.Adapter{
			"claude": claude.New(projectRoot),
			"codex":  codex.New(projectRoot),
		},
		PTYHarnesses: map[string]harness.Adapter{
			"claude": claude.NewPTY(projectRoot),
		},
	})
	os.Exit(app.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}
