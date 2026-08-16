// Package tracker defines the tracker boundary shared by local and remote
// ticket backends.
package tracker

import "context"

// Ticket is the tracker-neutral representation used by workflow commands.
type Ticket struct {
	Number int
	Title  string
	Body   string
	Status string
}

// Tracker provides the ticket operations used across the workflow.
type Tracker interface {
	Resolve(ctx context.Context, reference string) (Ticket, error)
	List(ctx context.Context) ([]Ticket, error)
	UpdateStatus(ctx context.Context, number int, status string) error
	AddComment(ctx context.Context, number int, note string) error
	Create(ctx context.Context, title, body string) (Ticket, error)
}
