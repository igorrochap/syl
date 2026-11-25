package main

import (
	"context"
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

func TestRunUntilContextDoneStopsSignalHandlerDuringShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopCalled := make(chan struct{})
	stopCalls := 0

	exitCode := runUntilContextDone(ctx, func() {
		stopCalls++
		close(stopCalled)
	}, func(context.Context) int {
		cancel()
		<-stopCalled
		return 17
	})

	if exitCode != 17 {
		t.Fatalf("runUntilContextDone() = %d, want 17", exitCode)
	}
	if stopCalls != 1 {
		t.Fatalf("stop() calls = %d, want one", stopCalls)
	}
}

func TestRunWithInterruptContextPreservesExitCode(t *testing.T) {
	const wantExitCode = 23
	called := false

	gotExitCode := runWithInterruptContext(func(ctx context.Context) int {
		called = true
		if err := ctx.Err(); err != nil {
			t.Fatalf("run context error = %v, want active context", err)
		}
		return wantExitCode
	})

	if !called {
		t.Fatal("run callback was not called")
	}
	if gotExitCode != wantExitCode {
		t.Fatalf("runWithInterruptContext() = %d, want %d", gotExitCode, wantExitCode)
	}
}
