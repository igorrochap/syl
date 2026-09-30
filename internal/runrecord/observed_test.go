package runrecord

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReaderObservesEveryRunStatus(t *testing.T) {
	localHost := "local-host"
	for _, test := range []struct {
		name       string
		state      *State
		legacyFile string
		alive      bool
		want       ObservedStatus
	}{
		{
			name:  "running",
			state: observedState(Running, localHost),
			alive: true,
			want:  ObservedRunning,
		},
		{
			name:  "interrupted",
			state: observedState(Running, localHost),
			alive: false,
			want:  ObservedInterrupted,
		},
		{
			name:  "running without a recorded hostname",
			state: observedState(Running, ""),
			alive: false,
			want:  ObservedInterrupted,
		},
		{name: "approved", state: observedState(Approved, localHost), want: ObservedApproved},
		{name: "exhausted", state: observedState(Exhausted, localHost), want: ObservedExhausted},
		{name: "failed", state: observedState(Failed, localHost), want: ObservedFailed},
		{name: "cancelled", state: observedState(Cancelled, localHost), want: ObservedCancelled},
		{name: "completed", legacyFile: summaryFile, want: ObservedCompleted},
		{name: "unknown", want: ObservedUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			runDir := t.TempDir()
			if test.state != nil {
				if err := Write(Path(runDir), *test.state); err != nil {
					t.Fatalf("Write() error = %v", err)
				}
			}
			if test.legacyFile != "" {
				if err := os.WriteFile(filepath.Join(runDir, test.legacyFile), nil, 0o644); err != nil {
					t.Fatalf("write legacy file: %v", err)
				}
			}

			livenessCalls := 0
			alive := func(int) bool {
				livenessCalls++
				return test.alive
			}
			reader := NewReaderWithProcessLiveness(nil, alive)
			got := reader.ObserveStatus(runDir, "", localHost)
			if got != test.want {
				t.Fatalf("ObserveStatus() = %q, want %q", got, test.want)
			}
			if test.state != nil && test.state.Status == Running && livenessCalls != 1 {
				t.Fatalf("liveness calls = %d, want 1", livenessCalls)
			}
			if test.state == nil && livenessCalls != 0 {
				t.Fatalf("liveness calls = %d, want 0", livenessCalls)
			}
		})
	}
}

func TestReaderObservesRemoteRunningRunWithoutCheckingProcess(t *testing.T) {
	runDir := t.TempDir()
	state := observedState(Running, "remote-host")
	if err := Write(Path(runDir), *state); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	livenessChecked := false
	reader := NewReaderWithProcessLiveness(nil, func(int) bool {
		livenessChecked = true
		return false
	})
	got := reader.ObserveStatus(runDir, "", "local-host")
	if got != ObservedRunning {
		t.Fatalf("ObserveStatus() = %q, want %q", got, ObservedRunning)
	}
	if livenessChecked {
		t.Fatal("ObserveStatus() checked a process on another host")
	}
}

func TestReaderTreatsUnreadableRunStateAsUnknown(t *testing.T) {
	runDir := t.TempDir()
	if err := os.WriteFile(Path(runDir), []byte("not json"), 0o644); err != nil {
		t.Fatalf("write unreadable state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, summaryFile), nil, 0o644); err != nil {
		t.Fatalf("write summary: %v", err)
	}

	got := NewReader(nil).ObserveStatus(runDir, "", "local-host")
	if got != ObservedUnknown {
		t.Fatalf("ObserveStatus() = %q, want %q", got, ObservedUnknown)
	}
}

func observedState(status Status, host string) *State {
	started := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	return &State{
		Status: status, PID: 123456, Hostname: host, StartedAt: started, UpdatedAt: started,
	}
}
