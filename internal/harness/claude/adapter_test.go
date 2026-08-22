package claude

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/harness/claude/transcript"
	"github.com/igorrochap/syl/internal/orchestration"
	"github.com/igorrochap/syl/internal/verdict"
)

func TestClaudeAttachInvokesInteractivePrompt(t *testing.T) {
	root := t.TempDir()
	argsPath := filepath.Join(root, "args")
	command := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + argsPath + "\"\n"
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	adapter := &Adapter{command: command, projectRoot: root}

	err := adapter.Attach(context.Background(), harness.Request{
		Model:  "claude-opus-5",
		Effort: config.EffortHigh,
		Prompt: "/to-tickets\n\nTopic: offline mode",
		MCP:    false,
	})
	if err != nil {
		t.Fatalf("Attach() error = %v", err)
	}

	contents, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	gotArgs := strings.Split(strings.TrimSpace(string(contents)), "\n")
	wantArgs := []string{
		"--strict-mcp-config",
		"--model", "claude-opus-5",
		"--effort", "high",
		"/to-tickets",
		"",
		"Topic: offline mode",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("Claude attach args = %#v, want %#v", gotArgs, wantArgs)
	}
}

func TestClaudeEffortMapping(t *testing.T) {
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
			got, err := claudeEffortFlag(tt.effort)
			if err != nil {
				t.Fatalf("claudeEffortFlag() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("claudeEffortFlag() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRunArgsGrantAutonomousPermissions guards against the headless-permission
// regression: without bypassPermissions Claude Code denies every tool call, so
// the implementer cannot write files and the reviewer cannot run git.
func TestRunArgsGrantAutonomousPermissions(t *testing.T) {
	args, err := runArgs(harness.Request{Model: "claude-sonnet-5", Effort: config.EffortMedium, Prompt: "do it"})
	if err != nil {
		t.Fatalf("runArgs() error = %v", err)
	}
	if !containsFlagValue(args, "--permission-mode", "bypassPermissions") {
		t.Fatalf("runArgs() = %v, want --permission-mode bypassPermissions", args)
	}
}

func TestClaudeMCPConfigurationIsAppliedToRunAndResume(t *testing.T) {
	for _, tt := range []struct {
		name string
		mcp  bool
		want bool
	}{
		{name: "inherit MCP", mcp: true, want: false},
		{name: "strip MCP", mcp: false, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			request := harness.Request{
				Model:  "claude-sonnet-5",
				Effort: config.EffortMedium,
				Prompt: "do it",
				MCP:    tt.mcp,
			}
			run, err := runArgs(request)
			if err != nil {
				t.Fatalf("runArgs() error = %v", err)
			}
			resume, err := resumeArgs("session-1", request)
			if err != nil {
				t.Fatalf("resumeArgs() error = %v", err)
			}

			if got := containsArg(run, "--strict-mcp-config"); got != tt.want {
				t.Fatalf("runArgs() strict MCP flag = %t, want %t: %v", got, tt.want, run)
			}
			if got := containsArg(resume, "--strict-mcp-config"); got != tt.want {
				t.Fatalf("resumeArgs() strict MCP flag = %t, want %t: %v", got, tt.want, resume)
			}
		})
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func containsFlagValue(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestDecodeClaudeStreamMapsStructuredEvents(t *testing.T) {
	line := `{"type":"assistant","session_id":"session-1","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git diff --stat"}}]}}`
	events := decodeLine(line + "\n")
	if len(events) != 2 {
		t.Fatalf("decodeLine() returned %d events, want session and tool event: %#v", len(events), events)
	}
	if events[0].Type != harness.EventSession || events[0].SessionID != "session-1" {
		t.Fatalf("session event = %#v", events[0])
	}
	if events[1].Type != harness.EventToolUse || events[1].ToolName != "Bash" || events[1].ArgumentGist != `{"command":"git diff --stat"}` {
		t.Fatalf("tool event = %#v", events[1])
	}
	if events[0].Raw == "" || events[1].Raw != "" {
		t.Fatalf("raw output should be attached once, got first=%q second=%q", events[0].Raw, events[1].Raw)
	}
}

func TestPTYRunReadsLatestCompletedTurnAndStopsClaude(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	argsPath := filepath.Join(t.TempDir(), "args")
	stoppedPath := filepath.Join(t.TempDir(), "stopped")
	transcriptDir := claudeTranscriptDir(t, home, root)
	command := writeClaudeTestDouble(t, fmt.Sprintf(`
printf '%%s\n' "$@" > %q
session_id=''
while [ "$#" -gt 0 ]; do
	if [ "$1" = '--session-id' ]; then
		session_id="$2"
		break
	fi
	shift
done
transcript=%q/"$session_id".jsonl
mkdir -p %q
trap 'printf stopped > %q; exit 0' TERM
printf '%%s\n' '{"type":"assistant","message":{"role":"assistant","stop_reason":"tool_use","content":[{"type":"text","text":"VERDICT: approve\\nSUMMARY: planning only\\nFINDINGS:\\n"}]}}' >> "$transcript"
printf '\033]9;Claude is waiting for your input\007'
sleep 0.1
printf '%%s\n' '{"type":"user","message":{"role":"user","content":"continue"}}' >> "$transcript"
printf '%%s\n' '{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"VERDICT: revise\\nSUMMARY: final verdict\\nFINDINGS:\\n- [blocking] file.go:1 — fix it\\n"}]}}' >> "$transcript"
printf '\033]9;Claude is waiting for your input\007'
while :; do sleep 0.05; done
`, argsPath, transcriptDir, transcriptDir, stoppedPath))
	adapter := newPTYTestAdapter(command, root, home)

	stream, err := adapter.Run(context.Background(), harness.Request{
		Model:  "claude-sonnet-5",
		Effort: config.EffortMedium,
		Prompt: "review it",
		MCP:    false,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	var events []harness.Event
	for event := range stream.Events() {
		events = append(events, event)
	}
	if err := stream.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}

	var assistantText strings.Builder
	var sessionID string
	for _, event := range events {
		if event.Type == harness.EventSession {
			sessionID = event.SessionID
		}
		if event.Type == harness.EventAssistantText {
			assistantText.WriteString(event.Text)
		}
	}
	if sessionID == "" {
		t.Fatal("PTY stream emitted no generated session id")
	}
	if !strings.Contains(assistantText.String(), "SUMMARY: final verdict") {
		t.Fatalf("assistant text = %q, want latest completed turn", assistantText.String())
	}
	if _, err := os.Stat(stoppedPath); err != nil {
		t.Fatalf("Claude test double was not stopped with SIGTERM: %v", err)
	}

	contents, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(contents)), "\n")
	if containsArg(args, "--print") || containsArg(args, "--output-format") ||
		containsArg(args, "--include-partial-messages") {
		t.Fatalf("PTY Claude args contain headless flags: %v", args)
	}
	if !containsFlagValue(args, "--session-id", sessionID) ||
		!containsFlagValue(args, "--permission-mode", "bypassPermissions") ||
		!containsFlagValue(args, "--model", "claude-sonnet-5") ||
		!containsFlagValue(args, "--effort", "medium") ||
		!containsArg(args, "--strict-mcp-config") || args[len(args)-1] != "review it" {
		t.Fatalf("PTY Claude args = %v, want session, role flags, and final prompt", args)
	}
}

func TestPTYResumeContinuesSameSessionTranscript(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	argsPath := filepath.Join(t.TempDir(), "args")
	transcriptDir := claudeTranscriptDir(t, home, root)
	command := writeClaudeTestDouble(t, fmt.Sprintf(`
printf '%%s\n' '---' "$@" >> %q
mode=run
session_id=''
while [ "$#" -gt 0 ]; do
	case "$1" in
		--session-id) session_id="$2"; shift 2 ;;
		--resume) mode=resume; session_id="$2"; shift 2 ;;
		*) shift ;;
	esac
done
transcript=%q/"$session_id".jsonl
mkdir -p %q
trap 'exit 0' TERM
if [ "$mode" = resume ]; then
	sleep 0.5
	printf '%%s\n' '{"type":"user","message":{"role":"user","content":"review again"}}' >> "$transcript"
	printf '%%s\n' '{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"VERDICT: revise\\nSUMMARY: resumed review\\nFINDINGS:\\n- [blocking] file.go:1 — fix it\\n"}]}}' >> "$transcript"
else
	printf '%%s\n' '{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"VERDICT: approve\\nSUMMARY: initial review\\nFINDINGS:\\n"}]}}' >> "$transcript"
fi
printf '\033]9;Claude is waiting for your input\007'
while :; do sleep 0.05; done
`, argsPath, transcriptDir, transcriptDir))
	adapter := newPTYTestAdapter(command, root, home)
	request := harness.Request{
		Model: "claude-sonnet-5", Effort: config.EffortMedium, Prompt: "review it",
	}

	first, err := adapter.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	firstEvents := collectHarnessEvents(t, first)
	sessionID := eventSessionID(firstEvents)
	if sessionID == "" {
		t.Fatal("Run() emitted no session id")
	}

	request.Prompt = "review again"
	resumed, err := adapter.Resume(context.Background(), sessionID, request)
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	resumedEvents := collectHarnessEvents(t, resumed)
	if got := eventSessionID(resumedEvents); got != sessionID {
		t.Fatalf("resumed session id = %q, want %q", got, sessionID)
	}
	resumedText := eventAssistantText(resumedEvents)
	if !strings.Contains(resumedText, "SUMMARY: resumed review") ||
		strings.Contains(resumedText, "SUMMARY: initial review") {
		t.Fatalf("resumed assistant text = %q, want only the resumed turn", resumedText)
	}

	path, err := transcript.New(home).Find(root, sessionID)
	if err != nil {
		t.Fatalf("find transcript: %v", err)
	}
	entries, err := transcript.New(home).Read(path)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("same-session transcript entries = %d, want initial and resumed entries", len(entries))
	}
	contents, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	resumeInvocation := "---\n--resume\n" + sessionID + "\n"
	if !strings.Contains(string(contents), resumeInvocation) ||
		!strings.HasSuffix(string(contents), "review again\n") {
		t.Fatalf("Claude invocations = %q, want same-session resume with new prompt", contents)
	}
}

func TestPTYQuestionRelayResumesSameSessionWithAnswer(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	argsPath := filepath.Join(t.TempDir(), "args")
	transcriptDir := claudeTranscriptDir(t, home, root)
	command := writeClaudeTestDouble(t, fmt.Sprintf(`
printf '%%s\n' '---' "$@" >> %q
mode=run
prompt=''
session_id=''
for argument in "$@"; do prompt="$argument"; done
while [ "$#" -gt 0 ]; do
	case "$1" in
		--session-id) session_id="$2"; shift 2 ;;
		--resume) mode=resume; session_id="$2"; shift 2 ;;
		*) shift ;;
	esac
done
transcript=%q/"$session_id".jsonl
mkdir -p %q
trap 'exit 0' TERM
if [ "$mode" = resume ]; then
	if [ "$prompt" != 'Use SQLite.' ]; then exit 42; fi
	printf '%%s\n' '{"type":"user","message":{"role":"user","content":"Use SQLite."}}' >> "$transcript"
	printf '%%s\n' '{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"VERDICT: approve\nSUMMARY: answer received\nFINDINGS:\n"}]}}' >> "$transcript"
else
	printf '%%s\n' '{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"QUESTION:\nWhich data"},{"type":"text","text":"base?\nEND QUESTION"}]}}' >> "$transcript"
fi
printf '\033]9;Claude is waiting for your input\007'
while :; do sleep 0.05; done
`, argsPath, transcriptDir, transcriptDir))
	adapter := newPTYTestAdapter(command, root, home)
	var questionOutput strings.Builder
	questions := orchestration.NewQuestionHandler(
		strings.NewReader("Use SQLite.\n"),
		&questionOutput,
		"#71",
		nil,
	)

	review, err := orchestration.RunReviewExecution(
		context.Background(),
		adapter,
		harness.Request{
			Model: "claude-sonnet-5", Effort: config.EffortMedium, Prompt: "review it",
		},
		io.Discard,
		orchestration.ParsedHarnessOutput,
		questions,
	)
	if err != nil {
		t.Fatalf("RunReviewExecution() error = %v", err)
	}
	if review.Verdict.Status != verdict.Approve || review.Verdict.Summary != "answer received" {
		t.Fatalf("review verdict = %#v, want approval after the answer", review.Verdict)
	}
	if len(review.SessionIDs) < 2 || review.SessionIDs[0] != review.SessionIDs[1] {
		t.Fatalf("session ids = %#v, want the same session across the question relay", review.SessionIDs)
	}
	if !strings.Contains(questionOutput.String(), "Which database?") ||
		!strings.Contains(questionOutput.String(), "answer sent — resuming reviewer") {
		t.Fatalf("question output = %q, want question followed by resume confirmation", questionOutput.String())
	}

	contents, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	resumeInvocation := "---\n--resume\n" + review.SessionIDs[0] + "\n"
	if !strings.Contains(string(contents), resumeInvocation) ||
		!strings.HasSuffix(string(contents), "Use SQLite.\n") {
		t.Fatalf("Claude invocations = %q, want answer on the original session", contents)
	}
	path, err := transcript.New(home).Find(root, review.SessionIDs[0])
	if err != nil {
		t.Fatalf("find transcript: %v", err)
	}
	entries, err := transcript.New(home).Read(path)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("question relay transcript entries = %d, want question and resumed answer turn", len(entries))
	}
}

func TestPTYRunTranscriptEntriesWithoutToolCallsResetIdleTimeout(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	transcriptDir := claudeTranscriptDir(t, home, root)
	command := writeClaudeTestDouble(t, fmt.Sprintf(`
session_id=''
while [ "$#" -gt 0 ]; do
	if [ "$1" = '--session-id' ]; then
		session_id="$2"
		break
	fi
	shift
done
transcript=%q/"$session_id".jsonl
mkdir -p %q
trap 'exit 0' TERM
printf '%%s\n' '{"type":"user","message":{"role":"user","content":"started"}}' >> "$transcript"
sleep 0.4
printf '%%s\n' '{"type":"progress","message":{"role":"user","content":"subagent running"}}' >> "$transcript"
sleep 0.4
printf '%%s\n' '{"type":"progress","message":{"role":"user","content":"subagent still running"}}' >> "$transcript"
sleep 0.4
printf '%%s\n' '{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"VERDICT: approve\\nSUMMARY: complete\\nFINDINGS:\\n"}]}}' >> "$transcript"
printf '\033]9;Claude is waiting for your input\007'
while :; do sleep 0.05; done
`, transcriptDir, transcriptDir))
	adapter := newPTYTestAdapter(command, root, home)
	adapter.idleTimeout = 750 * time.Millisecond

	stream, err := adapter.Run(context.Background(), harness.Request{
		Model: "claude-sonnet-5", Effort: config.EffortMedium, Prompt: "review it",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for range stream.Events() {
	}
	if err := stream.Wait(); err != nil {
		t.Fatalf("Wait() error = %v; transcript entries without tool calls should keep the run alive", err)
	}
}

func TestPTYRunIdleTimeoutCompletesWithoutTerminalEscape(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	stoppedPath := filepath.Join(t.TempDir(), "stopped")
	transcriptDir := claudeTranscriptDir(t, home, root)
	command := writeClaudeTestDouble(t, fmt.Sprintf(`
session_id=''
while [ "$#" -gt 0 ]; do
	if [ "$1" = '--session-id' ]; then
		session_id="$2"
		break
	fi
	shift
done
transcript=%q/"$session_id".jsonl
mkdir -p %q
trap 'printf stopped > %q; exit 0' TERM
printf '%%s\n' '{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"VERDICT: approve\\nSUMMARY: complete without OSC\\nFINDINGS:\\n"}]}}' >> "$transcript"
while :; do sleep 0.05; done
`, transcriptDir, transcriptDir, stoppedPath))
	adapter := newPTYTestAdapter(command, root, home)
	adapter.idleTimeout = 750 * time.Millisecond

	stream, err := adapter.Run(context.Background(), harness.Request{
		Model: "claude-sonnet-5", Effort: config.EffortMedium, Prompt: "review it",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for range stream.Events() {
	}
	if err := stream.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if _, err := os.Stat(stoppedPath); err != nil {
		t.Fatalf("idle timeout did not stop Claude: %v", err)
	}
}

func TestPTYRunKillsClaudeWhenSIGTERMDoesNotStopIt(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	pidPath := filepath.Join(t.TempDir(), "pid")
	transcriptDir := claudeTranscriptDir(t, home, root)
	command := writeClaudeTestDouble(t, fmt.Sprintf(`
printf '%%s' "$$" > %q
session_id=''
while [ "$#" -gt 0 ]; do
	if [ "$1" = '--session-id' ]; then
		session_id="$2"
		break
	fi
	shift
done
transcript=%q/"$session_id".jsonl
mkdir -p %q
trap '' TERM
printf '%%s\n' '{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"VERDICT: approve\\nSUMMARY: complete\\nFINDINGS:\\n"}]}}' >> "$transcript"
printf '\033]9;Claude is waiting for your input\007'
while :; do :; done
`, pidPath, transcriptDir, transcriptDir))
	adapter := newPTYTestAdapter(command, root, home)
	adapter.terminateWait = 100 * time.Millisecond

	stream, err := adapter.Run(context.Background(), harness.Request{
		Model: "claude-sonnet-5", Effort: config.EffortMedium, Prompt: "review it",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for range stream.Events() {
	}
	if err := stream.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}

	pidContents, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(pidContents))
	if err != nil {
		t.Fatalf("parse test-double pid: %v", err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("Claude test double still exists after SIGKILL; signal check = %v", err)
	}
}

func TestPTYRunRefusesClaudeChildSession(t *testing.T) {
	t.Setenv("CLAUDE_CODE_CHILD_SESSION", "child")
	adapter := NewPTY(t.TempDir())

	_, err := adapter.Run(context.Background(), harness.Request{
		Model: "claude-sonnet-5", Effort: config.EffortMedium, Prompt: "review it",
	})
	if err == nil || !strings.Contains(err.Error(), "run syl from a terminal") {
		t.Fatalf("Run() error = %v, want child-session refusal with terminal guidance", err)
	}
}

func newPTYTestAdapter(command, projectRoot, home string) *PTYAdapter {
	return &PTYAdapter{
		command:       command,
		projectRoot:   projectRoot,
		homeDir:       home,
		pollInterval:  10 * time.Millisecond,
		idleTimeout:   2 * time.Second,
		terminateWait: 200 * time.Millisecond,
	}
}

func claudeTranscriptDir(t *testing.T, home, projectRoot string) string {
	t.Helper()
	path, err := transcript.New(home).Find(projectRoot, "session")
	if err != nil {
		t.Fatalf("find transcript: %v", err)
	}
	return filepath.Dir(path)
}

func writeClaudeTestDouble(t *testing.T, body string) string {
	t.Helper()
	command := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(command, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return command
}

func collectHarnessEvents(t *testing.T, stream harness.Stream) []harness.Event {
	t.Helper()
	var events []harness.Event
	for event := range stream.Events() {
		events = append(events, event)
	}
	if err := stream.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	return events
}

func eventSessionID(events []harness.Event) string {
	for _, event := range events {
		if event.Type == harness.EventSession {
			return event.SessionID
		}
	}
	return ""
}

func eventAssistantText(events []harness.Event) string {
	var text strings.Builder
	for _, event := range events {
		if event.Type == harness.EventAssistantText {
			text.WriteString(event.Text)
		}
	}
	return text.String()
}
