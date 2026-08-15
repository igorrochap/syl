package main

import (
	"context"
	"fmt"
	"os"

	"github.com/igorrochap/rig/internal/cli"
)

func main() {
	projectRoot, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "rig: determine project root: %s\n", err)
		os.Exit(1)
	}

	app := cli.New(projectRoot, cli.Dependencies{GH: cli.ExecGHRunner{}})
	os.Exit(app.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}
