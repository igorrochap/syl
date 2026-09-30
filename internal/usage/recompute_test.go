package usage

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/igorrochap/syl/internal/runrecord"
)

func TestRecomputeArtifactBuildsEntriesFromLegacyRunArtifacts(t *testing.T) {
	runDir := filepath.Join(t.TempDir(), ".syl", "runs", "20260929T200000.000000000Z-204")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("create Run directory: %v", err)
	}
	writer := runrecord.NewWriter(runDir)
	if err := writer.WriteMetadata(runrecord.Spec{
		Kind: runrecord.Review, TicketRef: "#204", ReviewerHarness: "codex",
	}); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	for _, session := range []struct {
		iteration int
		id        string
	}{{1, "shared-session"}, {2, "shared-session"}, {3, "other-session"}} {
		if err := writer.RecordSessions(session.iteration, "implement", []string{session.id}); err != nil {
			t.Fatalf("write session %d: %v", session.iteration, err)
		}
	}
	for _, artifact := range []struct {
		kind      runrecord.ArtifactKind
		iteration int
	}{{runrecord.ImplementFeed, 1}, {runrecord.ImplementTranscript, 1}, {runrecord.ReviewFeed, 4}, {runrecord.ReviewFeed, 0}} {
		if err := writer.WriteArtifact(artifact.kind, artifact.iteration, nil); err != nil {
			t.Fatalf("write artifact %d/%d: %v", artifact.kind, artifact.iteration, err)
		}
	}

	got, err := RecomputeArtifact(runDir, t.TempDir(), t.TempDir(), map[string]RoleMetadata{
		"implement": {Harness: "codex"},
	})
	if err != nil {
		t.Fatalf("RecomputeArtifact() error = %v", err)
	}
	if len(got.Entries) != 4 {
		t.Fatalf("entries = %#v, want four role clusters", got.Entries)
	}
	want := []struct {
		iteration int
		role      string
	}{
		{0, "review"},
		{1, "implement, iterations 1–2 combined"},
		{3, "implement"},
		{4, "review"},
	}
	for index, entry := range got.Entries {
		if entry.Iteration != want[index].iteration || entry.Role != want[index].role || entry.Tracked || entry.Metrics != nil {
			t.Errorf("entry[%d] = %#v, want unavailable %d/%q", index, entry, want[index].iteration, want[index].role)
		}
	}
}

func TestRecomputeArtifactRejectsNonDirectoryPaths(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := RecomputeArtifact(missing, t.TempDir(), t.TempDir(), nil); !os.IsNotExist(err) {
		t.Fatalf("RecomputeArtifact(missing) error = %v, want not-exist", err)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := RecomputeArtifact(file, t.TempDir(), t.TempDir(), nil); err == nil {
		t.Fatal("RecomputeArtifact(file) error = nil, want not-directory error")
	}
}

func TestMergeArtifactInvocationsAddsMissingArtifactRuns(t *testing.T) {
	invocations := []sessionInvocation{{iteration: 1, role: "implement", sessions: []string{"session"}}}
	record := runrecord.Record{
		Metadata: runrecord.Metadata{Kind: runrecord.Review},
		Artifacts: []runrecord.Artifact{
			{Kind: runrecord.ImplementFeed, Iteration: 1, Role: "implement"},
			{Kind: runrecord.ImplementTranscript, Iteration: 1, Role: "implement"},
			{Kind: runrecord.ReviewFeed, Iteration: 3, Role: "review"},
			{Kind: runrecord.ReviewFeed, Standalone: true, Role: "review"},
			{Kind: runrecord.ReviewTranscript, Standalone: true, Role: "review"},
			{Kind: runrecord.VerdictFile, Iteration: 3},
		},
	}

	got := mergeArtifactInvocations(record, invocations)
	want := []sessionInvocation{
		{iteration: 0, role: "review"},
		{iteration: 1, role: "implement", sessions: []string{"session"}},
		{iteration: 3, role: "review"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeArtifactInvocations() = %#v, want %#v", got, want)
	}
}

func TestClusterInvocationsMergesTransitiveSessionsByRole(t *testing.T) {
	invocations := []sessionInvocation{
		{iteration: 1, role: "implement", sessions: []string{"first"}},
		{iteration: 2, role: "implement", sessions: []string{"second"}},
		{iteration: 3, role: "implement", sessions: []string{"first", "second"}},
		{iteration: 1, role: "review", sessions: []string{"first"}},
	}
	clusters := clusterInvocations(invocations)
	if len(clusters) != 2 || len(clusters[0].invocations) != 3 || clusters[0].role != "implement" || clusters[1].role != "review" {
		t.Fatalf("clusters = %#v, want one three-invocation implement cluster and a separate review cluster", clusters)
	}
	if empty := clusterInvocations(nil); len(empty) != 0 {
		t.Fatalf("empty invocation clusters = %#v, want empty", empty)
	}
}

func TestRoleArtifactModTimePrefersRoleFilesAndFallsBackToFeeds(t *testing.T) {
	first := time.Date(2026, time.September, 29, 20, 0, 0, 0, time.UTC)
	second := first.Add(time.Minute)
	artifacts := []runrecord.Artifact{
		{Kind: runrecord.ImplementFeed, Iteration: 2, Role: "implement", ModTime: first},
		{Kind: runrecord.ImplementTranscript, Iteration: 2, Role: "implement", ModTime: second},
		{Kind: runrecord.ReviewFeed, Iteration: 2, Role: "review", ModTime: second.Add(time.Minute)},
	}
	if got, ok := roleArtifactModTime(runrecord.Record{Artifacts: artifacts}, 2, "implement"); !ok || !got.Equal(second) {
		t.Fatalf("role artifact time = (%v, %t), want (%v, true)", got, ok, second)
	}
	fallback := []runrecord.Artifact{
		{Kind: runrecord.ImplementFeed, Iteration: 3, ModTime: first},
		{Kind: runrecord.ReviewFeed, Iteration: 3, ModTime: second},
	}
	if got, ok := roleArtifactModTime(runrecord.Record{Artifacts: fallback}, 3, "implement"); !ok || !got.Equal(second) {
		t.Fatalf("fallback feed time = (%v, %t), want (%v, true)", got, ok, second)
	}
	standalone := []runrecord.Artifact{{Kind: runrecord.ReviewTranscript, Standalone: true, Role: "review", ModTime: first}}
	if got, ok := roleArtifactModTime(runrecord.Record{Artifacts: standalone}, 0, "review"); !ok || !got.Equal(first) {
		t.Fatalf("standalone role artifact time = (%v, %t), want (%v, true)", got, ok, first)
	}
	if got, ok := roleArtifactModTime(runrecord.Record{Artifacts: fallback}, 0, "review"); ok || !got.IsZero() {
		t.Fatalf("iteration zero fallback time = (%v, %t), want zero and false", got, ok)
	}
}

func TestRecomputeFormattingAndMetadataHelpers(t *testing.T) {
	if got := roleMetadata(nil, "implement"); got != (RoleMetadata{Harness: "unknown", Model: "unknown"}) {
		t.Fatalf("roleMetadata() = %#v, want unknown defaults", got)
	}
	if got := clusterSessionIDs(usageCluster{invocations: []sessionInvocation{
		{sessions: []string{"first", "shared"}}, {sessions: []string{"shared", "second"}},
	}}); !reflect.DeepEqual(got, []string{"first", "shared", "second"}) {
		t.Fatalf("clusterSessionIDs() = %#v, want deduplicated session IDs", got)
	}
	if got := combinedRole(usageCluster{role: "implement", invocations: []sessionInvocation{
		{iteration: 1}, {iteration: 2},
	}}); got != "implement, iterations 1–2 combined" {
		t.Fatalf("combinedRole() = %q, want contiguous iteration span", got)
	}
	if got := formatIterationSpan(nil); got != "unknown" {
		t.Fatalf("formatIterationSpan(empty) = %q, want unknown", got)
	}
	if got := formatIterationSpan([]int{1, 3}); got != "1, 3" {
		t.Fatalf("formatIterationSpan(gapped) = %q, want explicit values", got)
	}
}

func TestUsageArtifactRoundTripsAndUsesMarshalDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	artifact := NewArtifact()
	artifact.Upsert(Entry{Iteration: 2, Role: "review", Harness: "codex", Tracked: true})
	artifact.Upsert(Entry{Iteration: 1, Role: "implement", Harness: "claude", Tracked: true})
	artifact.Upsert(Entry{Iteration: 2, Role: "review", Harness: "claude", Tracked: true})
	if err := WriteArtifact(path, artifact); err != nil {
		t.Fatalf("WriteArtifact() error = %v", err)
	}
	got, err := ReadArtifact(path)
	if err != nil {
		t.Fatalf("ReadArtifact() error = %v", err)
	}
	if len(got.Entries) != 2 || got.Entries[0].Iteration != 1 || got.Entries[1].Harness != "claude" {
		t.Fatalf("round-trip artifact = %#v, want sorted entries and latest upsert", got)
	}
	if _, err := MarshalArtifact(Artifact{}); err != nil {
		t.Fatalf("MarshalArtifact(zero value) error = %v", err)
	}
	if _, err := ParseArtifact(path, []byte(`{"entries":[]}`)); err == nil {
		t.Fatal("ParseArtifact(missing schema) error = nil, want validation error")
	}
}
