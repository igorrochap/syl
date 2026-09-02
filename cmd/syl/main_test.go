package main

import (
	"testing"

	"github.com/igorrochap/syl/internal/adapters/glab"
)

func TestNewGLabRunnerUsesSuppliedRoot(t *testing.T) {
	const root = "/project"

	runner, ok := newGLabRunner(root).(glab.Runner)
	if !ok {
		t.Fatalf("newGLabRunner() = %T, want glab.Runner", newGLabRunner(root))
	}
	if runner.Dir != root {
		t.Fatalf("newGLabRunner() directory = %q, want %q", runner.Dir, root)
	}
}
