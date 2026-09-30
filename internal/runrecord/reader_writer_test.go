package runrecord

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/igorrochap/syl/internal/verdict"
)

func newRunDirectory(t *testing.T) (originRoot, runDir string) {
	t.Helper()
	originRoot = t.TempDir()
	runDir = RunDirectory(originRoot, "20260929T200000.000000000Z-204")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("create Run directory: %v", err)
	}
	return originRoot, runDir
}

func writeRunFile(t *testing.T, runDir, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(runDir, name), []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestReaderReadsIndividualFiles(t *testing.T) {
	_, runDir := newRunDirectory(t)
	reader := NewReader(nil)
	if _, err := reader.ReadMetadata(runDir); err == nil {
		t.Error("ReadMetadata() error = nil for a missing file")
	}
	if _, err := reader.ReadSessions(runDir); err == nil {
		t.Error("ReadSessions() error = nil for a missing file")
	}
	if _, err := reader.ReadSummary(runDir); err == nil {
		t.Error("ReadSummary() error = nil for a missing file")
	}
	if _, err := reader.ReadUsage(runDir); err == nil {
		t.Error("ReadUsage() error = nil for a missing file")
	}
	if _, err := reader.ReadState(runDir); err == nil {
		t.Error("ReadState() error = nil for a missing file")
	}
	if exists, err := reader.HasSummary(runDir); err != nil || exists {
		t.Errorf("HasSummary() = (%t, %v), want (false, nil)", exists, err)
	}

	writeRunFile(t, runDir, "metadata.txt", "Branch: b\n")
	writeRunFile(t, runDir, "sessions.txt", "iteration 1 implement: abc\n")
	writeRunFile(t, runDir, "summary.txt", "Iterations: 1\n")
	writeRunFile(t, runDir, "usage.json", "{}")
	if metadata, err := reader.ReadMetadata(runDir); err != nil || metadata.Branch != "b" {
		t.Errorf("ReadMetadata() = (%#v, %v)", metadata, err)
	}
	if sessions, err := reader.ReadSessions(runDir); err != nil || len(sessions) != 1 {
		t.Errorf("ReadSessions() = (%#v, %v)", sessions, err)
	}
	if summary, err := reader.ReadSummary(runDir); err != nil || summary.Iterations != 1 {
		t.Errorf("ReadSummary() = (%#v, %v)", summary, err)
	}
	if usage, err := reader.ReadUsage(runDir); err != nil || string(usage) != "{}" {
		t.Errorf("ReadUsage() = (%q, %v)", usage, err)
	}
}

func TestReaderReadsState(t *testing.T) {
	_, runDir := newRunDirectory(t)
	now := time.Date(2026, time.September, 29, 20, 0, 0, 0, time.UTC)
	want := State{Status: Approved, Iteration: 1, MaxIterations: 2, PID: 1, Hostname: "h", StartedAt: now, UpdatedAt: now, Kind: Implement, TicketRef: "#1"}
	if err := NewWriter(runDir).WriteState(want); err != nil {
		t.Fatalf("WriteState() error = %v", err)
	}
	got, err := NewReader(nil).ReadState(runDir)
	if err != nil || got != want {
		t.Fatalf("ReadState() = (%#v, %v), want %#v", got, err, want)
	}
}

func TestReadIgnoresUnparseableState(t *testing.T) {
	_, runDir := newRunDirectory(t)
	writeRunFile(t, runDir, "run-state.json", "not json")
	record, err := NewReader(nil).Read(runDir)
	if err != nil || record.HasState {
		t.Fatalf("Read() = (state %t, %v), want no state and no error", record.HasState, err)
	}
}

func TestReadFailsForMissingDirectory(t *testing.T) {
	if _, err := NewReader(nil).Read(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("Read() error = nil for a missing directory")
	}
}

func TestReadSkipsDirectoriesAndUnreadableVerdicts(t *testing.T) {
	_, runDir := newRunDirectory(t)
	if err := os.Mkdir(filepath.Join(runDir, "iteration-05-review.diff"), 0o755); err != nil {
		t.Fatalf("create directory: %v", err)
	}
	writeRunFile(t, runDir, "iteration-02-verdict.txt", "VERDICT: revise\nnot a valid verdict body\n")
	writeRunFile(t, runDir, "unknown.txt", "x")
	record, err := NewReader(nil).Read(runDir)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if len(record.Artifacts) != 1 || record.Artifacts[0].Name != "iteration-02-verdict.txt" {
		t.Fatalf("artifacts = %#v, want only the verdict file", record.Artifacts)
	}
	if record.HighestIteration != 2 {
		t.Fatalf("HighestIteration = %d, want 2 (directories do not count)", record.HighestIteration)
	}
	if record.VerdictText[2] != "revise" {
		t.Fatalf("VerdictText[2] = %q, want revise", record.VerdictText[2])
	}
	if _, parsed := record.Verdicts[2]; parsed {
		t.Fatalf("Verdicts[2] present for an unparseable verdict body")
	}
}

func TestReadSortsArtifactsByName(t *testing.T) {
	_, runDir := newRunDirectory(t)
	writeRunFile(t, runDir, "iteration-02-review.diff", "")
	writeRunFile(t, runDir, "iteration-01-review.diff", "")
	writeRunFile(t, runDir, "handoff-01.md", "")
	record, err := NewReader(nil).Read(runDir)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	var names []string
	for _, artifact := range record.Artifacts {
		names = append(names, artifact.Name)
	}
	if got := strings.Join(names, ","); got != "handoff-01.md,iteration-01-review.diff,iteration-02-review.diff" {
		t.Fatalf("artifact order = %s", got)
	}
}

func TestResolveArtifactRejectsUnsafeInput(t *testing.T) {
	originRoot, runDir := newRunDirectory(t)
	writeRunFile(t, runDir, "summary.txt", "x")
	if err := os.Mkdir(filepath.Join(runDir, "sub"), 0o755); err != nil {
		t.Fatalf("create directory: %v", err)
	}
	outside := filepath.Join(originRoot, "outside.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(runDir, "link.txt")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	tests := []struct{ name, runDir, artifact string }{
		{"blank run directory", " ", "summary.txt"},
		{"blank artifact", runDir, " "},
		{"absolute artifact", runDir, "/etc/passwd"},
		{"parent traversal", runDir, "../outside.txt"},
		{"missing artifact", runDir, "missing.txt"},
		{"symlink leaving the run", runDir, "link.txt"},
		{"directory artifact", runDir, "sub"},
		{"missing run directory", filepath.Join(originRoot, "nope"), "summary.txt"},
		{"not a run directory", originRoot, "outside.txt"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ResolveArtifact(test.runDir, test.artifact); err == nil {
				t.Fatal("ResolveArtifact() error = nil")
			}
		})
	}
	if _, err := ResolveArtifact(runDir, "summary.txt"); err != nil {
		t.Fatalf("ResolveArtifact() valid error = %v", err)
	}
}

func TestResolveRunDirectoryRejectsFiles(t *testing.T) {
	originRoot, runDir := newRunDirectory(t)
	if _, err := ResolveRunDirectory(runDir); err != nil {
		t.Fatalf("ResolveRunDirectory() error = %v", err)
	}
	filePath := filepath.Join(originRoot, ".syl", "runs", "a-file")
	if err := os.WriteFile(filePath, nil, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := ResolveRunDirectory(filePath); err == nil {
		t.Fatal("ResolveRunDirectory() error = nil for a regular file")
	}
}

func TestDirectoriesListsOnlyDirectories(t *testing.T) {
	originRoot, runDir := newRunDirectory(t)
	writeRunFile(t, filepath.Dir(runDir), "stray.txt", "x")
	got, err := Directories(originRoot)
	if err != nil || len(got) != 1 || got[0] != runDir {
		t.Fatalf("Directories() = (%#v, %v), want [%q]", got, err, runDir)
	}
	if _, err := Directories(t.TempDir()); err == nil {
		t.Fatal("Directories() error = nil without a runs directory")
	}
}

func TestHasSummaryReportsStatFailures(t *testing.T) {
	_, runDir := newRunDirectory(t)
	writeRunFile(t, runDir, "summary.txt", "x")
	if _, err := NewReader(nil).HasSummary(filepath.Join(runDir, "summary.txt")); err == nil {
		t.Fatal("HasSummary() error = nil when the run path is a file")
	}
}

func TestWriteMetadataFormatsEachRunKind(t *testing.T) {
	tests := []struct {
		name string
		spec Spec
		want string
	}{
		{
			"implement", Spec{Kind: Implement, Branch: "b", BranchPoint: "p", WorkRoot: "/w", ImplementerHarness: "codex", ReviewerHarness: "claude", ImplementContext: "a\nb", ReviewContext: "c"},
			"Branch: b\nBranch point: p\nWork root: /w\nImplementer harness: codex\nReviewer harness: claude\n" +
				"Implementer context:\n  a\n  b\nReviewer context:\n  c\n",
		},
		{
			"review", Spec{Kind: Review, TicketRef: " #7 ", BranchPoint: "p", WorkRoot: "/w", ReviewerHarness: "claude", ReviewContext: "c"},
			"Ticket: #7\nBranch point: p\nWork root: /w\nReviewer harness: claude\nReviewer context:\n  c\n",
		},
		{
			"blank context is omitted", Spec{Kind: Review, TicketRef: "#7", ReviewContext: "  \n "},
			"Ticket: #7\nBranch point: \nWork root: \nReviewer harness: \n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, runDir := newRunDirectory(t)
			if err := NewWriter(runDir).WriteMetadata(test.spec); err != nil {
				t.Fatalf("WriteMetadata() error = %v", err)
			}
			got, err := os.ReadFile(filepath.Join(runDir, "metadata.txt"))
			if err != nil || string(got) != test.want {
				t.Fatalf("metadata.txt = (%q, %v), want %q", got, err, test.want)
			}
		})
	}
	_, runDir := newRunDirectory(t)
	if err := NewWriter(runDir).WriteMetadata(Spec{Kind: Kind("other")}); err == nil {
		t.Fatal("WriteMetadata() error = nil for an unsupported kind")
	}
}

func TestWriterRejectsInvalidArtifactKind(t *testing.T) {
	_, runDir := newRunDirectory(t)
	writer := NewWriter(runDir)
	if err := writer.WriteArtifact(ArtifactKind(0), 1, nil); err == nil {
		t.Error("WriteArtifact() error = nil for an unknown kind")
	}
	if err := writer.WriteArtifact(ImplementFeed, 0, nil); err == nil {
		t.Error("WriteArtifact() error = nil for a per-iteration kind without an iteration")
	}
}

func TestWriterReportsFailuresInMissingDirectory(t *testing.T) {
	writer := NewWriter(filepath.Join(t.TempDir(), "missing"))
	if err := writer.WriteImplementTurn(1, "f", "t"); err == nil {
		t.Error("WriteImplementTurn() error = nil")
	}
	if _, err := writer.WriteReviewDiff(1, "d"); err == nil || !strings.Contains(err.Error(), "write pre-computed diff") {
		t.Errorf("WriteReviewDiff() error = %v, want diff context", err)
	}
	if err := writer.WriteReviewOutput(1, "f", "t"); err == nil {
		t.Error("WriteReviewOutput() error = nil")
	}
	if err := writer.WriteVerdict(1, verdict.Verdict{}); err == nil {
		t.Error("WriteVerdict() error = nil")
	}
	if err := writer.WriteSummary(SummaryInput{}); err == nil {
		t.Error("WriteSummary() error = nil")
	}
	if err := writer.WriteUsage(nil); err == nil {
		t.Error("WriteUsage() error = nil")
	}
	if err := writer.RecordSessions(1, "implement", []string{"a"}); err == nil {
		t.Error("RecordSessions() error = nil")
	}
	if err := writer.WriteState(State{}); err == nil {
		t.Error("WriteState() error = nil")
	}
}

func TestWriterCreationErrors(t *testing.T) {
	now := time.Date(2026, time.September, 29, 20, 0, 0, 0, time.UTC)
	if _, err := CreateWriter(t.TempDir(), Spec{Kind: Kind("other")}, now); err == nil {
		t.Error("CreateWriter() error = nil for an unsupported kind")
	}
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	if _, err := CreateNamedWriter(blocker, "run"); err == nil {
		t.Error("CreateNamedWriter() error = nil under a regular file")
	}
}

func TestRecordSessionsDeduplicatesAndSorts(t *testing.T) {
	_, runDir := newRunDirectory(t)
	writer := NewWriter(runDir)
	if err := writer.RecordSessions(2, "review", []string{"b", " ", "b"}); err != nil {
		t.Fatalf("RecordSessions() error = %v", err)
	}
	if err := writer.RecordSessions(1, "implement", []string{"a"}); err != nil {
		t.Fatalf("RecordSessions() error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(runDir, "sessions.txt"))
	if want := "iteration 1 implement: a\niteration 2 review: b\n"; err != nil || string(got) != want {
		t.Fatalf("sessions.txt = (%q, %v), want %q", got, err, want)
	}
	if want := filepath.Join(runDir, "handoff-03.md"); writer.HandoffPath(3) != want {
		t.Fatalf("HandoffPath(3) = %q, want %q", writer.HandoffPath(3), want)
	}
}
