package orchestration

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gitadapter "github.com/igorrochap/syl/internal/adapters/git"
	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/runrecord"
	"github.com/igorrochap/syl/internal/tracker"
	"github.com/igorrochap/syl/internal/verdict"
)

func TestImplementStartsNewTurnAfterRedGateWithinIterationThenReviews(t *testing.T) {
	repo, branchPoint := newGateTestRepo(t, "#!/bin/sh\necho branch-point gate\n", true)
	if err := os.WriteFile(filepath.Join(repo, "scripts", "quality.sh"), []byte("#!/bin/sh\necho working-tree gate\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	recorder := newMemoryRunRecorder()
	recorder.dir = t.TempDir()
	implementer := gateImplementer(2)
	reviewer := gateReviewer("VERDICT: approve\nSUMMARY: Ready\nFINDINGS:\n")
	gateRunner := &scriptedGateRunner{
		t: t, recorder: recorder, workRoot: repo, branchPoint: branchPoint,
		results: []GateExecution{
			{ExitStatus: 0, Output: qualitySummary("FAIL")},
			{ExitStatus: 0, Output: qualitySummary("PASS")},
		},
		wantScript: "#!/bin/sh\necho branch-point gate\n",
	}
	state := newRunStateTracker(recorder, runrecord.New(runrecord.Implement, "#263", 0, 1, testNow()))
	iterations, final, _, err := runImplementIterations(context.Background(), implementIterationsParams{
		git:            gitadapter.ExecGitRunner{Dir: repo},
		workRoot:       repo,
		implementer:    implementer,
		reviewer:       reviewer,
		projectConfig:  gateTestConfig(1),
		ticket:         tracker.Ticket{Number: 263, Title: "Gate attempt", Body: "Fix failures."},
		branchPoint:    branchPoint,
		recorder:       recorder,
		runState:       state,
		gateRunner:     gateRunner,
		output:         io.Discard,
		allowEmptyDiff: true,
	})
	if err != nil {
		t.Fatalf("runImplementIterations() error = %v", err)
	}
	if iterations != 1 || final.Status != verdict.Approve {
		t.Fatalf("result = (%d, %q), want one iteration ending in approve", iterations, final.Status)
	}
	if implementer.runCalls != 1 || len(implementer.resumeCalls) != 1 {
		t.Fatalf("implementer calls = (%d runs, %d resumes), want an initial turn and a new Gate attempt turn", implementer.runCalls, len(implementer.resumeCalls))
	}
	if len(reviewer.runRequests) != 1 || gateRunner.calls != 2 {
		t.Fatalf("reviewer calls = %d, Gate calls = %d, want one review after two Gate attempts", len(reviewer.runRequests), gateRunner.calls)
	}
	if len(recorder.states) < 3 || recorder.states[2].Activity != runrecord.Verifying {
		t.Fatalf("state after implementer turn = %#v, want verifying", recorder.states)
	}
	gateAttemptPrompt := implementer.resumeCalls[0].prompt
	for _, want := range []string{"Failed Gates: tests (FAIL)", filepath.Join(recorder.dir, "iteration-01-gate-attempt-01.output.txt")} {
		if !strings.Contains(gateAttemptPrompt, want) {
			t.Errorf("Gate attempt prompt = %q, want %q", gateAttemptPrompt, want)
		}
	}
	if strings.Contains(gateAttemptPrompt, "Blocking findings:") ||
		strings.HasPrefix(gateAttemptPrompt, "/implement") || strings.HasPrefix(gateAttemptPrompt, "/fix-review") {
		t.Errorf("Gate attempt prompt has conflicting skill instructions or reviewer findings: %q", gateAttemptPrompt)
	}
	for _, want := range []string{"Continue implementing the ticket", "Ticket: #263", "Title: Gate attempt"} {
		if !strings.Contains(gateAttemptPrompt, want) {
			t.Errorf("Gate attempt prompt = %q, want dedicated ticket instruction %q", gateAttemptPrompt, want)
		}
	}
	greenResultPath := filepath.Join(recorder.dir, "iteration-01-gate-attempt-02.txt")
	if !strings.Contains(reviewer.runRequests[0].Prompt, greenResultPath) {
		t.Errorf("reviewer prompt = %q, want Gate result path %q", reviewer.runRequests[0].Prompt, greenResultPath)
	}
	if !strings.Contains(recorder.files["iteration-01-gate-attempt-01.txt"], "tests: FAIL") ||
		!strings.Contains(recorder.files["iteration-01-gate-attempt-01.output.txt"], "QUALITY GATE") {
		t.Errorf("first Gate attempt was not fully recorded: %#v", recorder.files)
	}
}

func TestImplementUsesGateExitStatusWhenSummaryIsUnavailable(t *testing.T) {
	repo, branchPoint := newGateTestRepo(t, "#!/bin/sh\necho legacy\n", true)
	recorder := newMemoryRunRecorder()
	recorder.dir = t.TempDir()
	implementer := gateImplementer(2)
	reviewer := gateReviewer("VERDICT: approve\nSUMMARY: Ready\nFINDINGS:\n")
	gateRunner := &scriptedGateRunner{
		t: t, recorder: recorder, workRoot: repo, branchPoint: branchPoint,
		results: []GateExecution{
			{ExitStatus: 7, Output: "legacy output without a summary"},
			{ExitStatus: 0, Output: "legacy gate passed without a summary"},
		},
	}
	iterations, final, _, err := runImplementIterations(context.Background(), implementIterationsParams{
		git: gitadapter.ExecGitRunner{Dir: repo}, workRoot: repo,
		implementer: implementer, reviewer: reviewer, projectConfig: gateTestConfig(1),
		ticket: tracker.Ticket{Number: 264}, branchPoint: branchPoint, recorder: recorder,
		gateRunner: gateRunner, output: io.Discard, allowEmptyDiff: true,
	})
	if err != nil || iterations != 1 || final.Status != verdict.Approve {
		t.Fatalf("runImplementIterations() = (%d, %q, %v), want one approved iteration", iterations, final.Status, err)
	}
	if !strings.Contains(implementer.resumeCalls[0].prompt, "Per-Gate results are unavailable") ||
		!strings.Contains(implementer.resumeCalls[0].prompt, filepath.Join(recorder.dir, "iteration-01-gate-attempt-01.output.txt")) {
		t.Fatalf("legacy Gate attempt prompt = %q, want unavailable results and full output path", implementer.resumeCalls[0].prompt)
	}
	if !strings.Contains(recorder.files["iteration-01-gate-attempt-01.txt"], "Per-Gate results: unavailable") ||
		!strings.Contains(recorder.files["iteration-01-gate-attempt-01.output.txt"], "legacy output without a summary") {
		t.Fatalf("legacy Gate attempt record = %#v", recorder.files)
	}
	if len(reviewer.runRequests) != 1 || !strings.Contains(reviewer.runRequests[0].Prompt, "iteration-01-gate-attempt-02.txt") {
		t.Fatalf("reviewer prompt = %#v, want green legacy Gate result path", reviewer.runRequests)
	}
}

func TestCommandGateRunnerSetsBaseRefAndRunsExtractedCopy(t *testing.T) {
	baseRef := "branch-point-263"
	scriptPath := filepath.Join(t.TempDir(), "quality.sh")
	script := "#!/bin/sh\nprintf 'QUALITY GATE\\n  format        PASS\\n  vet           PASS\\n  style         PASS\\n  complexity    PASS\\n  coverage      PASS\\n  tests         PASS\\n  architecture  PASS\\n'\nprintf 'base-ref=%s\\n' \"$QUALITY_BASE_REF\"\nprintf 'cwd=%s\\n' \"$PWD\"\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	workRoot := t.TempDir()
	result := (CommandGateRunner{}).Run(context.Background(), scriptPath, workRoot, baseRef)
	if result.ExitStatus != 0 {
		t.Fatalf("Gate exit status = %d, output %q, want zero", result.ExitStatus, result.Output)
	}
	if !strings.Contains(result.Output, "base-ref="+baseRef) {
		t.Fatalf("Gate output = %q, want QUALITY_BASE_REF %q", result.Output, baseRef)
	}
	resolvedWorkRoot, err := filepath.EvalSymlinks(workRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "cwd="+resolvedWorkRoot) {
		t.Fatalf("Gate output = %q, want Work root %q", result.Output, resolvedWorkRoot)
	}
	if parsed := parseGateSummary(result.Output); !parsed.valid || len(parsed.results) != len(gateOrder) || parsed.results[len(parsed.results)-1].Result != "PASS" {
		t.Fatalf("parsed summary = %#v, want a valid passing summary", parsed)
	}
}

func TestImplementResetsGateAttemptNumberAfterGreen(t *testing.T) {
	repo, branchPoint := newGateTestRepo(t, "#!/bin/sh\necho gate\n", true)
	recorder := newMemoryRunRecorder()
	recorder.dir = t.TempDir()
	implementer := gateImplementer(4)
	reviewer := &scriptedConversationAdapter{
		runs: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "review-1"},
			{Type: harness.EventAssistantText, Text: "VERDICT: revise\nSUMMARY: Fix required\nFINDINGS:\n- [blocking] file.go:1 — fix it\n"},
		}},
		resumes: [][]harness.Event{{
			{Type: harness.EventSession, SessionID: "review-1"},
			{Type: harness.EventAssistantText, Text: "VERDICT: approve\nSUMMARY: Ready\nFINDINGS:\n"},
		}},
	}
	gateRunner := &scriptedGateRunner{
		t: t, recorder: recorder, workRoot: repo, branchPoint: branchPoint,
		results: []GateExecution{
			{ExitStatus: 0, Output: qualitySummary("FAIL")},
			{ExitStatus: 0, Output: qualitySummary("PASS")},
			{ExitStatus: 0, Output: qualitySummary("FAIL")},
			{ExitStatus: 0, Output: qualitySummary("PASS")},
		},
	}
	iterations, final, _, err := runImplementIterations(context.Background(), implementIterationsParams{
		git: gitadapter.ExecGitRunner{Dir: repo}, workRoot: repo,
		implementer: implementer, reviewer: reviewer, projectConfig: gateTestConfig(2),
		ticket: tracker.Ticket{Number: 269}, branchPoint: branchPoint, recorder: recorder,
		gateRunner: gateRunner, output: io.Discard, allowEmptyDiff: true,
	})
	if err != nil || iterations != 2 || final.Status != verdict.Approve {
		t.Fatalf("runImplementIterations() = (%d, %q, %v), want two iterations ending in approve", iterations, final.Status, err)
	}
	if _, ok := recorder.files["iteration-02-gate-attempt-01.txt"]; !ok {
		t.Fatalf("second iteration did not restart Gate attempt numbering: %#v", recorder.files)
	}
	if _, ok := recorder.files["iteration-02-gate-attempt-02.txt"]; !ok {
		t.Fatalf("second iteration did not start a new turn after a red Gate: %#v", recorder.files)
	}
	gateAttemptPrompt := implementer.resumeCalls[2].prompt
	if !strings.Contains(gateAttemptPrompt, "iteration-02-gate-attempt-01.output.txt") || strings.Contains(gateAttemptPrompt, "file.go:1") {
		t.Fatalf("iteration-two Gate attempt prompt = %q, want its Gate output and no prior reviewer finding", gateAttemptPrompt)
	}
}

func TestGateDrivenTurnKeepsRolloverAndQuestionProtocol(t *testing.T) {
	repo, branchPoint := newGateTestRepo(t, "#!/bin/sh\necho gate\n", true)
	recorder := newMemoryRunRecorder()
	recorder.dir = t.TempDir()
	handoffPath := recorder.ImplementHandoffPath(1)
	implementer := &scriptedConversationAdapter{
		runs: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "implement-1"}},
			{{Type: harness.EventSession, SessionID: "implement-2"}},
		},
		runHooks: []func(harness.Request){func(harness.Request) {
			if err := os.WriteFile(handoffPath, []byte("Rollover context"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	}
	reviewer := gateReviewer("VERDICT: approve\nSUMMARY: Ready\nFINDINGS:\n")
	gateRunner := &scriptedGateRunner{
		t: t, recorder: recorder, workRoot: repo, branchPoint: branchPoint,
		results: []GateExecution{
			{ExitStatus: 1, Output: "old gate output"},
			{ExitStatus: 0, Output: qualitySummary("PASS")},
		},
	}
	_, final, _, err := runImplementIterations(context.Background(), implementIterationsParams{
		git: gitadapter.ExecGitRunner{Dir: repo}, workRoot: repo,
		implementer: implementer, reviewer: reviewer, projectConfig: gateTestConfig(1),
		ticket:      tracker.Ticket{Number: 270, Title: "Keep rollover", Body: "Retain the ticket."},
		branchPoint: branchPoint, recorder: recorder, gateRunner: gateRunner,
		output: io.Discard, allowEmptyDiff: true,
	})
	if err != nil || final.Status != verdict.Approve {
		t.Fatalf("runImplementIterations() = (%q, %v), want approve", final.Status, err)
	}
	if implementer.runCalls != 2 || len(implementer.resumeCalls) != 0 {
		t.Fatalf("implementer calls = (%d runs, %d resumes), want fresh Rollover turn", implementer.runCalls, len(implementer.resumeCalls))
	}
	prompt := implementer.runRequests[1].Prompt
	for _, want := range []string{
		handoffPath,
		"Keep rollover",
		"Per-Gate results are unavailable",
		filepath.Join(recorder.dir, "iteration-01-gate-attempt-01.output.txt"),
		"QUESTION:",
		"END QUESTION",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("Gate Rollover prompt = %q, want %q", prompt, want)
		}
	}
}

func TestNonExecutableBranchPointGateIsRecordedAsRed(t *testing.T) {
	repo, branchPoint := newGateTestRepo(t, "#!/bin/sh\necho gate\n", false)
	recorder := newMemoryRunRecorder()
	recorder.dir = t.TempDir()
	implementer := gateImplementer(1)
	reviewer := gateReviewer("VERDICT: approve\nSUMMARY: Must not review\nFINDINGS:\n")
	runState := newRunStateTracker(recorder, runrecord.New(runrecord.Implement, "#271", 0, 1, testNow()))
	options, run := gateExecuteOptions(repo, branchPoint, recorder, implementer, reviewer, nil, 1, 1, runState)
	err := executeImplementRun(context.Background(), options, run)
	if err == nil || !strings.Contains(err.Error(), "Gate attempt limit") {
		t.Fatalf("executeImplementRun() error = %v, want exhaustion after failed start", err)
	}
	if !strings.Contains(recorder.files["iteration-01-gate-attempt-01.txt"], "Exit status: -1") ||
		!strings.Contains(recorder.files["iteration-01-gate-attempt-01.output.txt"], "could not start") {
		t.Fatalf("non-executable Gate attempt = %#v, want start failure output and status", recorder.files)
	}
	if len(reviewer.runRequests) != 0 {
		t.Fatalf("reviewer ran after Gate could not start: %#v", reviewer.runRequests)
	}
}

func TestParseGateSummaryRejectsMalformedOrRepeatedResults(t *testing.T) {
	for _, output := range []string{
		"QUALITY GATE\n  tests FAIL\n",
		"QUALITY GATE\n  tests          PASS\n  tests          FAIL\n",
		"QUALITY GATE\n  tests          PASS\nQUALITY GATE\n",
	} {
		if summary := parseGateSummary(output); summary.valid {
			t.Errorf("parseGateSummary(%q) = %#v, want unavailable summary", output, summary)
		}
	}
}

func TestImplementRecordsNoGateFromBranchPoint(t *testing.T) {
	repo, branchPoint := newGateTestRepo(t, "", false)
	if err := os.MkdirAll(filepath.Join(repo, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "scripts", "quality.sh"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	recorder := newMemoryRunRecorder()
	recorder.dir = t.TempDir()
	implementer := gateImplementer(1)
	reviewer := gateReviewer("VERDICT: approve\nSUMMARY: Ready\nFINDINGS:\n")
	gateRunner := &scriptedGateRunner{t: t, recorder: recorder, workRoot: repo, branchPoint: branchPoint}
	_, final, _, err := runImplementIterations(context.Background(), implementIterationsParams{
		git: gitadapter.ExecGitRunner{Dir: repo}, workRoot: repo,
		implementer: implementer, reviewer: reviewer, projectConfig: gateTestConfig(1),
		ticket: tracker.Ticket{Number: 265}, branchPoint: branchPoint, recorder: recorder,
		gateRunner: gateRunner, output: io.Discard, allowEmptyDiff: true,
	})
	if err != nil || final.Status != verdict.Approve {
		t.Fatalf("runImplementIterations() = (%q, %v), want approve", final.Status, err)
	}
	if gateRunner.calls != 0 || recorder.files["iteration-01-gate.txt"] == "" {
		t.Fatalf("Gate calls = %d, no-Gate record = %q", gateRunner.calls, recorder.files["iteration-01-gate.txt"])
	}
	if !strings.Contains(reviewer.runRequests[0].Prompt, filepath.Join(recorder.dir, "iteration-01-gate.txt")) {
		t.Fatalf("reviewer prompt = %q, want no-Gate result path", reviewer.runRequests[0].Prompt)
	}
}

func TestImplementExhaustsAtGateAttemptLimitAndKeepsReason(t *testing.T) {
	repo, branchPoint := newGateTestRepo(t, "#!/bin/sh\nexit 1\n", true)
	recorder := newMemoryRunRecorder()
	recorder.dir = t.TempDir()
	implementer := gateImplementer(2)
	reviewer := gateReviewer("VERDICT: approve\nSUMMARY: Should not review\nFINDINGS:\n")
	gateRunner := &scriptedGateRunner{
		t: t, recorder: recorder, workRoot: repo, branchPoint: branchPoint,
		results: []GateExecution{
			{ExitStatus: 1, Output: "red one"},
			{ExitStatus: 1, Output: "red two"},
		},
	}
	runState := newRunStateTracker(recorder, runrecord.New(runrecord.Implement, "#266", 0, 2, testNow()))
	options, run := gateExecuteOptions(repo, branchPoint, recorder, implementer, reviewer, gateRunner, 2, 2, runState)
	err := executeImplementRun(context.Background(), options, run)
	if err == nil || !strings.Contains(err.Error(), "Gate attempt limit (2) in iteration 1") {
		t.Fatalf("executeImplementRun() error = %v, want Gate attempt limit", err)
	}
	if runState.state.Status != runrecord.Exhausted || runState.state.ExhaustionReason != "Gate attempt limit (2) in iteration 1" {
		t.Fatalf("Run state = %#v, want exhausted at Gate attempt limit", runState.state)
	}
	if len(reviewer.runRequests) != 0 || implementer.runCalls != 1 || len(implementer.resumeCalls) != 1 {
		t.Fatalf("harness calls = (%d implement runs, %d implement resumes, %d reviews), want 2 implementer turns and no review", implementer.runCalls, len(implementer.resumeCalls), len(reviewer.runRequests))
	}
	if !strings.Contains(recorder.files["summary.txt"], "Exhaustion reason: Gate attempt limit (2) in iteration 1") {
		t.Fatalf("summary = %q, want exhaustion reason", recorder.files["summary.txt"])
	}
}

func TestImplementExhaustsAtIterationLimitWithReason(t *testing.T) {
	repo, branchPoint := newGateTestRepo(t, "#!/bin/sh\necho QUALITY GATE\necho '  tests          PASS'\n", true)
	recorder := newMemoryRunRecorder()
	recorder.dir = t.TempDir()
	implementer := gateImplementer(1)
	reviewer := gateReviewer("VERDICT: revise\nSUMMARY: Needs work\nFINDINGS:\n- [blocking] file.go:1 — fix it\n")
	gateRunner := &scriptedGateRunner{
		t: t, recorder: recorder, workRoot: repo, branchPoint: branchPoint,
		results: []GateExecution{{ExitStatus: 0, Output: qualitySummary("PASS")}},
	}
	runState := newRunStateTracker(recorder, runrecord.New(runrecord.Implement, "#267", 0, 1, testNow()))
	options, run := gateExecuteOptions(repo, branchPoint, recorder, implementer, reviewer, gateRunner, 1, 3, runState)
	err := executeImplementRun(context.Background(), options, run)
	if err == nil || !strings.Contains(err.Error(), "max iterations") {
		t.Fatalf("executeImplementRun() error = %v, want iteration limit", err)
	}
	if runState.state.Status != runrecord.Exhausted || runState.state.ExhaustionReason != "iteration limit (1)" {
		t.Fatalf("Run state = %#v, want exhausted at iteration limit", runState.state)
	}
	if !strings.Contains(recorder.files["summary.txt"], "Exhaustion reason: iteration limit (1)") {
		t.Fatalf("summary = %q, want iteration limit reason", recorder.files["summary.txt"])
	}
}

func gateExecuteOptions(
	repo, branchPoint string,
	recorder *memoryRunRecorder,
	implementer, reviewer *scriptedConversationAdapter,
	gateRunner GateRunner,
	maxIterations, maxGateAttempts int,
	runState *runStateTracker,
) (ImplementOptions, implementRunState) {
	git := gitadapter.ExecGitRunner{Dir: repo}
	options := ImplementOptions{
		OriginRoot: repo, WorkRoot: repo,
		ProjectConfig: config.Config{
			Roles: config.RolesConfig{
				Implement: config.RoleConfig{Harness: config.HarnessCodex},
				Review:    config.RoleConfig{Harness: config.HarnessClaude},
			},
			Loop: config.LoopConfig{MaxIterations: maxIterations, MaxGateAttempts: maxGateAttempts},
		},
		Ticket: tracker.Ticket{Number: 268}, Implementer: implementer, Reviewer: reviewer,
		GateRunner: gateRunner, Output: io.Discard,
	}
	return options, implementRunState{
		setup:    implementSetup{git: git, branchPoint: branchPoint},
		recorder: recorder, runState: runState,
	}
}

func gateTestConfig(maxIterations int) config.Config {
	return config.Config{
		Roles: config.RolesConfig{
			Implement: config.RoleConfig{Harness: config.HarnessCodex},
			Review:    config.RoleConfig{Harness: config.HarnessClaude},
		},
		Loop: config.LoopConfig{MaxIterations: maxIterations, MaxGateAttempts: 3},
	}
}

func gateImplementer(turns int) *scriptedConversationAdapter {
	implementer := &scriptedConversationAdapter{}
	for turn := 0; turn < turns; turn++ {
		events := []harness.Event{
			{Type: harness.EventSession, SessionID: fmt.Sprintf("implement-%d", turn+1)},
			{Type: harness.EventAssistantText, Text: "Implementation turn."},
		}
		if turn == 0 {
			implementer.runs = append(implementer.runs, events)
			continue
		}
		implementer.resumes = append(implementer.resumes, events)
	}
	return implementer
}

func gateReviewer(response string) *scriptedConversationAdapter {
	return &scriptedConversationAdapter{runs: [][]harness.Event{{
		{Type: harness.EventSession, SessionID: "review-1"},
		{Type: harness.EventAssistantText, Text: response},
	}}}
}

type scriptedGateRunner struct {
	t           *testing.T
	recorder    *memoryRunRecorder
	workRoot    string
	branchPoint string
	wantScript  string
	results     []GateExecution
	calls       int
}

func (runner *scriptedGateRunner) Run(_ context.Context, scriptPath, workRoot, branchPoint string) GateExecution {
	runner.t.Helper()
	runner.calls++
	if workRoot != runner.workRoot || branchPoint != runner.branchPoint {
		runner.t.Errorf("Gate args = (work root %q, branch point %q), want (%q, %q)", workRoot, branchPoint, runner.workRoot, runner.branchPoint)
	}
	if len(runner.recorder.states) == 0 || runner.recorder.states[len(runner.recorder.states)-1].Activity != runrecord.Verifying {
		runner.t.Errorf("Run activity during Gate = %#v, want verifying", runner.recorder.states)
	}
	contents, err := os.ReadFile(scriptPath)
	if err != nil {
		runner.t.Errorf("read extracted Gate script: %v", err)
	} else if runner.wantScript != "" && string(contents) != runner.wantScript {
		runner.t.Errorf("extracted Gate script = %q, want branch-point copy %q", contents, runner.wantScript)
	}
	if len(runner.results) == 0 {
		return GateExecution{ExitStatus: 0, Output: qualitySummary("PASS")}
	}
	result := runner.results[0]
	runner.results = runner.results[1:]
	return result
}

func qualitySummary(result string) string {
	var summary strings.Builder
	summary.WriteString("QUALITY GATE\n")
	for _, gate := range gateOrder {
		gateResult := "PASS"
		if gate == "tests" {
			gateResult = result
		}
		fmt.Fprintf(&summary, "  %-14s%s\n", gate, gateResult)
	}
	return summary.String()
}

func newGateTestRepo(t *testing.T, script string, executable bool) (string, string) {
	t.Helper()
	root := t.TempDir()
	gitTestCommand(t, root, "init", "-q")
	gitTestCommand(t, root, "config", "user.email", "gate-test@example.com")
	gitTestCommand(t, root, "config", "user.name", "Gate Test")
	if script != "" {
		path := filepath.Join(root, "scripts", "quality.sh")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if executable {
			mode = 0o755
		}
		if err := os.WriteFile(path, []byte(script), mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "change.txt"), []byte("branch-point\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTestCommand(t, root, "add", "-A")
	gitTestCommand(t, root, "commit", "-m", "branch point")
	if err := os.WriteFile(filepath.Join(root, "change.txt"), []byte("working-tree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, strings.TrimSpace(gitTestCommand(t, root, "rev-parse", "HEAD"))
}

func gitTestCommand(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func testNow() time.Time {
	return time.Now().UTC()
}

func TestRevisionCarriesAllVerdictFindingsAndDeclineReasons(t *testing.T) {
	repo, branchPoint := newGateTestRepo(t, "#!/bin/sh\necho gate\n", true)
	recorder := newMemoryRunRecorder()
	recorder.dir = t.TempDir()
	implementer := gateImplementer(3)
	implementer.resumes[0][1].Text = "Declined nit n1: already covered."
	implementer.resumes[1][1].Text = "Gate attempt output."
	reviewer := &scriptedConversationAdapter{
		runs:    [][]harness.Event{{{Type: harness.EventSession, SessionID: "review-1"}, {Type: harness.EventAssistantText, Text: "VERDICT: revise\nSUMMARY: Fix required\nFINDINGS:\n- [blocking] a.go — block one\n- [blocking] b.go — block two\n- [nit] c.go — nit one\n- [nit] d.go — nit two\n- [nit] e.go — nit three\n"}}},
		resumes: [][]harness.Event{{{Type: harness.EventSession, SessionID: "review-1"}, {Type: harness.EventAssistantText, Text: "VERDICT: approve\nSUMMARY: Ready\nFINDINGS:\n"}}},
	}
	gateRunner := &scriptedGateRunner{t: t, recorder: recorder, workRoot: repo, branchPoint: branchPoint, results: []GateExecution{{Output: qualitySummary("PASS")}, {Output: qualitySummary("FAIL")}, {Output: qualitySummary("PASS")}}}
	_, _, _, err := runImplementIterations(context.Background(), implementIterationsParams{git: gitadapter.ExecGitRunner{Dir: repo}, workRoot: repo, implementer: implementer, reviewer: reviewer, projectConfig: gateTestConfig(2), ticket: tracker.Ticket{Number: 278}, branchPoint: branchPoint, recorder: recorder, gateRunner: gateRunner, output: io.Discard, allowEmptyDiff: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, prompt := range []string{implementer.resumeCalls[0].prompt, reviewer.resumeCalls[0].prompt} {
		assertContainsAll(t, prompt, "[blocking] a.go", "block one", "[blocking] b.go", "block two", "[nit] c.go", "nit one", "[nit] d.go", "nit two", "[nit] e.go", "nit three")
	}
	prompt := reviewer.resumeCalls[0].prompt
	for _, name := range []string{"iteration-02-implement-turn-01.feed", "iteration-02-implement-turn-02.feed"} {
		assertContainsAll(t, prompt, filepath.Join(recorder.dir, name))
	}
	if strings.TrimSpace(recorder.files["iteration-02-implement-turn-01.feed"]) != "Declined nit n1: already covered." || strings.TrimSpace(recorder.files["iteration-02-implement-turn-02.feed"]) != "Gate attempt output." {
		t.Fatalf("recorded feeds = %#v", recorder.files)
	}
	if strings.Contains(prompt, "Declined nit n1: already covered.") {
		t.Fatal("review prompt contains full implementer output")
	}
}

func TestRevisionFreshReviewFallbackCarriesFindingsAndDeclineReasons(t *testing.T) {
	repo, branchPoint := newGateTestRepo(t, "#!/bin/sh\necho gate\n", true)
	recorder := newMemoryRunRecorder()
	recorder.dir = t.TempDir()
	implementer := gateImplementer(2)
	implementer.resumes[0][1].Text = "Declined nit c.go: already covered."
	reviewer := &scriptedConversationAdapter{
		runs: [][]harness.Event{
			{{Type: harness.EventSession, SessionID: "review-1"}, {Type: harness.EventAssistantText, Text: "VERDICT: revise\nSUMMARY: Fix required\nFINDINGS:\n- [blocking] a.go — block one\n- [nit] c.go — nit one\n"}},
			{{Type: harness.EventSession, SessionID: "review-2"}, {Type: harness.EventAssistantText, Text: "VERDICT: approve\nSUMMARY: Ready\nFINDINGS:\n"}},
		},
		resumes:           [][]harness.Event{{}},
		resumeStartErrors: []error{&scriptedConversationError{message: "session unavailable"}},
	}
	gateRunner := &scriptedGateRunner{t: t, recorder: recorder, workRoot: repo, branchPoint: branchPoint, results: []GateExecution{{Output: qualitySummary("PASS")}, {Output: qualitySummary("PASS")}}}
	_, final, _, err := runImplementIterations(context.Background(), implementIterationsParams{git: gitadapter.ExecGitRunner{Dir: repo}, workRoot: repo, implementer: implementer, reviewer: reviewer, projectConfig: gateTestConfig(2), ticket: tracker.Ticket{Number: 278}, branchPoint: branchPoint, recorder: recorder, gateRunner: gateRunner, output: io.Discard, allowEmptyDiff: true})
	if err != nil || final.Status != verdict.Approve {
		t.Fatalf("runImplementIterations() = (%q, %v), want approve", final.Status, err)
	}
	if len(reviewer.runRequests) != 2 || len(reviewer.resumeCalls) != 1 {
		t.Fatalf("review requests = (%d fresh, %d resumed), want fallback after resume", len(reviewer.runRequests), len(reviewer.resumeCalls))
	}
	assertContainsAll(t, reviewer.runRequests[1].Prompt, "[blocking] a.go", "block one", "[nit] c.go", "nit one", filepath.Join(recorder.dir, "iteration-02-implement-turn-01.feed"), "verify each carried-over finding")
	if strings.TrimSpace(recorder.files["iteration-02-implement-turn-01.feed"]) != "Declined nit c.go: already covered." {
		t.Fatal("missing decline reason from recorded feed")
	}
}

func TestNitBudgetControlsApproval(t *testing.T) {
	for _, test := range []struct {
		name                         string
		count, budget, maxIterations int
		override, exhausted          bool
	}{
		{"over budget", 6, 5, 2, true, false},
		{"equal budget", 5, 5, 2, false, false},
		{"zero budget", 1, 0, 2, true, false},
		{"exhausted", 6, 5, 1, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, branchPoint := newGateTestRepo(t, "#!/bin/sh\necho gate\n", true)
			recorder := newMemoryRunRecorder()
			recorder.dir = t.TempDir()
			implementer := gateImplementer(test.maxIterations)
			var findings strings.Builder
			for i := 1; i <= test.count; i++ {
				fmt.Fprintf(&findings, "- [nit] file%d.go — nit number %d\n", i, i)
			}
			first := "VERDICT: approve\nSUMMARY: Ready\nFINDINGS:\n" + findings.String()
			reviewer := &scriptedConversationAdapter{runs: [][]harness.Event{{{Type: harness.EventSession, SessionID: "review-1"}, {Type: harness.EventAssistantText, Text: first}}}, resumes: [][]harness.Event{{{Type: harness.EventSession, SessionID: "review-1"}, {Type: harness.EventAssistantText, Text: "VERDICT: approve\nSUMMARY: Ready\nFINDINGS:\n"}}}}
			results := []GateExecution{{Output: qualitySummary("PASS")}, {Output: qualitySummary("PASS")}}
			gateRunner := &scriptedGateRunner{t: t, recorder: recorder, workRoot: repo, branchPoint: branchPoint, results: results}
			state := newRunStateTracker(recorder, runrecord.New(runrecord.Implement, "#280", 0, test.maxIterations, testNow()))
			options, run := gateExecuteOptions(repo, branchPoint, recorder, implementer, reviewer, gateRunner, test.maxIterations, 3, state)
			options.ProjectConfig.Loop.NitBudget = test.budget
			publisher := &recordingPullRequestTracker{}
			options.IssueTracker = publisher
			options.PublishOnApprove = test.exhausted
			var output strings.Builder
			options.Output = &output
			err := executeImplementRun(context.Background(), options, run)
			if (err != nil) != test.exhausted {
				t.Fatalf("error = %v, exhausted = %v", err, test.exhausted)
			}
			if (len(state.state.NitBudgetOverrides) > 0) != test.override {
				t.Fatalf("overrides = %#v", state.state.NitBudgetOverrides)
			}
			if test.override {
				override := state.state.NitBudgetOverrides[0]
				if override.Iteration != 1 || override.NitCount != test.count || override.Budget != test.budget {
					t.Fatalf("override = %#v", override)
				}
				expected := fmt.Sprintf("Nit budget override: iteration 1, %d nits (budget %d)", test.count, test.budget)
				assertContainsAll(t, recorder.files["summary.txt"], expected)
				assertContainsAll(t, output.String(), expected)
				assertContainsAll(t, recorder.files["iteration-01-verdict.txt"], "VERDICT: approve", "nit number 1")
				if !test.exhausted {
					for i := 1; i <= test.count; i++ {
						assertContainsAll(t, implementer.resumeCalls[0].prompt, fmt.Sprintf("nit number %d", i))
					}
				}
			}
			if test.exhausted && (publisher.request != nil || strings.Contains(recorder.files["summary.txt"], "Publication:")) {
				t.Fatalf("exhausted run attempted publication: request = %#v, summary = %q", publisher.request, recorder.files["summary.txt"])
			}
			if test.exhausted && (state.state.Status != runrecord.Exhausted || !strings.Contains(state.state.ExhaustionReason, "Nit budget")) {
				t.Fatalf("state = %#v", state.state)
			}
			if !test.exhausted && state.state.Status != runrecord.Approved {
				t.Fatalf("state = %#v", state.state)
			}
		})
	}
}
