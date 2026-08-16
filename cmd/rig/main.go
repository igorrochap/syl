package main

import (
	"context"
	"fmt"
	"os"

	"github.com/igorrochap/rig/internal/cli"
	"github.com/igorrochap/rig/internal/harness"
	"github.com/igorrochap/rig/internal/harness/claude"
)

func main() {
	projectRoot, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "rig: determine project root: %s\n", err)
		os.Exit(1)
	}

	app := cli.New(projectRoot, cli.Dependencies{
		Input: os.Stdin,
		GH:    cli.ExecGHRunner{},
		Harnesses: map[string]harness.Adapter{
			"claude": claude.New(projectRoot),
		},
	})
	os.Exit(app.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}
