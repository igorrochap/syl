package orchestration

import (
	"bytes"
	"testing"
)

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
}
