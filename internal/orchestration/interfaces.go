package orchestration

import "context"

// GitRunner is the boundary for repository inspection and mutation.
type GitRunner interface {
	Run(ctx context.Context, args ...string) (string, error)
}

// CommitgenRunner invokes the optional commitgen command in a Work root.
type CommitgenRunner interface {
	Available() bool
	Run(ctx context.Context, workRoot string, args ...string) (string, error)
}

// Notifier is the boundary for optional user notifications.
type Notifier interface {
	Notify(ctx context.Context, message string) error
}
