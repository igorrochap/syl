package orchestration

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunArtifactsRecordSessionsDeduplicatesIDs(t *testing.T) {
	artifacts := runArtifacts{dir: t.TempDir()}
	artifacts.recordSessions(2, "review", []string{"review-session", "review-session"})
	artifacts.recordSessions(1, "implement", []string{"review-session"})
	artifacts.recordSessions(1, "review", []string{"review-session", "review-session-2"})
	artifacts.recordSessions(2, "review", []string{"review-session", "review-session-2", ""})

	if got, want := len(artifacts.sessions), 5; got != want {
		t.Fatalf("recorded sessions = %d, want %d: %v", got, want, artifacts.sessions)
	}

	if err := artifacts.writeSessions(); err != nil {
		t.Fatalf("writeSessions() error = %v", err)
	}

	got, err := os.ReadFile(filepath.Join(artifacts.dir, "sessions.txt"))
	if err != nil {
		t.Fatalf("read sessions.txt: %v", err)
	}
	want := "iteration 1 implement: review-session\n" +
		"iteration 1 review: review-session\n" +
		"iteration 1 review: review-session-2\n" +
		"iteration 2 review: review-session\n" +
		"iteration 2 review: review-session-2\n"
	if string(got) != want {
		t.Fatalf("sessions.txt = %q, want %q", got, want)
	}
}
