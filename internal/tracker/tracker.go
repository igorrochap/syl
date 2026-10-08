// Package tracker defines the tracker boundary shared by local and remote
// ticket backends.
package tracker

import (
	"context"
	"errors"
)

// ErrNoRemote identifies remote tracker failures caused by a missing Git
// repository or origin. Local tracker failures never use this error.
var ErrNoRemote = errors.New("no remote configured")

type noRemoteError string

func (e noRemoteError) Error() string { return string(e) }

func (e noRemoteError) Is(target error) bool { return target == ErrNoRemote }

func newNoRemoteError(message string) error { return noRemoteError(message) }

// Ticket is the tracker-neutral representation used by workflow commands.
type Ticket struct {
	Number int
	Title  string
	Body   string
	Status string
	State  string
	Labels []string
}

// Tracker provides the ticket operations used across the workflow.
type Tracker interface {
	Resolve(ctx context.Context, reference string) (Ticket, error)
	List(ctx context.Context) ([]Ticket, error)
	UpdateStatus(ctx context.Context, number int, status string) error
	AddComment(ctx context.Context, number int, note string) error
	Create(ctx context.Context, title, body string) (Ticket, error)
}
