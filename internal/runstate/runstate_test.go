package runstate

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWriteAndReadRoundTrip(t *testing.T) {
	runDir := t.TempDir()
	started := time.Date(2026, time.September, 23, 19, 0, 0, 0, time.UTC)
	ended := started.Add(time.Minute)
	want := State{
		Status:        Approved,
		Activity:      Reviewing,
		Iteration:     1,
		MaxIterations: 1,
		PID:           42,
		Hostname:      "builder",
		StartedAt:     started,
		UpdatedAt:     ended,
		EndedAt:       &ended,
		Kind:          Review,
		TicketRef:     "#180",
	}
	path := Path(runDir)

	if err := Write(path, want); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Read() = %#v, want %#v", got, want)
	}
}

func TestWriteReplacesStateAtomically(t *testing.T) {
	runDir := t.TempDir()
	path := Path(runDir)
	if err := Write(path, State{Status: Running, StartedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("initial Write() error = %v", err)
	}
	if err := Write(path, State{Status: Failed, StartedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("replacement Write() error = %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(contents), `"status": "failed"`) {
		t.Fatalf("state contents = %q, want failed state", contents)
	}
	matches, err := filepath.Glob(filepath.Join(runDir, ".run-state.json-*"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary state files = %v, want none", matches)
	}
}

func TestStatusDoesNotContainInterrupted(t *testing.T) {
	statuses := []Status{Running, Approved, Exhausted, Failed, Cancelled}
	for _, status := range statuses {
		if status == Status("interrupted") {
			t.Fatal("status type contains interrupted")
		}
	}
	if err := Write(Path(t.TempDir()), State{Status: Status("interrupted")}); err == nil {
		t.Fatal("Write() accepted interrupted status")
	}
}
