package harness

import (
	"errors"
	"strings"
	"testing"
)

func TestSessionStalledErrorCarriesSessionSummary(t *testing.T) {
	wantSummary := "Task notifications: 2 enqueued, 0 dequeued or removed"
	stalled := &SessionStalledError{SessionID: "review-session", StateSummary: wantSummary}
	if !errors.Is(stalled, ErrSessionStalled) {
		t.Fatalf("errors.Is() = false, want ErrSessionStalled")
	}
	var typed *SessionStalledError
	if !errors.As(stalled, &typed) || typed.SessionID != "review-session" || typed.StateSummary != wantSummary {
		t.Fatalf("errors.As() = %#v, want session id and summary", typed)
	}
	if message := stalled.Error(); !strings.Contains(message, "review-session") || !strings.Contains(message, wantSummary) {
		t.Fatalf("Error() = %q, want session id and summary", message)
	}
}

func TestSessionStalledErrorUsesFallbackForEmptySummary(t *testing.T) {
	message := (&SessionStalledError{SessionID: "review-session"}).Error()
	if !strings.Contains(message, "Session state:\nunavailable") {
		t.Fatalf("Error() = %q, want unavailable summary", message)
	}
}
