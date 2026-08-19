package orchestration

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

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
