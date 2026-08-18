package orchestration

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
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

func TestShouldColorizeOutputRequiresATerminal(t *testing.T) {
	var buf bytes.Buffer
	if shouldColorizeOutput(&buf) {
		t.Fatal("shouldColorizeOutput(bytes.Buffer) = true, want false: no Fd() method")
	}

	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	defer read.Close()
	defer write.Close()

	if shouldColorizeOutput(write) {
		t.Fatal("shouldColorizeOutput(pipe) = true, want false: a pipe is never a terminal")
	}
}

func TestShouldColorizeOutputHonorsNoColorEnv(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	defer read.Close()
	defer write.Close()

	t.Setenv("NO_COLOR", "1")
	if shouldColorizeOutput(write) {
		t.Fatal("shouldColorizeOutput() = true with NO_COLOR set, want false")
	}
}

func TestRoleLabelWriterPlainPrefixesEveryLine(t *testing.T) {
	var buf bytes.Buffer
	writer := newRoleLabelWriter(&buf, "implement", ansiColorImplement)

	if _, err := writer.Write([]byte("first line\nsecond ")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if _, err := writer.Write([]byte("line\n")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	got := buf.String()
	want := "[implement] first line\n[implement] second line\n"
	if got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("output = %q, want no ANSI escapes for a non-terminal writer", got)
	}
}
