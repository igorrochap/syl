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

func TestLineTrackingWritersTrackLineBoundaries(t *testing.T) {
	var output bytes.Buffer
	tracked := newLineTrackingWriter(&output)
	if !tracked.AtLineStart() {
		t.Fatal("newLineTrackingWriter() starts mid-line, want line start")
	}
	if ensureLineTrackingWriter(tracked) != tracked {
		t.Fatal("ensureLineTrackingWriter() wrapped an already line-aware writer")
	}
	if _, err := tracked.Write([]byte("partial")); err != nil {
		t.Fatalf("lineTrackingWriter.Write() error = %v", err)
	}
	if tracked.AtLineStart() {
		t.Fatal("lineTrackingWriter.AtLineStart() = true after partial output")
	}
	if _, err := tracked.Write([]byte("\n")); err != nil {
		t.Fatalf("lineTrackingWriter.Write() error = %v", err)
	}
	if !tracked.AtLineStart() {
		t.Fatal("lineTrackingWriter.AtLineStart() = false after newline")
	}

	progress := newReviewProgressWriter(&output)
	if !progress.AtLineStart() {
		t.Fatal("newReviewProgressWriter() starts mid-line, want line start")
	}
	if _, err := progress.Write([]byte("visible")); err != nil {
		t.Fatalf("reviewProgressWriter.Write() error = %v", err)
	}
	if progress.AtLineStart() {
		t.Fatal("reviewProgressWriter.AtLineStart() = true after visible output")
	}
	if _, err := progress.Write([]byte("VERDICT")); err != nil {
		t.Fatalf("reviewProgressWriter.Write() error = %v", err)
	}
	if err := progress.EndTurn(); err != nil {
		t.Fatalf("reviewProgressWriter.EndTurn() error = %v", err)
	}
	if progress.AtLineStart() {
		t.Fatal("reviewProgressWriter.AtLineStart() = true after unterminated output")
	}

	labeled := newRoleLabelWriter(&output, "review", ansiColorReview)
	delegatingProgress := newReviewProgressWriter(labeled)
	if !delegatingProgress.AtLineStart() {
		t.Fatal("reviewProgressWriter.AtLineStart() = false, want delegated line start")
	}
	if _, err := delegatingProgress.Write([]byte("visible")); err != nil {
		t.Fatalf("delegating reviewProgressWriter.Write() error = %v", err)
	}
	if delegatingProgress.AtLineStart() {
		t.Fatal("reviewProgressWriter.AtLineStart() = true after delegated visible output")
	}
}
