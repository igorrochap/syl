package codex

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/orchestration"
	"github.com/igorrochap/syl/internal/verdict"
)

func TestRunInvokesCodexWithModelEffortAndComposedSkillPrompt(t *testing.T) {
	root := t.TempDir()
	argsPath := filepath.Join(root, "args")
	command := fakeCodexCommand(t, argsPath, []string{
		`{"type":"thread.started","thread_id":"codex-session"}`,
		`{"type":"item.completed","item":{"id":"item-1","type":"agent_message","text":"implemented"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1}}`,
	})
	adapter := &Adapter{command: command, projectRoot: root}

	stream, err := adapter.Run(context.Background(), harness.Request{
		Model:  "gpt-5.6-luna",
		Effort: config.EffortXHigh,
		Prompt: "/implement\n\nTicket: #8",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	events := collectEvents(stream)
	if err := stream.Wait(); err != nil {
		t.Fatalf("stream.Wait() error = %v", err)
	}

	wantEvents := []harness.Event{
		{Type: harness.EventSession, SessionID: "codex-session"},
		{Type: harness.EventAssistantText, Text: "implemented"},
		{Type: harness.EventResult},
	}
	if len(events) != len(wantEvents) {
		t.Fatalf("events = %#v, want %d events", events, len(wantEvents))
	}
	for i := range wantEvents {
		wantEvents[i].Raw = events[i].Raw
	}
	for i := range wantEvents {
		if events[i].Type != wantEvents[i].Type || events[i].SessionID != wantEvents[i].SessionID || events[i].Text != wantEvents[i].Text {
			t.Fatalf("event %d = %#v, want %#v", i, events[i], wantEvents[i])
		}
	}

	gotArgs := strings.Split(strings.TrimSpace(readFile(t, argsPath)), "\n")
	for i := range gotArgs {
		gotArgs[i] = strings.ReplaceAll(gotArgs[i], "\x1c", "\n")
	}
	wantArgs := []string{
		"exec",
		"--json",
		"--sandbox", "danger-full-access",
		"--model", "gpt-5.6-luna",
		"--config", `model_reasoning_effort="xhigh"`,
		"--cd", root,
		"$implement\n\nTicket: #8",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("Codex args = %#v, want %#v", gotArgs, wantArgs)
	}
}

func TestCodexEffortMapping(t *testing.T) {
	tests := []struct {
		effort config.Effort
		want   string
	}{
		{effort: config.EffortLow, want: "low"},
		{effort: config.EffortMedium, want: "medium"},
		{effort: config.EffortHigh, want: "high"},
		{effort: config.EffortXHigh, want: "xhigh"},
	}
	for _, tt := range tests {
		t.Run(string(tt.effort), func(t *testing.T) {
			got, err := codexEffortSetting(tt.effort)
			if err != nil {
				t.Fatalf("codexEffortSetting() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("codexEffortSetting() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestComposePromptUsesCodexSkillSyntax(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   string
	}{
		{name: "multiline slash skill", prompt: "/implement\n\nTicket: #8", want: "$implement\n\nTicket: #8"},
		{name: "single line slash skill", prompt: "/plan", want: "$plan"},
		{name: "already native", prompt: "$review\n\nReview the diff.", want: "$review\n\nReview the diff."},
		{name: "plain prompt", prompt: "Review the diff.", want: "Review the diff."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := composePrompt(tt.prompt); got != tt.want {
				t.Fatalf("composePrompt() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDecodeCodexStreamMapsSessionAssistantToolAndTerminalEvents(t *testing.T) {
	decoder := &codexDecoder{}
	lines := []string{
		`{"type":"thread.started","thread_id":"session-1"}`,
		`{"type":"item.started","item":{"id":"command-1","type":"command_execution","command":"git diff --stat"}}`,
		`{"type":"item.completed","item":{"id":"message-1","type":"agent_message","text":"Reviewed the diff."}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":3}}`,
	}
	var events []harness.Event
	for _, line := range lines {
		events = append(events, decoder.decodeLine(line+"\n")...)
	}

	if len(events) != 4 {
		t.Fatalf("decoded events = %#v, want four events", events)
	}
	if events[0].Type != harness.EventSession || events[0].SessionID != "session-1" {
		t.Fatalf("session event = %#v", events[0])
	}
	if events[1].Type != harness.EventToolUse || events[1].ToolName != "Bash" || events[1].ArgumentGist != "git diff --stat" {
		t.Fatalf("tool event = %#v", events[1])
	}
	if events[2].Type != harness.EventAssistantText || events[2].Text != "Reviewed the diff." {
		t.Fatalf("assistant event = %#v", events[2])
	}
	if events[3].Type != harness.EventResult || events[3].IsError {
		t.Fatalf("result event = %#v", events[3])
	}
}

func TestCodexResumeKeepsSessionModelEffortAndComposedSkillPrompt(t *testing.T) {
	root := t.TempDir()
	argsPath := filepath.Join(root, "args")
	command := fakeCodexCommand(t, argsPath, []string{
		`{"type":"thread.started","thread_id":"codex-session"}`,
		`{"type":"item.completed","item":{"id":"item-1","type":"agent_message","text":"continued"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1}}`,
	})
	adapter := &Adapter{command: command, projectRoot: root}

	stream, err := adapter.Resume(context.Background(), "codex-session", harness.Request{
		Model:  "gpt-5.6-luna",
		Effort: config.EffortXHigh,
		Prompt: "/fix-review\n\nAddress the blocking findings.",
	})
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	_ = collectEvents(stream)
	if err := stream.Wait(); err != nil {
		t.Fatalf("stream.Wait() error = %v", err)
	}

	gotArgs := strings.Split(strings.TrimSpace(readFile(t, argsPath)), "\n")
	for i := range gotArgs {
		gotArgs[i] = strings.ReplaceAll(gotArgs[i], "\x1c", "\n")
	}
	wantArgs := []string{
		"exec",
		"--cd", root,
		"resume",
		"--json",
		"--config", `sandbox_mode="danger-full-access"`,
		"--model", "gpt-5.6-luna",
		"--config", `model_reasoning_effort="xhigh"`,
		"codex-session",
		"$fix-review\n\nAddress the blocking findings.",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("Codex resume args = %#v, want %#v", gotArgs, wantArgs)
	}
}

func TestCodexReviewEventsProduceVerdictWithoutSpecialParsing(t *testing.T) {
	root := t.TempDir()
	command := fakeCodexCommand(t, filepath.Join(root, "args"), []string{
		`{"type":"thread.started","thread_id":"review-session"}`,
		`{"type":"item.completed","item":{"id":"item-1","type":"agent_message","text":"VERDICT: approve\nSUMMARY: The implementation meets the ticket.\nFINDINGS:\n"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1}}`,
	})
	adapter := &Adapter{command: command, projectRoot: root}

	review, err := orchestration.RunReviewExecution(context.Background(), adapter, harness.Request{
		Model:  "sonnet-5",
		Effort: config.EffortMedium,
		Prompt: "/code-review\n\nReview the diff.",
	}, io.Discard, orchestration.ParsedHarnessOutput, nil)
	if err != nil {
		t.Fatalf("RunReviewExecution() error = %v", err)
	}
	if review.Verdict.Status != verdict.Approve || review.Verdict.Summary != "The implementation meets the ticket." {
		t.Fatalf("review verdict = %#v, want approve verdict", review.Verdict)
	}
}

func TestCodexErrorItemIsAWarningThatDoesNotEndTheConversation(t *testing.T) {
	root := t.TempDir()
	command := fakeCodexCommand(t, filepath.Join(root, "args"), []string{
		`{"type":"thread.started","thread_id":"review-session"}`,
		"{\"type\":\"item.completed\",\"item\":{\"id\":\"item_0\",\"type\":\"error\",\"message\":\"This session was recorded with model `gpt-5.6-luna` but is resuming with `gpt-5.6-sol`.\"}}",
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"VERDICT: approve\nSUMMARY: Ready.\nFINDINGS:\n"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1}}`,
	})
	adapter := &Adapter{command: command, projectRoot: root}

	review, err := orchestration.RunReviewExecution(context.Background(), adapter, harness.Request{
		Model:  "gpt-5.6-luna",
		Effort: config.EffortMedium,
		Prompt: "/code-review\n\nReview the diff.",
	}, io.Discard, orchestration.ParsedHarnessOutput, nil)
	if err != nil {
		t.Fatalf("RunReviewExecution() error = %v", err)
	}
	if review.Verdict.Status != verdict.Approve {
		t.Fatalf("review verdict = %#v, want approve despite the warning item", review.Verdict)
	}
}

func TestCodexQuestionResumeUsesSameSessionAndAnswerThroughConversation(t *testing.T) {
	root := t.TempDir()
	argsPath := filepath.Join(root, "args")
	command := fakeCodexQuestionResumeCommand(t, argsPath)
	adapter := &Adapter{command: command, projectRoot: root}
	var questionOutput strings.Builder
	questions := orchestration.NewQuestionHandler(strings.NewReader("SQLite\n\n"), &questionOutput, "#8", nil, nil)

	review, err := orchestration.RunReviewExecution(context.Background(), adapter, harness.Request{
		Model:  "gpt-5.6-luna",
		Effort: config.EffortXHigh,
		Prompt: "/code-review\n\nReview the diff.",
	}, io.Discard, orchestration.ParsedHarnessOutput, questions)
	if err != nil {
		t.Fatalf("RunReviewExecution() error = %v", err)
	}
	if review.Verdict.Status != verdict.Approve {
		t.Fatalf("review verdict = %#v, want approve after resume", review.Verdict)
	}
	if len(review.SessionIDs) < 2 || review.SessionIDs[0] != "codex-session" || review.SessionIDs[1] != "codex-session" {
		t.Fatalf("session ids = %#v, want the same Codex session across turns", review.SessionIDs)
	}
	if !strings.Contains(questionOutput.String(), "Which database?") {
		t.Fatalf("question output = %q, want the Codex question", questionOutput.String())
	}

	gotArgs := strings.Split(strings.TrimSpace(readFile(t, argsPath)), "\n")
	for i := range gotArgs {
		gotArgs[i] = strings.ReplaceAll(gotArgs[i], "\x1c", "\n")
	}
	if !containsArgs(gotArgs, "resume") || !containsArgs(gotArgs, "codex-session") || !containsArgs(gotArgs, "SQLite") {
		t.Fatalf("resume args = %#v, want session id and answer", gotArgs)
	}
}

func TestCodexAttachInvokesInteractivePrompt(t *testing.T) {
	root := t.TempDir()
	argsPath := filepath.Join(root, "args")
	command := fakeCodexCommand(t, argsPath, nil)
	adapter := &Adapter{command: command, projectRoot: root}

	if err := adapter.Attach(context.Background(), harness.Request{
		Model:  "gpt-5.6-luna",
		Effort: config.EffortHigh,
		Prompt: "/plan\n\nPlan the ticket.",
	}); err != nil {
		t.Fatalf("Attach() error = %v", err)
	}

	gotArgs := strings.Split(strings.TrimSpace(readFile(t, argsPath)), "\n")
	for i := range gotArgs {
		gotArgs[i] = strings.ReplaceAll(gotArgs[i], "\x1c", "\n")
	}
	wantArgs := []string{
		"--model", "gpt-5.6-luna",
		"--config", `model_reasoning_effort="high"`,
		"--sandbox", "danger-full-access",
		"--cd", root,
		"$plan\n\nPlan the ticket.",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("Codex attach args = %#v, want %#v", gotArgs, wantArgs)
	}
}

func TestCodexAttachSessionInvokesResumeWithRequestSandboxMode(t *testing.T) {
	for _, mode := range []struct {
		name        string
		sandboxMode config.SandboxMode
		wantSandbox string
	}{
		{name: "omitted sandbox defaults to full access", wantSandbox: "danger-full-access"},
		{name: "full access", sandboxMode: config.SandboxModeFullAccess, wantSandbox: "danger-full-access"},
		{name: "workspace write", sandboxMode: config.SandboxModeWorkspaceWrite, wantSandbox: "workspace-write"},
		{name: "read only", sandboxMode: config.SandboxModeReadOnly, wantSandbox: "read-only"},
	} {
		t.Run(mode.name, func(t *testing.T) {
			root := t.TempDir()
			argsPath := filepath.Join(root, "args")
			command := fakeCodexCommand(t, argsPath, nil)
			adapter := &Adapter{command: command, projectRoot: root}

			err := adapter.AttachSession(context.Background(), "codex-session", harness.Request{
				Model:       "ignored-model",
				Effort:      config.Effort("ignored-effort"),
				MCP:         true,
				SandboxMode: mode.sandboxMode,
			})
			if err != nil {
				t.Fatalf("AttachSession() error = %v", err)
			}

			gotArgs := strings.Split(strings.TrimSpace(readFile(t, argsPath)), "\n")
			for i := range gotArgs {
				gotArgs[i] = strings.ReplaceAll(gotArgs[i], "\x1c", "\n")
			}
			wantArgs := []string{"resume", "codex-session", "--sandbox", mode.wantSandbox, "--cd", root}
			if !reflect.DeepEqual(gotArgs, wantArgs) {
				t.Fatalf("Codex attach session args = %#v, want %#v", gotArgs, wantArgs)
			}
		})
	}
}

func TestCodexAttachSessionRejectsBlankSessionID(t *testing.T) {
	adapter := &Adapter{command: "does-not-run"}
	for _, sessionID := range []string{"", " \t\n"} {
		t.Run(strconv.Quote(sessionID), func(t *testing.T) {
			err := adapter.AttachSession(context.Background(), sessionID, harness.Request{})
			if err == nil || !strings.Contains(err.Error(), "Codex") {
				t.Fatalf("AttachSession() error = %v, want Codex session error", err)
			}
		})
	}
}

func TestCodexAttachSessionUsesProjectRootAndReportsExitFailure(t *testing.T) {
	root := t.TempDir()
	workingDirectoryPath := filepath.Join(root, "working-directory")
	command := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\npwd > \"" + workingDirectoryPath + "\"\nexit 7\n"
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	adapter := &Adapter{command: command, projectRoot: root}

	err := adapter.AttachSession(context.Background(), "codex-session", harness.Request{})
	if err == nil || !strings.Contains(err.Error(), "Codex") {
		t.Fatalf("AttachSession() error = %v, want Codex exit error", err)
	}
	workingDirectory := strings.TrimSpace(readFile(t, workingDirectoryPath))
	if workingDirectory != root {
		t.Fatalf("Codex attach session working directory = %q, want %q", workingDirectory, root)
	}
}

func collectEvents(stream harness.Stream) []harness.Event {
	var events []harness.Event
	for event := range stream.Events() {
		events = append(events, event)
	}
	return events
}

func fakeCodexCommand(t *testing.T, argsPath string, output []string) string {
	t.Helper()
	command := filepath.Join(t.TempDir(), "codex")
	var script strings.Builder
	script.WriteString("#!/bin/sh\n")
	script.WriteString("for arg in \"$@\"; do\n")
	script.WriteString("printf '%s' \"$arg\" | tr '\\n' '\\034'\n")
	script.WriteString("printf '\\n'\ndone > ")
	script.WriteString(shellQuote(argsPath))
	script.WriteByte('\n')
	for _, line := range output {
		script.WriteString("printf '%s\\n' ")
		script.WriteString(shellQuote(line))
		script.WriteByte('\n')
	}
	if err := os.WriteFile(command, []byte(script.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return command
}

func fakeCodexQuestionResumeCommand(t *testing.T, argsPath string) string {
	t.Helper()
	command := filepath.Join(t.TempDir(), "codex")
	var script strings.Builder
	script.WriteString("#!/bin/sh\n")
	script.WriteString("for arg in \"$@\"; do\n")
	script.WriteString("printf '%s' \"$arg\" | tr '\\n' '\\034'\n")
	script.WriteString("printf '\\n'\ndone > ")
	script.WriteString(shellQuote(argsPath))
	script.WriteString("\ncase \" $* \" in *' resume '*)\n")
	for _, line := range []string{
		`{"type":"thread.started","thread_id":"codex-session"}`,
		`{"type":"item.completed","item":{"id":"item-2","type":"agent_message","text":"VERDICT: approve\nSUMMARY: Resumed successfully.\nFINDINGS:\n"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1}}`,
	} {
		script.WriteString("printf '%s\\n' ")
		script.WriteString(shellQuote(line))
		script.WriteByte('\n')
	}
	script.WriteString(";;\n*)\n")
	for _, line := range []string{
		`{"type":"thread.started","thread_id":"codex-session"}`,
		`{"type":"item.completed","item":{"id":"item-1","type":"agent_message","text":"Before.\nQUESTION:\nWhich database?\nEND QUESTION"}}`,
	} {
		script.WriteString("printf '%s\\n' ")
		script.WriteString(shellQuote(line))
		script.WriteByte('\n')
	}
	script.WriteString(";;\nesac\n")
	if err := os.WriteFile(command, []byte(script.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return command
}

func containsArgs(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func TestCodexSessionSandboxModes(t *testing.T) {
	for _, operation := range []string{"Run", "Resume", "Attach"} {
		for _, mode := range []struct {
			sandboxMode config.SandboxMode
			want        string
		}{
			{config.SandboxModeFullAccess, "danger-full-access"},
			{config.SandboxModeWorkspaceWrite, "workspace-write"},
			{config.SandboxModeReadOnly, "read-only"},
		} {
			t.Run(operation+"/"+string(mode.sandboxMode), func(t *testing.T) {
				root := t.TempDir()
				path := filepath.Join(root, "args")
				adapter := &Adapter{command: fakeCodexCommand(t, path, nil), projectRoot: root}
				request := harness.Request{Model: "gpt-5.6-luna", Effort: config.EffortHigh, Prompt: "noop", SandboxMode: mode.sandboxMode}
				invokeCodexSession(t, adapter, operation, request)
				args := strings.Split(strings.TrimSpace(readFile(t, path)), "\n")
				flag, value := "--sandbox", mode.want
				if operation == "Resume" {
					flag, value = "--config", `sandbox_mode="`+mode.want+`"`
				}
				for index := 0; index+1 < len(args); index++ {
					if args[index] == flag && args[index+1] == value {
						return
					}
				}
				t.Fatalf("%s args = %v, want %s %s", operation, args, flag, value)
			})
		}
	}
}

func invokeCodexSession(t *testing.T, adapter *Adapter, operation string, request harness.Request) {
	t.Helper()
	if operation == "Attach" {
		if err := adapter.Attach(context.Background(), request); err != nil {
			t.Fatal(err)
		}
		return
	}
	var stream harness.Stream
	var err error
	if operation == "Resume" {
		stream, err = adapter.Resume(context.Background(), "session-1", request)
	} else {
		stream, err = adapter.Run(context.Background(), request)
	}
	if err != nil {
		t.Fatal(err)
	}
	_ = collectEvents(stream)
	if err := stream.Wait(); err != nil {
		t.Fatal(err)
	}
}
