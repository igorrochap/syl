package runrecord

import (
	"errors"
	"os"
	"syscall"
)

// ObservedStatus is the lifecycle outcome shown for a Run when it is read.
type ObservedStatus string

const (
	// ObservedRunning means the recorded running process is alive or remote.
	ObservedRunning ObservedStatus = "running"
	// ObservedInterrupted means a local Run is recorded running but its process is gone.
	ObservedInterrupted ObservedStatus = "interrupted"
	// ObservedApproved means the Run ended with an approving verdict.
	ObservedApproved ObservedStatus = "approved"
	// ObservedExhausted means the Run ended after reaching its iteration limit.
	ObservedExhausted ObservedStatus = "exhausted"
	// ObservedFailed means the Run ended with an error.
	ObservedFailed ObservedStatus = "failed"
	// ObservedCancelled means the Run ended because its context was cancelled.
	ObservedCancelled ObservedStatus = "cancelled"
	// ObservedCompleted means a legacy Run has a summary but no readable state file.
	ObservedCompleted ObservedStatus = "completed"
	// ObservedUnknown means a legacy Run has no summary or its state cannot be read.
	ObservedUnknown ObservedStatus = "unknown"
)

// ObserveStatus derives the status shown for a Run. markerHost takes precedence
// over the host recorded in run-state.json when identifying local Runs.
func (reader *Reader) ObserveStatus(
	runDir string,
	markerHost string,
	localHost string,
) ObservedStatus {
	state, err := reader.ReadState(runDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			hasSummary, summaryErr := reader.HasSummary(runDir)
			if summaryErr == nil && hasSummary {
				return ObservedCompleted
			}
		}
		return ObservedUnknown
	}
	if state.Status != Running {
		return ObservedStatus(state.Status)
	}

	host := markerHost
	if host == "" {
		host = state.Hostname
	}
	// Older state files have no hostname; preserve their local-run behavior.
	isLocalRun := host == "" || (localHost != "" && host == localHost)
	if !isLocalRun {
		return ObservedRunning
	}
	if reader.processAlive(state.PID) {
		return ObservedRunning
	}
	return ObservedInterrupted
}

func checkProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EPERM)
}
