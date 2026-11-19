package orchestration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/igorrochap/syl/internal/runrecord"
)

// GateExecution contains the process result of one branch-point Gate run.
type GateExecution struct {
	ExitStatus int
	Output     string
}

// GateRunner runs an extracted Gate script in the Work root.
type GateRunner interface {
	Run(context.Context, string, string, string) GateExecution
}

// CommandGateRunner executes the script directly so its recorded executable
// bit and interpreter determine whether it can start.
type CommandGateRunner struct{}

var _ GateRunner = CommandGateRunner{}

// Run executes the Gate script in the Work root and captures its output.
func (CommandGateRunner) Run(ctx context.Context, scriptPath, workRoot, branchPoint string) GateExecution {
	command := exec.CommandContext(ctx, scriptPath)
	command.Dir = workRoot
	command.Env = environmentWithValue(os.Environ(), "QUALITY_BASE_REF", branchPoint)
	output, err := command.CombinedOutput()
	if err == nil {
		return GateExecution{ExitStatus: 0, Output: string(output)}
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return GateExecution{ExitStatus: exitError.ExitCode(), Output: string(output)}
	}
	return GateExecution{ExitStatus: -1, Output: appendGateStartError(output, err)}
}

func appendGateStartError(output []byte, err error) string {
	var contents bytes.Buffer
	contents.Write(output)
	if contents.Len() > 0 && contents.Bytes()[contents.Len()-1] != '\n' {
		contents.WriteByte('\n')
	}
	fmt.Fprintf(&contents, "Gate script could not start: %v\n", err)
	return contents.String()
}

func environmentWithValue(environment []string, name, value string) []string {
	updated := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		key, _, found := strings.Cut(entry, "=")
		if found && key == name {
			continue
		}
		updated = append(updated, entry)
	}
	return append(updated, name+"="+value)
}

type branchGateScript struct {
	path   string
	loaded bool
}

func extractBranchGateScript(ctx context.Context, git GitRunner, recorder RunRecorder, branchPoint string) (branchGateScript, error) {
	entry, err := git.Run(ctx, "ls-tree", branchPoint, "--", "scripts/quality.sh")
	if err != nil {
		return branchGateScript{}, fmt.Errorf("inspect branch-point Gate script: %w", err)
	}
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return branchGateScript{}, nil
	}
	fields := strings.Fields(entry)
	if len(fields) < 4 || fields[3] != "scripts/quality.sh" {
		return branchGateScript{}, fmt.Errorf("inspect branch-point Gate script: unexpected git tree entry %q", entry)
	}
	modeValue, err := strconv.ParseUint(fields[0], 8, 32)
	if err != nil {
		return branchGateScript{}, fmt.Errorf("inspect branch-point Gate script mode %q: %w", fields[0], err)
	}
	contents, err := git.Run(ctx, "show", branchPoint+":scripts/quality.sh")
	if err != nil {
		return branchGateScript{}, fmt.Errorf("extract branch-point Gate script: %w", err)
	}
	mode := os.FileMode(modeValue).Perm()
	path, err := recorder.RecordGateScript([]byte(contents), mode&0o111 != 0)
	if err != nil {
		return branchGateScript{}, fmt.Errorf("record branch-point Gate script: %w", err)
	}
	return branchGateScript{path: path, loaded: true}, nil
}

type parsedGateSummary struct {
	results []runrecord.GateResult
	valid   bool
}

var gateOrder = []string{"format", "vet", "style", "complexity", "coverage", "tests", "architecture"}

func parseGateSummary(output string) parsedGateSummary {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	headerIndex := -1
	headerCount := 0
	for index, line := range lines {
		if line == "QUALITY GATE" {
			headerIndex = index
			headerCount++
		}
	}
	if headerCount != 1 {
		return parsedGateSummary{}
	}
	results := make([]runrecord.GateResult, 0, len(gateOrder))
	for _, line := range lines[headerIndex+1:] {
		gate, result, ok := parseGateResultLine(line)
		if !ok {
			if looksLikeGateResultLine(line) {
				return parsedGateSummary{}
			}
			break
		}
		order := gateOrderIndex(gate)
		if order != len(results) {
			return parsedGateSummary{}
		}
		results = append(results, runrecord.GateResult{Name: gate, Result: result})
	}
	if len(results) != len(gateOrder) {
		return parsedGateSummary{}
	}
	return parsedGateSummary{results: results, valid: true}
}

func looksLikeGateResultLine(line string) bool {
	if !strings.HasPrefix(line, "  ") {
		return false
	}
	fields := strings.Fields(line)
	return len(fields) > 1 && gateOrderIndex(fields[0]) >= 0
}

func parseGateResultLine(line string) (string, string, bool) {
	if len(line) < 20 || !strings.HasPrefix(line, "  ") {
		return "", "", false
	}
	name := strings.TrimSpace(line[2:16])
	if name == "" || fmt.Sprintf("%-14s", name) != line[2:16] {
		return "", "", false
	}
	order := gateOrderIndex(name)
	if order < 0 {
		return "", "", false
	}
	resultField := strings.TrimSpace(line[16:])
	fields := strings.Fields(resultField)
	if len(fields) == 0 || (fields[0] != "PASS" && fields[0] != "FAIL" && fields[0] != "SKIP") {
		return "", "", false
	}
	return name, fields[0], true
}

func gateOrderIndex(name string) int {
	for index, candidate := range gateOrder {
		if name == candidate {
			return index
		}
	}
	return -1
}

func gateAttemptIsGreen(execution GateExecution, summary parsedGateSummary) bool {
	if execution.ExitStatus != 0 {
		return false
	}
	for _, result := range summary.results {
		if result.Result == "FAIL" {
			return false
		}
	}
	return true
}

type failedGateAttemptFeedback struct {
	resultsAvailable bool
	results          []runrecord.GateResult
	outputPath       string
	exitStatus       int
}

func formatGateAttemptFeedback(feedback failedGateAttemptFeedback) string {
	var prompt strings.Builder
	prompt.WriteString("The orchestrator ran the Gate script after your turn, and this Gate attempt was red. ")
	if feedback.resultsAvailable {
		failed := make([]string, 0)
		for _, gate := range feedback.results {
			if gate.Result == "FAIL" {
				failed = append(failed, gate.Name+" (FAIL)")
			}
		}
		if len(failed) > 0 {
			fmt.Fprintf(&prompt, "Failed Gates: %s. ", strings.Join(failed, ", "))
		} else {
			fmt.Fprintf(&prompt, "The Gate script exited with status %d even though the summary has no FAIL result. ", feedback.exitStatus)
		}
	} else {
		prompt.WriteString("Per-Gate results are unavailable because the output has no valid summary block. ")
	}
	fmt.Fprintf(&prompt, "Full Gate output: %s. Address the Gate failure and stop; the reviewer has not run for this Gate attempt.", feedback.outputPath)
	return prompt.String()
}

func recordGateAttempt(
	ctx context.Context,
	params implementIterationsParams,
	iteration, attemptNumber int,
	scriptPath string,
) (failedGateAttemptFeedback, string, bool, error) {
	runner := params.gateRunner
	if runner == nil {
		runner = CommandGateRunner{}
	}
	result := runner.Run(ctx, scriptPath, params.workRoot, params.branchPoint)
	summary := parseGateSummary(result.Output)
	green := gateAttemptIsGreen(result, summary)
	paths, err := params.recorder.RecordGateAttempt(iteration, runrecord.GateAttempt{
		Number: attemptNumber, ExitStatus: result.ExitStatus,
		PerGateResultsAvailable: summary.valid, Results: summary.results,
		Green: green, Output: result.Output,
	})
	if err != nil {
		return failedGateAttemptFeedback{}, "", false, fmt.Errorf("record Gate attempt: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return failedGateAttemptFeedback{}, paths.Result, green, err
	}
	feedback := failedGateAttemptFeedback{
		resultsAvailable: summary.valid, results: summary.results,
		outputPath: paths.Output, exitStatus: result.ExitStatus,
	}
	return feedback, paths.Result, green, nil
}

func gateScriptPath(script branchGateScript) string {
	if script.path == "" {
		return ""
	}
	return filepath.Clean(script.path)
}
