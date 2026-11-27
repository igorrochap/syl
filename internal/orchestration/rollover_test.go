package orchestration

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gitadapter "github.com/igorrochap/syl/internal/adapters/git"
	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/harness"
	claudetranscript "github.com/igorrochap/syl/internal/harness/claude/transcript"
	"github.com/igorrochap/syl/internal/tracker"
	"github.com/igorrochap/syl/internal/usage"
	"github.com/igorrochap/syl/internal/verdict"
)

const (
	reviseVerdict  = "VERDICT: revise\nSUMMARY: Fix required\nFINDINGS:\n- [blocking] worker.go — handle the error\n- [nit] notes.go — rename the helper\n"
	approveVerdict = "VERDICT: approve\nSUMMARY: Ready\nFINDINGS:\n"
	approveWithNit = "VERDICT: approve\nSUMMARY: Ready\nFINDINGS:\n- [nit] notes.go — rename the helper\n"
)

var rolloverTicket = tracker.Ticket{Number: 287, Title: "Roll over every boundary", Body: "Seed a fresh session."}

type rolloverScenario struct {
	maxIterations   int
	maxGateAttempts int
	nitBudget       int
	implementer     *scriptedConversationAdapter
	verdicts        []string
	gateResults     []GateExecution
	claude          bool
	worktreeRoot    string
	ctx             context.Context
	// rollover overrides the loop.rollover mode. When it is empty, the
	// harness uses iteration-boundary, the mode the ADR 0008 scenarios in this
	// file exercise; production treats an empty value as off.
	rollover config.Rollover
	// onWorkRoot receives the scenario's Work root before the loop starts.
	onWorkRoot func(workRoot string)
}

type rolloverOutcome struct {
	final    verdict.Verdict
	err      error
	recorder *memoryRunRecorder
	output   string
	gate     *scriptedGateRunner
	workRoot string
	// gateCallsAtHandoff is the Gate run count when the last handoff turn ran.
	gateCallsAtHandoff int
}

func runRolloverScenario(t *testing.T, scenario rolloverScenario) rolloverOutcome {
	t.Helper()
	repo, branchPoint := newGateTestRepo(t, "#!/bin/sh\necho gate\n", true)
	if scenario.onWorkRoot != nil {
		scenario.onWorkRoot(repo)
	}
	recorder := newMemoryRunRecorder()
	recorder.dir = t.TempDir()
	reviewer := &scriptedConversationAdapter{}
	for index, text := range scenario.verdicts {
		events := []harness.Event{{Type: harness.EventSession, SessionID: "review-1"}, {Type: harness.EventAssistantText, Text: text}}
		if index == 0 {
			reviewer.runs = append(reviewer.runs, events)
			continue
		}
		reviewer.resumes = append(reviewer.resumes, events)
	}
	gate := &scriptedGateRunner{t: t, recorder: recorder, workRoot: repo, branchPoint: branchPoint, results: scenario.gateResults}
	outcome := rolloverOutcome{recorder: recorder, gate: gate, workRoot: repo}
	handoffHook := scenario.implementer.handoffHook
	scenario.implementer.handoffHook = func() {
		outcome.gateCallsAtHandoff = gate.calls
		if handoffHook != nil {
			handoffHook()
		}
	}
	projectConfig := gateTestConfig(scenario.maxIterations)
	if scenario.claude {
		projectConfig.Roles.Implement.Harness = config.HarnessClaude
	}
	ctx := scenario.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	projectConfig.Loop.NitBudget = scenario.nitBudget
	projectConfig.Loop.Rollover = config.RolloverIterationBoundary
	if scenario.rollover != "" {
		projectConfig.Loop.Rollover = scenario.rollover
	}
	if scenario.maxGateAttempts > 0 {
		projectConfig.Loop.MaxGateAttempts = scenario.maxGateAttempts
	}
	var output strings.Builder
	_, final, _, _, err := runImplementIterations(ctx, implementIterationsParams{
		git: gitadapter.ExecGitRunner{Dir: repo}, workRoot: repo,
		implementer: scenario.implementer, reviewer: reviewer, projectConfig: projectConfig,
		ticket: rolloverTicket, branchPoint: branchPoint, recorder: recorder, gateRunner: gate,
		worktreeArtifactRoot: scenario.worktreeRoot, output: &output, allowEmptyDiff: true,
	})
	outcome.final, outcome.err, outcome.output = final, err, output.String()
	return outcome
}

func implementerTurn(sessionID string) []harness.Event {
	return []harness.Event{
		{Type: harness.EventSession, SessionID: sessionID},
		{Type: harness.EventAssistantText, Text: "Implementation turn."},
	}
}

func passingGates(count int) []GateExecution {
	results := make([]GateExecution, count)
	for index := range results {
		results[index] = GateExecution{Output: qualitySummary("PASS")}
	}
	return results
}

func TestGateAttemptTurnsResumeThePreviousTurnSession(t *testing.T) {
	implementer := &scriptedConversationAdapter{
		runs:    [][]harness.Event{implementerTurn("implement-1")},
		resumes: [][]harness.Event{implementerTurn("implement-2"), implementerTurn("implement-3"), implementerTurn("implement-4")},
	}
	red := GateExecution{ExitStatus: 1, Output: qualitySummary("FAIL")}

	worktreeRoot := t.TempDir()
	outcome := runRolloverScenario(t, rolloverScenario{
		maxIterations: 1, maxGateAttempts: 4, implementer: implementer, verdicts: []string{approveVerdict},
		gateResults:  []GateExecution{red, red, red, {Output: qualitySummary("PASS")}},
		worktreeRoot: worktreeRoot,
	})

	if outcome.err != nil || outcome.final.Status != verdict.Approve {
		t.Fatalf("result = (%q, %v), want approve", outcome.final.Status, outcome.err)
	}
	if implementer.runCalls != 1 || len(implementer.resumeCalls) != 3 {
		t.Fatalf("implementer calls = (%d runs, %d resumes), want one fresh turn and three Gate attempt resumes", implementer.runCalls, len(implementer.resumeCalls))
	}
	for index, call := range implementer.resumeCalls {
		want := fmt.Sprintf("implement-%d", index+1)
		if call.sessionID != want {
			t.Errorf("Gate attempt turn %d resumed %q, want the previous turn's session %q", index+1, call.sessionID, want)
		}
	}
	assertNoGateAttemptHandoff(t, outcome.recorder, implementer, outcome.recorder.dir, worktreeRoot)
}

func assertNoGateAttemptHandoff(t *testing.T, recorder *memoryRunRecorder, implementer *scriptedConversationAdapter, artifactRoots ...string) {
	t.Helper()
	for name := range recorder.files {
		if strings.HasPrefix(name, "handoff-") {
			t.Errorf("recorder holds handoff artifact %q, want none", name)
		}
	}
	for _, root := range artifactRoots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err == nil && strings.Contains(entry.Name(), "-gate-") && strings.HasPrefix(entry.Name(), "handoff-") {
				t.Errorf("artifact root holds Gate attempt handoff %s", path)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, call := range implementer.resumeCalls {
		if strings.Contains(call.prompt, "handoff") {
			t.Errorf("Gate attempt prompt mentions a handoff: %q", call.prompt)
		}
	}
	if len(implementer.handoffCalls) != 0 {
		t.Errorf("handoff turns = %#v, want none", implementer.handoffCalls)
	}
}

func TestReviseVerdictRollsOverIntoFreshSeededSession(t *testing.T) {
	implementer := &scriptedConversationAdapter{
		runs:         [][]harness.Event{implementerTurn("implement-1"), implementerTurn("implement-fresh")},
		resumes:      [][]harness.Event{implementerTurn("implement-1-gate")},
		writeHandoff: true,
	}

	outcome := runRolloverScenario(t, rolloverScenario{
		maxIterations: 2, implementer: implementer, verdicts: []string{reviseVerdict, approveVerdict},
		gateResults: []GateExecution{{ExitStatus: 1, Output: qualitySummary("FAIL")}, {Output: qualitySummary("PASS")}, {Output: qualitySummary("PASS")}},
	})

	if outcome.err != nil || outcome.final.Status != verdict.Approve {
		t.Fatalf("result = (%q, %v), want approve", outcome.final.Status, outcome.err)
	}
	handoffPath := outcome.recorder.ImplementHandoffPath(1)
	if len(implementer.handoffCalls) != 1 {
		t.Fatalf("handoff turns = %#v, want one", implementer.handoffCalls)
	}
	handoff := implementer.handoffCalls[0]
	if handoff.sessionID != "implement-1-gate" {
		t.Errorf("handoff turn resumed %q, want the last session of iteration 1 after its Gate attempt turn", handoff.sessionID)
	}
	if handoff.prompt != composeHandoffTurnPrompt(handoffPath) {
		t.Errorf("handoff turn prompt = %q, want /handoff with %s", handoff.prompt, handoffPath)
	}
	if implementer.runCalls != 2 || len(implementer.resumeCalls) != 1 {
		t.Fatalf("implementer calls = (%d runs, %d resumes), want iteration 2 in a fresh session", implementer.runCalls, len(implementer.resumeCalls))
	}
	assertContainsAll(t, implementer.runRequests[1].Prompt,
		"read the Rollover handoff document at "+handoffPath, rolloverTicket.Title, rolloverTicket.Body,
		"[blocking] worker.go", "handle the error", "[nit] notes.go", "rename the helper",
	)
	if outcome.gateCallsAtHandoff != 2 || outcome.gate.calls != 3 {
		t.Errorf("Gate runs = %d before the handoff turn and %d in total, want both Gate runs of iteration 1 before it and only iteration 2's Gate run after it",
			outcome.gateCallsAtHandoff, outcome.gate.calls)
	}
	if strings.Contains(outcome.output, "warning") {
		t.Errorf("output = %q, want no Rollover warning", outcome.output)
	}
}

func TestRolloverOffResumesTheSameSessionWithoutHandoffTurn(t *testing.T) {
	implementer := &scriptedConversationAdapter{
		runs:         [][]harness.Event{implementerTurn("implement-1")},
		resumes:      [][]harness.Event{implementerTurn("implement-1-gate"), implementerTurn("implement-2")},
		writeHandoff: true,
	}

	outcome := runRolloverScenario(t, rolloverScenario{
		maxIterations: 2, rollover: config.RolloverOff, implementer: implementer, verdicts: []string{reviseVerdict, approveVerdict},
		gateResults: []GateExecution{{ExitStatus: 1, Output: qualitySummary("FAIL")}, {Output: qualitySummary("PASS")}, {Output: qualitySummary("PASS")}},
	})

	if outcome.err != nil || outcome.final.Status != verdict.Approve {
		t.Fatalf("result = (%q, %v), want approve", outcome.final.Status, outcome.err)
	}
	if len(implementer.handoffCalls) != 0 {
		t.Fatalf("handoff turns = %#v, want none", implementer.handoffCalls)
	}
	if implementer.runCalls != 1 || len(implementer.resumeCalls) != 2 {
		t.Fatalf("implementer calls = (%d runs, %d resumes), want one fresh session resumed by the Gate attempt and iteration 2", implementer.runCalls, len(implementer.resumeCalls))
	}
	iterationTwo := implementer.resumeCalls[1]
	if iterationTwo.sessionID != "implement-1-gate" {
		t.Errorf("iteration 2 resumed %q, want the last implementer session of iteration 1", iterationTwo.sessionID)
	}
	if strings.Contains(iterationTwo.prompt, "Rollover handoff document") {
		t.Errorf("iteration 2 prompt = %q, want no Rollover seed", iterationTwo.prompt)
	}
	assertContainsAll(t, iterationTwo.prompt, "[blocking] worker.go", "handle the error")
	handoffPath := outcome.recorder.ImplementHandoffPath(1)
	if _, err := os.Stat(handoffPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat %s error = %v, want no handoff document", handoffPath, err)
	}
	if strings.Contains(outcome.output, "warning") {
		t.Errorf("output = %q, want no Rollover warning", outcome.output)
	}
}

func TestApproveVerdictOverNitBudgetRunsHandoffTurn(t *testing.T) {
	implementer := &scriptedConversationAdapter{
		runs:         [][]harness.Event{implementerTurn("implement-1"), implementerTurn("implement-fresh")},
		writeHandoff: true,
	}

	outcome := runRolloverScenario(t, rolloverScenario{
		maxIterations: 2, implementer: implementer, verdicts: []string{approveWithNit, approveVerdict},
		gateResults: passingGates(2),
	})

	if outcome.err != nil || outcome.final.Status != verdict.Approve {
		t.Fatalf("result = (%q, %v), want approve", outcome.final.Status, outcome.err)
	}
	if len(implementer.handoffCalls) != 1 || implementer.handoffCalls[0].sessionID != "implement-1" {
		t.Fatalf("handoff turns = %#v, want one in implement-1", implementer.handoffCalls)
	}
}

func TestNoHandoffTurnWhenTheLoopEnds(t *testing.T) {
	red := GateExecution{ExitStatus: 1, Output: qualitySummary("FAIL")}
	for _, test := range []struct {
		name          string
		maxIterations int
		verdicts      []string
		gateResults   []GateExecution
	}{
		{name: "approve within nit budget", maxIterations: 2, verdicts: []string{approveVerdict}, gateResults: passingGates(1)},
		{name: "revise on the last iteration", maxIterations: 1, verdicts: []string{reviseVerdict}, gateResults: passingGates(1)},
		{name: "Gate attempt limit", maxIterations: 2, gateResults: []GateExecution{red, red, red}},
	} {
		t.Run(test.name, func(t *testing.T) {
			implementer := &scriptedConversationAdapter{
				runs:         [][]harness.Event{implementerTurn("implement-1")},
				resumes:      [][]harness.Event{implementerTurn("implement-2"), implementerTurn("implement-3")},
				writeHandoff: true,
			}

			outcome := runRolloverScenario(t, rolloverScenario{
				maxIterations: test.maxIterations, implementer: implementer, verdicts: test.verdicts, gateResults: test.gateResults,
			})

			if outcome.err != nil {
				t.Fatalf("runImplementIterations() error = %v", outcome.err)
			}
			if len(implementer.handoffCalls) != 0 {
				t.Fatalf("handoff turns = %#v, want none", implementer.handoffCalls)
			}
		})
	}
}

func TestFailedOrEmptyHandoffTurnKeepsTheSession(t *testing.T) {
	for _, test := range []struct {
		name       string
		handoffErr error
		reason     string
	}{
		{name: "no handoff document", reason: "wrote no handoff document"},
		{name: "handoff turn fails", handoffErr: &scriptedConversationError{message: "harness crashed"}, reason: "harness crashed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			implementer := &scriptedConversationAdapter{
				runs:       [][]harness.Event{implementerTurn("implement-1")},
				resumes:    [][]harness.Event{implementerTurn("implement-1")},
				handoffErr: test.handoffErr,
			}

			outcome := runRolloverScenario(t, rolloverScenario{
				maxIterations: 2, implementer: implementer, verdicts: []string{reviseVerdict, approveVerdict},
				gateResults: passingGates(2),
			})

			if outcome.err != nil || outcome.final.Status != verdict.Approve {
				t.Fatalf("result = (%q, %v), want the Run to continue to approve", outcome.final.Status, outcome.err)
			}
			handoffPath := outcome.recorder.ImplementHandoffPath(1)
			assertContainsAll(t, outcome.output, "syl: warning: iteration 1 Rollover skipped", test.reason, handoffPath)
			if implementer.runCalls != 1 || len(implementer.resumeCalls) != 1 || implementer.resumeCalls[0].sessionID != "implement-1" {
				t.Fatalf("implementer calls = (%d runs, %#v resumes), want iteration 2 to resume implement-1", implementer.runCalls, implementer.resumeCalls)
			}
			if strings.Contains(implementer.resumeCalls[0].prompt, "Rollover handoff document") {
				t.Errorf("iteration 2 prompt = %q, want no Rollover seed", implementer.resumeCalls[0].prompt)
			}
		})
	}
}

func TestImplementerUsageSplitsAtTheRollover(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	implementer := &scriptedConversationAdapter{
		runs: [][]harness.Event{implementerTurn("implement-1"), implementerTurn("implement-fresh")},
		// Each session reports its usage during its own turn.
		runHooks: []func(harness.Request){
			func(harness.Request) { writeCodexRollout(t, home, "implement-1", 80) },
			func(harness.Request) { writeCodexRollout(t, home, "implement-fresh", 30) },
		},
		writeHandoff: true,
		// The handoff turn grows the cumulative total of the iteration 1 session.
		handoffHook: func() { writeCodexRollout(t, home, "implement-1", 100) },
	}

	outcome := runRolloverScenario(t, rolloverScenario{
		maxIterations: 2, implementer: implementer, verdicts: []string{reviseVerdict, approveVerdict},
		gateResults: passingGates(2),
	})

	if outcome.err != nil {
		t.Fatalf("runImplementIterations() error = %v", outcome.err)
	}
	inputTokens := implementInputTokensByIteration(t, outcome.recorder)
	if inputTokens[1] != 100 || inputTokens[2] != 30 {
		t.Fatalf("implement input tokens by iteration = %v, want iteration 1 to include the handoff turn (100) and iteration 2 only its fresh session (30)", inputTokens)
	}
}

func writeCodexRollout(t *testing.T, home, sessionID string, inputTokens int) {
	t.Helper()
	directory := filepath.Join(home, ".codex", "sessions", "2026", "11", "25")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf(`{"timestamp":%q,"payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":0,"total_tokens":%d}}}}`+"\n", time.Now().UTC().Format(time.RFC3339Nano), inputTokens, inputTokens+1)
	if err := os.WriteFile(filepath.Join(directory, "rollout-20261125T000000-"+sessionID+".jsonl"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHandoffTurnWithoutSessionStartsFreshUnseededIteration(t *testing.T) {
	sessionless := []harness.Event{{Type: harness.EventAssistantText, Text: "Implementation turn."}}
	implementer := &scriptedConversationAdapter{
		runs:         [][]harness.Event{sessionless, implementerTurn("implement-fresh")},
		writeHandoff: true,
	}

	outcome := runRolloverScenario(t, rolloverScenario{
		maxIterations: 2, implementer: implementer, verdicts: []string{reviseVerdict, approveVerdict},
		gateResults: passingGates(2),
	})

	if outcome.err != nil || outcome.final.Status != verdict.Approve {
		t.Fatalf("result = (%q, %v), want approve", outcome.final.Status, outcome.err)
	}
	if len(implementer.handoffCalls) != 0 {
		t.Fatalf("handoff turns = %#v, want none without a session to resume", implementer.handoffCalls)
	}
	assertContainsAll(t, outcome.output,
		"syl: warning: iteration 1 Rollover skipped (no implementer session to resume)",
		outcome.recorder.ImplementHandoffPath(1),
		"iteration 2 starts a fresh implementer session without a handoff document",
	)
	if implementer.runCalls != 2 || len(implementer.resumeCalls) != 0 {
		t.Fatalf("implementer calls = (%d runs, %d resumes), want iteration 2 in a fresh session", implementer.runCalls, len(implementer.resumeCalls))
	}
	if strings.Contains(implementer.runRequests[1].Prompt, "Rollover handoff document") {
		t.Errorf("iteration 2 prompt = %q, want no Rollover seed", implementer.runRequests[1].Prompt)
	}
}

func TestCancelledHandoffTurnEndsTheRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	implementer := &scriptedConversationAdapter{
		runs:        [][]harness.Event{implementerTurn("implement-1"), implementerTurn("implement-fresh")},
		resumes:     [][]harness.Event{implementerTurn("implement-1")},
		handoffErr:  context.Canceled,
		handoffHook: cancel,
	}

	outcome := runRolloverScenario(t, rolloverScenario{
		maxIterations: 2, implementer: implementer, verdicts: []string{reviseVerdict, approveVerdict},
		gateResults: passingGates(2), ctx: ctx,
	})

	if !errors.Is(outcome.err, context.Canceled) {
		t.Fatalf("runImplementIterations() error = %v, want the cancellation to end the Run", outcome.err)
	}
	if strings.Contains(outcome.output, "warning") {
		t.Errorf("output = %q, want no Rollover warning for a cancelled Run", outcome.output)
	}
	if implementer.runCalls != 1 || len(implementer.resumeCalls) != 0 {
		t.Fatalf("implementer calls = (%d runs, %d resumes), want no iteration 2 turn", implementer.runCalls, len(implementer.resumeCalls))
	}
}

func TestGateAttemptTurnNeverStartsAFreshSession(t *testing.T) {
	red := GateExecution{ExitStatus: 1, Output: qualitySummary("FAIL")}
	for _, test := range []struct {
		name        string
		implementer *scriptedConversationAdapter
		wantError   string
	}{
		{
			name: "resume fails",
			implementer: &scriptedConversationAdapter{
				runs:              [][]harness.Event{implementerTurn("implement-1"), implementerTurn("implement-fresh")},
				resumes:           [][]harness.Event{{}},
				resumeStartErrors: []error{&scriptedConversationError{message: "session unavailable"}},
			},
			wantError: "session unavailable",
		},
		{
			name: "no session reported",
			implementer: &scriptedConversationAdapter{runs: [][]harness.Event{
				{{Type: harness.EventAssistantText, Text: "Implementation turn."}},
				implementerTurn("implement-fresh"),
			}},
			wantError: "reported no session",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			outcome := runRolloverScenario(t, rolloverScenario{
				maxIterations: 1, implementer: test.implementer, verdicts: []string{approveVerdict},
				gateResults: []GateExecution{red, {Output: qualitySummary("PASS")}},
			})

			if outcome.err == nil || !strings.Contains(outcome.err.Error(), test.wantError) {
				t.Fatalf("runImplementIterations() error = %v, want %q", outcome.err, test.wantError)
			}
			if test.implementer.runCalls != 1 {
				t.Fatalf("implementer runs = %d, want the Gate attempt turn not to start a fresh session", test.implementer.runCalls)
			}
		})
	}
}

func TestClaudeImplementerUsageSplitsAtTheRollover(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var workRoot string
	appendUsage := func(sessionID string, inputTokens int) {
		appendClaudeUsage(t, home, workRoot, sessionID, inputTokens)
	}
	implementer := &scriptedConversationAdapter{
		runs:         [][]harness.Event{implementerTurn("implement-1"), implementerTurn("implement-fresh")},
		writeHandoff: true,
		runHooks: []func(harness.Request){
			func(harness.Request) { appendUsage("implement-1", 10) },
			func(harness.Request) { appendUsage("implement-fresh", 5) },
		},
		handoffHook: func() { appendUsage("implement-1", 20) },
	}

	outcome := runRolloverScenario(t, rolloverScenario{
		maxIterations: 2, implementer: implementer, verdicts: []string{reviseVerdict, approveVerdict},
		gateResults: passingGates(2), claude: true,
		onWorkRoot: func(root string) { workRoot = root },
	})

	if outcome.err != nil {
		t.Fatalf("runImplementIterations() error = %v", outcome.err)
	}
	inputTokens := implementInputTokensByIteration(t, outcome.recorder)
	if inputTokens[1] != 30 || inputTokens[2] != 5 {
		t.Fatalf("implement input tokens by iteration = %v, want iteration 1 to include its turn and the handoff turn (30) and iteration 2 only its fresh session (5)", inputTokens)
	}
}

// implementInputTokensByIteration folds the recorded usage the way the Run's
// usage artifact does, keeping the last entry per iteration and Role.
func implementInputTokensByIteration(t *testing.T, recorder *memoryRunRecorder) map[int]int64 {
	t.Helper()
	artifact := usage.NewArtifact()
	for _, entry := range recorder.usage {
		artifact.Upsert(entry)
	}
	inputTokens := map[int]int64{}
	for _, entry := range artifact.Entries {
		if entry.Role != "implement" {
			continue
		}
		if entry.Metrics == nil {
			t.Fatalf("implement usage for iteration %d is untracked: %s", entry.Iteration, entry.Reason)
		}
		inputTokens[entry.Iteration] = entry.Metrics.InputTokens
	}
	return inputTokens
}

func appendClaudeUsage(t *testing.T, home, workRoot, sessionID string, inputTokens int) {
	t.Helper()
	path, err := claudetranscript.New(home).Find(workRoot, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}()
	messageID := fmt.Sprintf("%s-%d", sessionID, time.Now().UnixNano())
	line := fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"id":%q,"role":"assistant","usage":{"input_tokens":%d,"output_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`+"\n",
		time.Now().UTC().Format(time.RFC3339Nano), messageID, inputTokens)
	if _, err := file.WriteString(line); err != nil {
		t.Fatal(err)
	}
}
