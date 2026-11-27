package orchestration

import (
	"context"
	"fmt"
	"time"

	"github.com/igorrochap/syl/internal/config"
)

// iterationHandoff describes the implementer session that ends at an
// iteration boundary and hands its context to a fresh session.
type iterationHandoff struct {
	iteration      int
	sessionID      string
	usageStartedAt time.Time
	mode           HarnessOutputMode
}

// rollsOverAtBoundary reports whether the implementer rolls over after this
// iteration. With loop.rollover = "off" the next iteration resumes the same
// session: Codex caches resumed history, so a fresh session's cold start costs
// more than it saves (ADR 0008). The last iteration has no next one to seed.
func rollsOverAtBoundary(params implementIterationsParams, iteration int) bool {
	loop := params.projectConfig.Loop
	return loop.RolloverMode() == config.RolloverIterationBoundary && iteration < loop.MaxIterations
}

// rollOverImplementer runs the handoff turn at an iteration boundary (ADR
// 0008). It returns the session the next iteration resumes and the handoff
// document that seeds it; at most one of them is set. A failed or empty
// handoff turn is not a Run failure: the next iteration keeps the old session.
func rollOverImplementer(ctx context.Context, params implementIterationsParams, handoff iterationHandoff) (string, string, error) {
	handoffPath, err := prepareIterationHandoffPath(params, handoff.iteration)
	if err != nil {
		return "", "", err
	}
	if handoff.sessionID == "" {
		warnRolloverSkipped(params, handoff.iteration, handoffPath,
			"no implementer session to resume", "starts a fresh implementer session without a handoff document")
		return "", "", nil
	}
	if err := runHandoffTurn(ctx, params, handoff, handoffPath); err != nil {
		// Cancellation ends the Run; any other handoff failure only skips the Rollover.
		if ctx.Err() != nil {
			return "", "", err
		}
		warnRolloverResumes(params, handoff.iteration, handoffPath, err.Error())
		return handoff.sessionID, "", nil
	}
	written, err := handoffExists(handoffPath)
	if err != nil {
		return "", "", err
	}
	if !written {
		warnRolloverResumes(params, handoff.iteration, handoffPath, "the handoff turn wrote no handoff document")
		return handoff.sessionID, "", nil
	}
	return "", handoffPath, nil
}

func runHandoffTurn(ctx context.Context, params implementIterationsParams, handoff iterationHandoff, handoffPath string) error {
	execution, err := runImplementerHarnessTurn(ctx, params, implementerHarnessTurn{
		iteration:      handoff.iteration,
		label:          fmt.Sprintf("%d/%d — writing handoff", handoff.iteration, params.projectConfig.Loop.MaxIterations),
		mode:           handoff.mode,
		request:        implementerRequest(params, composeHandoffTurnPrompt(handoffPath)),
		sessionID:      handoff.sessionID,
		resumeOnly:     true,
		usageStartedAt: handoff.usageStartedAt,
	})
	if err != nil {
		return err
	}
	if err := params.recorder.RecordSessions(handoff.iteration, "implement", execution.SessionIDs); err != nil {
		return fmt.Errorf("record implement sessions: %w", err)
	}
	return nil
}

func warnRolloverResumes(params implementIterationsParams, iteration int, handoffPath, reason string) {
	warnRolloverSkipped(params, iteration, handoffPath, reason, "resumes the same implementer session")
}

func warnRolloverSkipped(params implementIterationsParams, iteration int, handoffPath, reason, consequence string) {
	_, _ = fmt.Fprintf(
		params.output,
		"syl: warning: iteration %d Rollover skipped (%s); expected handoff document at %s; iteration %d %s\n",
		iteration, reason, handoffPath, iteration+1, consequence,
	)
}
