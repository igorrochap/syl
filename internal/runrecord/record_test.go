package runrecord

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/igorrochap/syl/internal/verdict"
)

func TestWriterRoundTripsRunRecord(t *testing.T) {
	originRoot := t.TempDir()
	now := time.Date(2026, time.September, 29, 20, 0, 0, 0, time.UTC)
	spec := Spec{
		Kind: Implement, TicketRef: "#204", MaxIterations: 2,
		Branch: "refactor/run-record-module", BranchPoint: "abc123", WorkRoot: originRoot,
		ImplementerHarness: "codex", ReviewerHarness: "claude",
		ImplementContext: "Preserve the format.\nKeep context blocks.",
		ReviewContext:    "Check the reader and writer.",
	}
	writer, err := CreateWriter(originRoot, spec, now)
	if err != nil {
		t.Fatalf("CreateWriter() error = %v", err)
	}
	if !IsRunDirectory(writer.Directory()) {
		t.Fatalf("created directory %q is not a Run directory", writer.Directory())
	}
	if err := writer.WriteMetadata(spec); err != nil {
		t.Fatalf("WriteMetadata() error = %v", err)
	}
	state := State{
		Status: Approved, Iteration: 2, MaxIterations: 2, PID: 123, Hostname: "host",
		StartedAt: now, UpdatedAt: now, Kind: Implement, TicketRef: "#204",
	}
	if err := writer.WriteState(state); err != nil {
		t.Fatalf("WriteState() error = %v", err)
	}
	if err := writer.WriteImplementTurn(1, "implement feed", "implement transcript"); err != nil {
		t.Fatalf("WriteImplementTurn() error = %v", err)
	}
	if _, err := writer.WriteReviewDiff(1, "diff --git a/a b/a\n"); err != nil {
		t.Fatalf("WriteReviewDiff() error = %v", err)
	}
	if err := writer.WriteReviewOutput(1, "review feed", "review transcript"); err != nil {
		t.Fatalf("WriteReviewOutput() error = %v", err)
	}
	reviewVerdict := verdict.Verdict{
		Status: verdict.Revise, Summary: "One change remains",
		Findings: []verdict.Finding{{Kind: verdict.Blocking, Location: "main.go:1", Issue: "handle errors"}},
	}
	if err := writer.WriteVerdict(1, reviewVerdict); err != nil {
		t.Fatalf("WriteVerdict() error = %v", err)
	}
	if err := writer.RecordSessions(1, "implement", []string{"implement-session"}); err != nil {
		t.Fatalf("RecordSessions() implement error = %v", err)
	}
	if err := writer.RecordSessions(1, "review", []string{"review-session"}); err != nil {
		t.Fatalf("RecordSessions() review error = %v", err)
	}
	if err := writer.WriteSummary(SummaryInput{
		Iterations: 1, Final: verdict.Verdict{Status: verdict.Approve, Summary: "Ready"}, DiffStat: " main.go | 2 ++",
	}); err != nil {
		t.Fatalf("WriteSummary() error = %v", err)
	}
	if err := writer.WriteUsage([]byte("{\"schema_version\":1}\n")); err != nil {
		t.Fatalf("WriteUsage() error = %v", err)
	}
	if err := writer.WriteArtifact(ImplementHandoff, 1, []byte("handoff notes")); err != nil {
		t.Fatalf("WriteArtifact() handoff error = %v", err)
	}

	record, err := NewReader(nil).Read(writer.Directory())
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	wantMetadata := Metadata{
		Branch: spec.Branch, BranchPoint: spec.BranchPoint, WorkRoot: spec.WorkRoot,
		ImplementerHarness: spec.ImplementerHarness, ReviewerHarness: spec.ReviewerHarness,
		ImplementContext: spec.ImplementContext, ReviewContext: spec.ReviewContext,
		Kind: Implement,
	}
	if !reflect.DeepEqual(record.Metadata, wantMetadata) {
		t.Fatalf("metadata = %#v, want %#v", record.Metadata, wantMetadata)
	}
	wantSessions := []Session{
		{Iteration: 1, Role: "implement", SessionID: "implement-session"},
		{Iteration: 1, Role: "review", SessionID: "review-session"},
	}
	if !reflect.DeepEqual(record.Sessions, wantSessions) {
		t.Fatalf("sessions = %#v, want %#v", record.Sessions, wantSessions)
	}
	if !record.SummaryExists || record.Summary.Iterations != 1 || record.Summary.FinalVerdict != "approve" ||
		record.Summary.Text != "Ready" || record.Summary.DiffStat != "main.go | 2 ++" {
		t.Fatalf("summary = %#v, want the written summary values", record.Summary)
	}
	if !record.HasState || !reflect.DeepEqual(record.State, state) {
		t.Fatalf("state = %#v (present %t), want %#v", record.State, record.HasState, state)
	}
	if got, ok := record.Verdicts[1]; !ok || !reflect.DeepEqual(got, reviewVerdict) {
		t.Fatalf("verdict = %#v (present %t), want %#v", got, ok, reviewVerdict)
	}
	if !record.UsageExists || string(record.UsageContents) != "{\"schema_version\":1}\n" {
		t.Fatalf("usage contents = %q (present %t)", record.UsageContents, record.UsageExists)
	}
	if !hasArtifact(record.Artifacts, ImplementHandoff, "handoff-01.md") ||
		!hasArtifact(record.Artifacts, ReviewDiff, "iteration-01-review.diff") {
		t.Fatalf("artifacts do not include the handoff and review diff: %#v", record.Artifacts)
	}
}

func TestReadLegacyRunFixtureWithoutRunState(t *testing.T) {
	originRoot := t.TempDir()
	fixture := filepath.Join("testdata", "legacy-run")
	fixtureEntries, err := os.ReadDir(fixture)
	if err != nil {
		t.Fatalf("read legacy fixture: %v", err)
	}
	const name = "20260920T152244.289465000Z-172"
	runDir := RunDirectory(originRoot, name)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("create Run directory: %v", err)
	}
	for _, entry := range fixtureEntries {
		contents, err := os.ReadFile(filepath.Join(fixture, entry.Name()))
		if err != nil {
			t.Fatalf("read fixture file %s: %v", entry.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(runDir, entry.Name()), contents, 0o644); err != nil {
			t.Fatalf("copy fixture file %s: %v", entry.Name(), err)
		}
	}
	record, err := NewReader(nil).Read(runDir)
	if err != nil {
		t.Fatalf("Read() legacy fixture error = %v", err)
	}
	if !IsRunDirectory(runDir) || record.HasState {
		t.Fatalf("legacy directory validation/state = (%t, %t), want valid and no state", IsRunDirectory(runDir), record.HasState)
	}
	if record.Metadata.Kind != Implement || record.Metadata.Branch != "feat/implementer-session-resume" ||
		record.Metadata.BranchPoint != "b2662efdc2856d6eb55af72123269d2fe083546a" ||
		record.Metadata.ImplementerHarness != "codex" || record.Metadata.ReviewerHarness != "claude" {
		t.Fatalf("legacy metadata = %#v", record.Metadata)
	}
	if len(record.Sessions) != 2 || record.Sessions[0].SessionID != "01a0bf69-9bda-70e3-bf62-45d7ef9ada55" ||
		record.Sessions[1].SessionID != "a5c17c9b-9cfb-4570-bfb0-0b16aea3a8e5" {
		t.Fatalf("legacy sessions = %#v", record.Sessions)
	}
	if !record.SummaryExists || record.Summary.Iterations != 1 || record.Summary.FinalVerdict != "approve" ||
		record.Summary.Text == "" || !hasArtifact(record.Artifacts, ReviewDiff, "iteration-01-review.diff") {
		t.Fatalf("legacy summary/artifacts = (%#v, %#v)", record.Summary, record.Artifacts)
	}
	if record.Verdicts[1].Status != verdict.Approve || record.HighestIteration != 1 {
		t.Fatalf("legacy verdict/iteration = (%#v, %d)", record.Verdicts[1], record.HighestIteration)
	}
}

func TestReaderFilesystemMethodsUseInjectedFilesystem(t *testing.T) {
	originRoot := t.TempDir()
	runDir := RunDirectory(originRoot, "20260929T200000.000000000Z-204")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("create Run directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "summary.txt"), []byte("summary"), 0o644); err != nil {
		t.Fatalf("write summary: %v", err)
	}
	files := &trackingFileSystem{}
	reader := NewReader(files)

	exists, err := reader.HasSummary(runDir)
	if err != nil || !exists {
		t.Fatalf("HasSummary() = (%t, %v), want (true, nil)", exists, err)
	}
	directories, err := reader.Directories(originRoot)
	if err != nil || len(directories) != 1 || directories[0] != runDir {
		t.Fatalf("Directories() = (%#v, %v), want [%q], nil", directories, err, runDir)
	}
	resolved, err := reader.ResolveArtifact(runDir, "summary.txt")
	wantResolved, resolveErr := filepath.EvalSymlinks(filepath.Join(runDir, "summary.txt"))
	if resolveErr != nil {
		t.Fatalf("resolve expected summary path: %v", resolveErr)
	}
	if err != nil || resolved != wantResolved {
		t.Fatalf("ResolveArtifact() = (%q, %v), want summary path, nil", resolved, err)
	}
	if files.statCalls != 3 || files.evalSymlinkCalls != 2 || files.readDirCalls != 1 {
		t.Fatalf("filesystem calls = (stat %d, eval symlinks %d, read dir %d), want (3, 2, 1)",
			files.statCalls, files.evalSymlinkCalls, files.readDirCalls)
	}
}

func hasArtifact(artifacts []Artifact, kind ArtifactKind, name string) bool {
	for _, artifact := range artifacts {
		if artifact.Kind == kind && artifact.Name == name {
			return true
		}
	}
	return false
}

type trackingFileSystem struct {
	osFileSystem
	statCalls        int
	evalSymlinkCalls int
	readDirCalls     int
}

func (files *trackingFileSystem) Stat(name string) (os.FileInfo, error) {
	files.statCalls++
	return files.osFileSystem.Stat(name)
}

func (files *trackingFileSystem) EvalSymlinks(path string) (string, error) {
	files.evalSymlinkCalls++
	return files.osFileSystem.EvalSymlinks(path)
}

func (files *trackingFileSystem) ReadDir(name string) ([]os.DirEntry, error) {
	files.readDirCalls++
	return files.osFileSystem.ReadDir(name)
}
