package commitgen

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type fakeCommandExecutor struct {
	path        string
	lookPathErr error
	lookedUp    []string
	runPath     string
	runWorkRoot string
	runArgs     []string
	runOutput   string
	runErr      error
}

func (e *fakeCommandExecutor) LookPath(name string) (string, error) {
	e.lookedUp = append(e.lookedUp, name)
	return e.path, e.lookPathErr
}

func (e *fakeCommandExecutor) Run(_ context.Context, executable, workRoot string, args ...string) (string, error) {
	e.runPath = executable
	e.runWorkRoot = workRoot
	e.runArgs = append([]string(nil), args...)
	return e.runOutput, e.runErr
}

func TestRunnerChecksAvailabilityAndRunsInWorkRoot(t *testing.T) {
	executor := &fakeCommandExecutor{path: "/tools/commitgen", runOutput: "generated output"}
	runner := Runner{executor: executor}

	if !runner.Available() {
		t.Fatal("Available() = false, want true")
	}
	output, err := runner.Run(context.Background(), "/work/project", "pr", "--agent", "--base", "main")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if output != "generated output" {
		t.Fatalf("Run() output = %q, want generated output", output)
	}
	if !reflect.DeepEqual(executor.lookedUp, []string{"commitgen", "commitgen"}) {
		t.Fatalf("LookPath calls = %v, want commitgen for availability and execution", executor.lookedUp)
	}
	if executor.runPath != "/tools/commitgen" || executor.runWorkRoot != "/work/project" ||
		!reflect.DeepEqual(executor.runArgs, []string{"pr", "--agent", "--base", "main"}) {
		t.Fatalf("Run invocation = %q %q %v", executor.runPath, executor.runWorkRoot, executor.runArgs)
	}
}

func TestRunnerReportsUnavailableAndExecutionFailures(t *testing.T) {
	lookupFailure := &fakeCommandExecutor{lookPathErr: errors.New("not found")}
	if (Runner{executor: lookupFailure}).Available() {
		t.Fatal("Available() = true, want false when command lookup fails")
	}
	if _, err := (Runner{executor: lookupFailure}).Run(context.Background(), "/work/project", "--agent"); err == nil {
		t.Fatal("Run() error = nil, want command lookup failure")
	}

	runFailure := errors.New("command failed")
	failed := &fakeCommandExecutor{path: "/tools/commitgen", runErr: runFailure}
	if _, err := (Runner{executor: failed}).Run(context.Background(), "/work/project", "--agent"); !errors.Is(err, runFailure) {
		t.Fatalf("Run() error = %v, want wrapped command error", err)
	}
}
