package orchestration

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type reviewEvidenceStatus uint8

const (
	reviewEvidenceNoIteration reviewEvidenceStatus = iota
	reviewEvidenceDirectoryMissing
	reviewEvidenceDirectoryNotDirectory
	reviewEvidenceDocumentMissing
	reviewEvidenceDocumentNotRegular
	reviewEvidenceCheckFailed
	reviewEvidenceAvailable
)

type reviewEvidence struct {
	directory   string
	status      reviewEvidenceStatus
	checkTarget string
	checkError  error
}

func noIterationReviewEvidence() reviewEvidence {
	return reviewEvidence{status: reviewEvidenceNoIteration}
}

func inspectReviewEvidence(directory string) reviewEvidence {
	return inspectReviewEvidenceWithStat(directory, os.Stat)
}

func inspectReviewEvidenceWithStat(directory string, stat func(string) (os.FileInfo, error)) reviewEvidence {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return noIterationReviewEvidence()
	}

	evidence := reviewEvidence{directory: directory}
	directoryInfo, err := stat(directory)
	if errors.Is(err, fs.ErrNotExist) {
		evidence.status = reviewEvidenceDirectoryMissing
		return evidence
	}
	if err != nil {
		evidence.status = reviewEvidenceCheckFailed
		evidence.checkTarget = "the directory"
		evidence.checkError = err
		return evidence
	}
	if !directoryInfo.IsDir() {
		evidence.status = reviewEvidenceDirectoryNotDirectory
		return evidence
	}

	evidencePath := filepath.Join(directory, "evidence.md")
	evidenceInfo, err := stat(evidencePath)
	if errors.Is(err, fs.ErrNotExist) {
		evidence.status = reviewEvidenceDocumentMissing
		return evidence
	}
	if err != nil {
		evidence.status = reviewEvidenceCheckFailed
		evidence.checkTarget = "evidence.md"
		evidence.checkError = err
		return evidence
	}
	if !evidenceInfo.Mode().IsRegular() {
		evidence.status = reviewEvidenceDocumentNotRegular
		return evidence
	}
	evidence.status = reviewEvidenceAvailable
	return evidence
}

func (evidence reviewEvidence) promptInstruction() string {
	if evidence.status == reviewEvidenceNoIteration {
		return "This standalone review has no iteration Evidence directory."
	}

	detail := "Read evidence.md and its supporting files."
	switch evidence.status {
	case reviewEvidenceDirectoryMissing:
		detail = "The directory does not exist; evidence.md is missing."
	case reviewEvidenceDirectoryNotDirectory:
		detail = "This path is not a directory; evidence.md is missing."
	case reviewEvidenceDocumentMissing:
		detail = "evidence.md is missing."
	case reviewEvidenceDocumentNotRegular:
		detail = "evidence.md is not a regular file."
	case reviewEvidenceCheckFailed:
		detail = fmt.Sprintf("Could not check %s: %v.", evidence.checkTarget, evidence.checkError)
	}
	return fmt.Sprintf("The Evidence directory for this iteration is %s. %s", evidence.directory, detail)
}
