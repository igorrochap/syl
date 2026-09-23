package main

import (
	"os"
	"path/filepath"
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

func TestResolveSylHomeUsesConfiguredPath(t *testing.T) {
	configured := filepath.Join(t.TempDir(), "syl")
	t.Setenv("SYL_HOME", configured)

	got := resolveSylHome()
	if got != configured {
		t.Fatalf("resolveSylHome() = %q, want %q", got, configured)
	}
}

func TestResolveSylHomeDefaultsToUserHome(t *testing.T) {
	t.Setenv("SYL_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}

	got := resolveSylHome()
	want := filepath.Join(home, ".syl")
	if got != want {
		t.Fatalf("resolveSylHome() = %q, want %q", got, want)
	}
}
