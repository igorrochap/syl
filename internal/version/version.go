// Package version provides the build metadata reported by syl.
package version

import "fmt"

const (
	development   = "dev"
	unknownCommit = "unknown"
)

var (
	// Version is the semantic version embedded in the binary at build time.
	Version = development
	// Commit is the short commit hash embedded in the binary at build time.
	Commit = unknownCommit
)

// String formats the build metadata for display to a user.
func String() string {
	return fmt.Sprintf(
		"%s (commit %s)",
		valueOrDefault(Version, development),
		valueOrDefault(Commit, unknownCommit),
	)
}

func valueOrDefault(value, defaultValue string) string {
	if value == "" {
		return defaultValue
	}
	return value
}
