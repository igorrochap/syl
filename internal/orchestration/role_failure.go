package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/igorrochap/syl/internal/harness"
)

const roleFailureTranscriptLineLimit = 20

func reportRoleFailure(
	ctx context.Context,
	failure error,
	failureTail string,
	adapter harness.Adapter,
	sessionIDs []string,
	recorder RunRecorder,
) error {
	if failure == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return failure
	}

	tail := lastTranscriptLines(failureTail, roleFailureTranscriptLineLimit)
	if tail == "" {
		tail = "(no transcript lines captured)"
	}
	details := []string{fmt.Sprintf("Failure transcript (last %d lines):\n%s", roleFailureTranscriptLineLimit, tail)}
	var transcriptError *harness.SessionTranscriptError
	if !errors.As(failure, &transcriptError) {
		transcriptPath := sessionTranscriptPath(adapter, sessionIDs)
		if transcriptPath == "" {
			transcriptPath = unavailableSessionTranscriptPath(sessionIDs)
		}
		details = append(details, "Session transcript: "+transcriptPath)
	}
	if recorder != nil {
		details = append(details, "Run directory: "+recorderArtifactDir(recorder))
	}
	return fmt.Errorf("%w\n\n%s", failure, strings.Join(details, "\n"))
}

func unavailableSessionTranscriptPath(sessionIDs []string) string {
	if len(sessionIDs) == 0 {
		return "unavailable (no session ID was captured)"
	}
	return "unavailable (session ID: " + sessionIDs[0] + ")"
}

func sessionTranscriptPath(adapter harness.Adapter, sessionIDs []string) string {
	provider, ok := adapter.(harness.SessionTranscriptPathProvider)
	if !ok {
		return ""
	}
	for _, sessionID := range sessionIDs {
		if path := strings.TrimSpace(provider.SessionTranscriptPath(sessionID)); path != "" {
			return path
		}
	}
	return ""
}

func lastTranscriptLines(transcript string, limit int) string {
	transcript = strings.TrimRight(transcript, "\n")
	if transcript == "" || limit <= 0 {
		return ""
	}
	lines := strings.Split(transcript, "\n")
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return strings.Join(lines, "\n")
}
