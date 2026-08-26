package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunWorktreeSetupExecutesInWorktreeAndStreamsOutput(t *testing.T) {
	worktreePath := t.TempDir()
	var stdout, stderr strings.Builder

	err := RunWorktreeSetup(
		context.Background(),
		"printf setup-stdout; printf setup-stderr >&2; touch setup-marker",
		worktreePath,
		&stdout,
		&stderr,
	)
	if err != nil {
		t.Fatalf("RunWorktreeSetup() error = %v", err)
	}
	if got := stdout.String(); got != "setup-stdout" {
		t.Fatalf("stdout = %q, want live command stdout", got)
	}
	if got := stderr.String(); got != "setup-stderr" {
		t.Fatalf("stderr = %q, want live command stderr", got)
	}
	if _, err := os.Stat(filepath.Join(worktreePath, "setup-marker")); err != nil {
		t.Fatalf("setup marker: %v", err)
	}
}

func TestRunWorktreeSetupReturnsCommandFailure(t *testing.T) {
	var stderr strings.Builder

	err := RunWorktreeSetup(
		context.Background(),
		"printf setup-failed >&2; exit 17",
		t.TempDir(),
		nil,
		&stderr,
	)
	if err == nil || !strings.Contains(err.Error(), "run worktree setup") {
		t.Fatalf("RunWorktreeSetup() error = %v, want command failure", err)
	}
	if got := stderr.String(); got != "setup-failed" {
		t.Fatalf("stderr = %q, want failure output", got)
	}
}

func TestRunWorktreeSetupHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worktreePath := t.TempDir()
	startedPath := filepath.Join(worktreePath, "setup-started")

	result := make(chan error, 1)
	go func() {
		result <- RunWorktreeSetup(ctx, "touch setup-started; sleep 30", worktreePath, nil, nil)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(startedPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("setup process did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "run worktree setup") {
			t.Fatalf("RunWorktreeSetup() error = %v, want cancellation failure", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunWorktreeSetup() did not stop after cancellation")
	}
}
