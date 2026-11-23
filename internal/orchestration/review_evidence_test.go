package orchestration

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectReviewEvidencePromptInstruction(t *testing.T) {
	root := t.TempDir()
	regularFile := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(regularFile, []byte("file"), 0o644); err != nil {
		t.Fatalf("create non-directory Evidence path: %v", err)
	}
	evidenceDirectory := filepath.Join(root, "evidence-1")
	if err := os.Mkdir(evidenceDirectory, 0o755); err != nil {
		t.Fatalf("create Evidence directory: %v", err)
	}
	if err := os.Mkdir(filepath.Join(evidenceDirectory, "evidence.md"), 0o755); err != nil {
		t.Fatalf("create non-regular evidence.md: %v", err)
	}
	missingEvidenceDirectory := filepath.Join(root, "missing-evidence-file")
	if err := os.Mkdir(missingEvidenceDirectory, 0o755); err != nil {
		t.Fatalf("create Evidence directory without evidence.md: %v", err)
	}
	completeDirectory := filepath.Join(root, "evidence-2")
	if err := os.Mkdir(completeDirectory, 0o755); err != nil {
		t.Fatalf("create complete Evidence directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(completeDirectory, "evidence.md"), []byte("Outcome: pass\n"), 0o644); err != nil {
		t.Fatalf("write evidence.md: %v", err)
	}
	checkFailure := errors.New("permission denied")
	completeDirectoryInfo, err := os.Stat(completeDirectory)
	if err != nil {
		t.Fatalf("stat complete Evidence directory: %v", err)
	}

	tests := []struct {
		name      string
		inspect   func() reviewEvidence
		wantParts []string
	}{
		{
			name:      "no iteration Evidence",
			inspect:   func() reviewEvidence { return inspectReviewEvidence(" \t ") },
			wantParts: []string{"standalone review", "no iteration Evidence directory"},
		},
		{
			name:      "missing directory",
			inspect:   func() reviewEvidence { return inspectReviewEvidence(filepath.Join(root, "missing")) },
			wantParts: []string{filepath.Join(root, "missing"), "directory does not exist", "evidence.md is missing"},
		},
		{
			name:      "path is not a directory",
			inspect:   func() reviewEvidence { return inspectReviewEvidence(regularFile) },
			wantParts: []string{regularFile, "not a directory", "evidence.md is missing"},
		},
		{
			name: "directory stat error",
			inspect: func() reviewEvidence {
				return inspectReviewEvidenceWithStat(evidenceDirectory, func(string) (os.FileInfo, error) {
					return nil, checkFailure
				})
			},
			wantParts: []string{evidenceDirectory, "Could not check the directory", "permission denied"},
		},
		{
			name:      "missing evidence file",
			inspect:   func() reviewEvidence { return inspectReviewEvidence(missingEvidenceDirectory) },
			wantParts: []string{missingEvidenceDirectory, "evidence.md is missing"},
		},
		{
			name:      "evidence path is not a regular file",
			inspect:   func() reviewEvidence { return inspectReviewEvidence(evidenceDirectory) },
			wantParts: []string{evidenceDirectory, "evidence.md is not a regular file"},
		},
		{
			name: "evidence file stat error",
			inspect: func() reviewEvidence {
				return inspectReviewEvidenceWithStat(completeDirectory, func(path string) (os.FileInfo, error) {
					if path == completeDirectory {
						return completeDirectoryInfo, nil
					}
					return nil, checkFailure
				})
			},
			wantParts: []string{completeDirectory, "Could not check evidence.md", "permission denied"},
		},
		{
			name:      "available evidence",
			inspect:   func() reviewEvidence { return inspectReviewEvidence(completeDirectory) },
			wantParts: []string{completeDirectory, "Read evidence.md", "supporting files"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := test.inspect().promptInstruction()
			for _, want := range test.wantParts {
				if !strings.Contains(got, want) {
					t.Errorf("prompt instruction = %q, want it to contain %q", got, want)
				}
			}
		})
	}
}
