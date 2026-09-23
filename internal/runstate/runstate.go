// Package runstate defines the durable state recorded by each syl Run.
package runstate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const fileName = "run-state.json"

// Status is the lifecycle outcome recorded by a Run.
type Status string

const (
	// Running means the Run has started and has not recorded a final outcome.
	Running Status = "running"
	// Approved means the final review verdict approved the Run.
	Approved Status = "approved"
	// Exhausted means the Run reached its iteration limit with a revise verdict.
	Exhausted Status = "exhausted"
	// Failed means the Run returned an error.
	Failed Status = "failed"
	// Cancelled means the Run's context was cancelled.
	Cancelled Status = "cancelled"
)

// Activity is the work currently being performed by a running Run.
type Activity string

const (
	// Preparing means the Run is creating its initial artifacts.
	Preparing Activity = "preparing"
	// Implementing means the implementer Role is active.
	Implementing Activity = "implementing"
	// Reviewing means the reviewer Role is active.
	Reviewing Activity = "reviewing"
	// AwaitingAnswer means the Run is paused for a user answer.
	AwaitingAnswer Activity = "awaiting-answer"
)

// Kind identifies the command that created a Run.
type Kind string

const (
	// Implement identifies an implement-and-review Run.
	Implement Kind = "implement"
	// Review identifies a standalone review Run.
	Review Kind = "review"
)

// State is the on-disk representation of one Run's current or final state.
type State struct {
	Status        Status     `json:"status"`
	Activity      Activity   `json:"activity,omitempty"`
	Iteration     int        `json:"iteration"`
	MaxIterations int        `json:"max_iterations"`
	Question      string     `json:"question,omitempty"`
	PID           int        `json:"pid"`
	Hostname      string     `json:"hostname"`
	StartedAt     time.Time  `json:"started_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	EndedAt       *time.Time `json:"ended_at,omitempty"`
	Kind          Kind       `json:"kind"`
	TicketRef     string     `json:"ticket_ref"`
}

// Path returns the state file path inside a Run directory.
func Path(runDir string) string {
	return filepath.Join(runDir, fileName)
}

// New returns the initial state for a newly created Run.
func New(kind Kind, ticketRef string, iteration, maxIterations int, now time.Time) State {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = ""
	}
	return State{
		Status:        Running,
		Activity:      Preparing,
		Iteration:     iteration,
		MaxIterations: maxIterations,
		PID:           os.Getpid(),
		Hostname:      hostname,
		StartedAt:     now.UTC(),
		UpdatedAt:     now.UTC(),
		Kind:          kind,
		TicketRef:     ticketRef,
	}
}

// Write atomically replaces the state file with state.
func Write(path string, state State) error {
	if err := validate(state); err != nil {
		return err
	}
	contents, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode run state: %w", err)
	}
	contents = append(contents, '\n')

	temporary, err := os.CreateTemp(filepath.Dir(path), ".run-state.json-*")
	if err != nil {
		return fmt.Errorf("create temporary run state: %w", err)
	}
	temporaryPath := temporary.Name()
	keepTemporary := false
	defer func() {
		if !keepTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set temporary run state permissions: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary run state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary run state: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace run state %s: %w", path, err)
	}
	keepTemporary = true
	return nil
}

// Read parses one Run state file.
func Read(path string) (State, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(contents, &state); err != nil {
		return State{}, fmt.Errorf("decode run state %s: %w", path, err)
	}
	if err := validate(state); err != nil {
		return State{}, fmt.Errorf("decode run state %s: %w", path, err)
	}
	return state, nil
}

func validate(state State) error {
	switch state.Status {
	case Running, Approved, Exhausted, Failed, Cancelled:
	default:
		return fmt.Errorf("invalid run status %q", state.Status)
	}
	return nil
}
