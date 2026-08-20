package version

import "testing"

func TestStringUsesFallbacksForMissingMetadata(t *testing.T) {
	originalVersion, originalCommit := Version, Commit
	Version = ""
	Commit = ""
	t.Cleanup(func() {
		Version = originalVersion
		Commit = originalCommit
	})

	if got, want := String(), "dev (commit unknown)"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
