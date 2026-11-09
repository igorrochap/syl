package claude

import (
	"bytes"
	"context"
	"encoding/base64"
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
	"unicode/utf8"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/harness/claude/transcript"
	"github.com/igorrochap/syl/internal/orchestration"
	"github.com/igorrochap/syl/internal/verdict"
)

func TestClaudeAttachInvokesInteractivePrompt(t *testing.T) {
	for _, mode := range []config.SandboxMode{config.SandboxModeFullAccess, config.SandboxModeWorkspaceWrite, config.SandboxModeReadOnly} {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			argsPath := filepath.Join(root, "args")
			command := filepath.Join(t.TempDir(), "claude")
			script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + argsPath + "\"\n"
			if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			adapter := New(root)
			adapter.command = command

			err := adapter.Attach(context.Background(), harness.Request{
				Model:       "claude-opus-5",
				Effort:      config.EffortHigh,
				Prompt:      "/to-tickets\n\nTopic: offline mode",
				MCP:         false,
				SandboxMode: mode,
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
		})
	}
}

func TestClaudeAttachSessionInvokesResumeWithoutRequestSettings(t *testing.T) {
	for _, sandbox := range []struct {
		name string
		mode config.SandboxMode
	}{
		{name: "omitted sandbox"},
		{name: "full access", mode: config.SandboxModeFullAccess},
		{name: "workspace write", mode: config.SandboxModeWorkspaceWrite},
		{name: "read only", mode: config.SandboxModeReadOnly},
	} {
		for _, tt := range []struct {
			name string
			mcp  bool
			want []string
		}{
			{name: "strict MCP", want: []string{"--resume", "session-1", "--strict-mcp-config"}},
			{name: "inherited MCP", mcp: true, want: []string{"--resume", "session-1"}},
		} {
			t.Run(tt.name+"/"+sandbox.name, func(t *testing.T) {
				root := t.TempDir()
				argsPath := filepath.Join(root, "args")
				command := filepath.Join(t.TempDir(), "claude")
				script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + argsPath + "\"\n"
				if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
				adapter := New(root)
				adapter.command = command

				err := adapter.AttachSession(context.Background(), "session-1", harness.Request{
					Model:       "ignored-model",
					Effort:      config.Effort("ignored-effort"),
					MCP:         tt.mcp,
					SandboxMode: sandbox.mode,
				})
				if err != nil {
					t.Fatalf("AttachSession() error = %v", err)
				}

				got := strings.Split(strings.TrimSpace(readClaudeFile(t, argsPath)), "\n")
				if !reflect.DeepEqual(got, tt.want) {
					t.Fatalf("Claude attach session args = %#v, want %#v", got, tt.want)
				}
			})
		}
	}
}

func TestClaudeAttachSessionRejectsBlankSessionID(t *testing.T) {
	adapter := New(t.TempDir())
	for _, sessionID := range []string{"", " \t\n"} {
		t.Run(strconv.Quote(sessionID), func(t *testing.T) {
			err := adapter.AttachSession(context.Background(), sessionID, harness.Request{})
			if err == nil || !strings.Contains(err.Error(), "Claude Code") {
				t.Fatalf("AttachSession() error = %v, want Claude Code session error", err)
			}
		})
	}
}

func TestClaudeAttachSessionUsesProjectRootAndReportsExitFailure(t *testing.T) {
	root := t.TempDir()
	workingDirectoryPath := filepath.Join(root, "working-directory")
	command := writeClaudeTestDouble(t, fmt.Sprintf("pwd > %q\nexit 7\n", workingDirectoryPath))
	adapter := New(root)
	adapter.command = command

	err := adapter.AttachSession(context.Background(), "session-1", harness.Request{})
	if err == nil || !strings.Contains(err.Error(), "Claude Code") {
		t.Fatalf("AttachSession() error = %v, want Claude Code exit error", err)
	}
	workingDirectory := strings.TrimSpace(readClaudeFile(t, workingDirectoryPath))
	if workingDirectory != root {
		t.Fatalf("Claude attach session working directory = %q, want %q", workingDirectory, root)
	}
}

func TestClaudeAttachSessionIgnoresChildSessionGuard(t *testing.T) {
	root := t.TempDir()
	command := writeClaudeTestDouble(t, "exit 0\n")
	adapter := New(root)
	adapter.command = command
	t.Setenv("CLAUDE_CODE_CHILD_SESSION", "child")

	if err := adapter.AttachSession(context.Background(), "session-1", harness.Request{}); err != nil {
		t.Fatalf("AttachSession() error = %v, want no child-session guard", err)
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

func TestPTYRunArgsGrantAutonomousPermissions(t *testing.T) {
	args, err := ptyRunArgs("session-1", harness.Request{
		Model: "claude-sonnet-5", Effort: config.EffortMedium, Prompt: "do it",
	})
	if err != nil {
		t.Fatalf("ptyRunArgs() error = %v", err)
	}
	if !containsFlagValue(args, "--permission-mode", "bypassPermissions") {
		t.Fatalf("ptyRunArgs() = %v, want --permission-mode bypassPermissions", args)
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
			run, err := ptyRunArgs("session-1", request)
			if err != nil {
				t.Fatalf("ptyRunArgs() error = %v", err)
			}
			resume, err := ptyResumeArgs("session-1", request)
			if err != nil {
				t.Fatalf("ptyResumeArgs() error = %v", err)
			}

			if got := containsArg(run, "--strict-mcp-config"); got != tt.want {
				t.Fatalf("ptyRunArgs() strict MCP flag = %t, want %t: %v", got, tt.want, run)
			}
			if got := containsArg(resume, "--strict-mcp-config"); got != tt.want {
				t.Fatalf("ptyResumeArgs() strict MCP flag = %t, want %t: %v", got, tt.want, resume)
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
	t.Setenv("HOME", home)
	t.Setenv("PATH", filepath.Dir(command)+string(os.PathListSeparator)+os.Getenv("PATH"))
	adapter := New(root)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream, err := adapter.Run(ctx, harness.Request{
		Model:  "claude-sonnet-5",
		Effort: config.EffortMedium,
		Prompt: "review it",
		MCP:    false,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	knownSession, ok := stream.(harness.SessionStream)
	if !ok || knownSession.SessionID() == "" {
		t.Fatalf("PTY stream = %#v, want preselected session ID", stream)
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
	if knownSession.SessionID() != sessionID {
		t.Fatalf("PTY stream session ID = %q, event session ID = %q", knownSession.SessionID(), sessionID)
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
		t.Fatalf("terminal Claude args contain batch-output flags: %v", args)
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
	for _, completion := range []harness.CompletionSignal{harness.CompletionReviewVerdict, harness.CompletionTurnEnd} {
		t.Run(completion.String(), func(t *testing.T) {
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
				nil,
			)

			review, err := orchestration.RunReviewExecution(
				context.Background(),
				adapter,
				harness.Request{
					Completion: completion,
					Model:      "claude-sonnet-5", Effort: config.EffortMedium, Prompt: "review it",
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
			if len(review.SessionIDs) != 1 || review.SessionIDs[0] == "" {
				t.Fatalf("session ids = %#v, want one preselected session across the question relay", review.SessionIDs)
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

		})
	}
}

func TestPTYRunTranscriptEntriesWithoutToolCallsResetNoProgressLimit(t *testing.T) {
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
	adapter.noProgressLimit = 750 * time.Millisecond

	stream, err := adapter.Run(context.Background(), harness.Request{
		Model: "claude-sonnet-5", Effort: config.EffortMedium, Prompt: "review it",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	eventCount := 0
	for range stream.Events() {
		eventCount++
	}
	if eventCount == 0 {
		t.Fatal("PTY stream emitted no session event")
	}
	if err := stream.Wait(); err != nil {
		t.Fatalf("Wait() error = %v; transcript entries without tool calls should keep the run alive", err)
	}
}

func TestPTYRunSubagentTranscriptGrowthResetsNoProgressLimit(t *testing.T) {
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
subagents=%q/"$session_id"/subagents
mkdir -p "$subagents"
trap 'exit 0' TERM
printf '%%s\n' '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"subagent work 1"}]}}' >> "$subagents/agent-x.jsonl"
sleep 0.3
printf '%%s\n' '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"subagent work 2"}]}}' >> "$subagents/agent-x.jsonl"
sleep 0.3
printf '%%s\n' '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"subagent work 3"}]}}' >> "$subagents/agent-x.jsonl"
sleep 0.3
printf '%%s\n' '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"subagent work 4"}]}}' >> "$subagents/agent-x.jsonl"
sleep 0.3
printf '%%s\n' '{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"VERDICT: approve\\nSUMMARY: complete\\nFINDINGS:\\n"}]}}' >> "$transcript"
printf '\033]9;Claude is waiting for your input\007'
while :; do sleep 0.05; done
`, transcriptDir, transcriptDir))
	adapter := newPTYTestAdapter(command, root, home)
	adapter.noProgressLimit = 750 * time.Millisecond

	stream, err := adapter.Run(context.Background(), harness.Request{
		Model: "claude-sonnet-5", Effort: config.EffortMedium, Prompt: "review it",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	var events []harness.Event
	for event := range stream.Events() {
		events = append(events, event)
	}
	if err := stream.Wait(); err != nil {
		t.Fatalf("Wait() error = %v; subagent growth should keep the session alive", err)
	}
	if got := eventAssistantText(events); !strings.Contains(got, "VERDICT: approve") {
		t.Fatalf("assistant text = %q, want main-transcript verdict", got)
	}
}

func TestPTYRunTerminalOutputResetsNoProgressLimit(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	transcriptDir := claudeTranscriptDir(t, home, root)
	command := writeClaudeTestDouble(t, fmt.Sprintf(`
session_id=''
while [ "$#" -gt 0 ]; do
	if [ "$1" = '--session-id' ]; then session_id="$2"; break; fi
	shift
done
transcript=%q/"$session_id".jsonl
mkdir -p %q
trap 'exit 0' TERM
printf '.'
sleep 0.5
printf '.'
sleep 0.5
printf '.'
sleep 0.5
printf '%%s\n' '{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"VERDICT: approve\\nSUMMARY: complete\\nFINDINGS:\\n"}]}}' >> "$transcript"
printf '\033]9;Claude is waiting for your input\007'
while :; do sleep 0.05; done
`, transcriptDir, transcriptDir))
	adapter := newPTYTestAdapter(command, root, home)
	adapter.noProgressLimit = time.Second
	adapter.pollInterval = 5 * time.Millisecond

	started := time.Now()
	stream, err := adapter.Run(context.Background(), harness.Request{
		Model: "claude-sonnet-5", Prompt: "review", Effort: config.EffortMedium,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := collectHarnessEvents(t, stream)
	if elapsed := time.Since(started); elapsed <= adapter.noProgressLimit {
		t.Fatalf("session completed after %s, want terminal output to keep it alive past %s", elapsed, adapter.noProgressLimit)
	}
	if !strings.Contains(eventAssistantText(events), "SUMMARY: complete") {
		t.Fatalf("assistant text = %q, want completion after terminal-only progress", eventAssistantText(events))
	}
}

func TestPTYRunHealthySilentSessionCompletesBeforeNoProgressLimit(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	transcriptDir := claudeTranscriptDir(t, home, root)
	command := writeClaudeTestDouble(t, fmt.Sprintf(`
session_id=''
while [ "$#" -gt 0 ]; do
	if [ "$1" = '--session-id' ]; then session_id="$2"; break; fi
	shift
done
transcript=%q/"$session_id".jsonl
mkdir -p %q
trap 'exit 0' TERM
sleep 1.2
printf '%%s\n' '{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"VERDICT: approve\\nSUMMARY: healthy response\\nFINDINGS:\\n"}]}}' >> "$transcript"
printf '\033]9;Claude is waiting for your input\007'
while :; do sleep 0.05; done
`, transcriptDir, transcriptDir))
	adapter := newPTYTestAdapter(command, root, home)
	adapter.noProgressLimit = 2 * time.Second
	adapter.stallWindow = 100 * time.Millisecond
	adapter.pollInterval = 5 * time.Millisecond

	started := time.Now()
	stream, err := adapter.Run(context.Background(), harness.Request{
		Model: "claude-sonnet-5", Prompt: "review", Effort: config.EffortMedium,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := collectHarnessEvents(t, stream)
	if elapsed := time.Since(started); elapsed <= time.Second {
		t.Fatalf("session completed after %s, want a silent response longer than the old idle window", elapsed)
	}
	if !strings.Contains(eventAssistantText(events), "SUMMARY: healthy response") {
		t.Fatalf("assistant text = %q, want healthy session response", eventAssistantText(events))
	}
}

func TestPTYRunNoProgressLimitReportsUnansweredAPIRequest(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	const sessionID = "unanswered-api-request"
	transcriptPath, err := transcript.New(home).Find(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(transcriptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcriptPath, []byte(`{"type":"api-request","timestamp":"2026-11-06T10:00:00Z"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := writeClaudeTestDouble(t, "trap 'exit 0' TERM\nwhile :; do sleep 0.02; done\n")
	adapter := newPTYTestAdapter(command, root, home)
	adapter.stallWindow = 40 * time.Millisecond
	adapter.noProgressLimit = 120 * time.Millisecond
	adapter.pollInterval = 5 * time.Millisecond
	started := time.Now()
	stream, err := adapter.start(context.Background(), sessionID, nil, transcriptStartFresh, harness.CompletionReviewVerdict)
	if err != nil {
		t.Fatal(err)
	}
	eventCount := 0
	for range stream.Events() {
		eventCount++
	}
	if eventCount == 0 {
		t.Fatal("PTY stream emitted no events")
	}
	err = stream.Wait()
	if !errors.Is(err, harness.ErrSessionStalled) {
		t.Fatalf("Wait() error = %v, want ErrSessionStalled", err)
	}
	if !strings.Contains(err.Error(), "no-progress limit") || !strings.Contains(err.Error(), "Unanswered api-request age:") {
		t.Fatalf("Wait() error = %v, want no-progress limit and session-state summary", err)
	}
	if elapsed := time.Since(started); elapsed < adapter.noProgressLimit {
		t.Fatalf("Wait() returned after %s, before the no-progress limit %s", elapsed, adapter.noProgressLimit)
	}
}

func TestPTYResumeNoProgressIncludesFinishedReportsNotPreviouslyDelivered(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	const sessionID = "review-awaiting-verdict"
	reader := transcript.New(home)
	transcriptPath, err := reader.Find(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(transcriptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	priorTurn := `{"type":"user","message":{"role":"user","content":"review this change"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"Review completed without a verdict."}]}}` + "\n"
	if err := os.WriteFile(transcriptPath, []byte(priorTurn), 0o600); err != nil {
		t.Fatal(err)
	}
	subagentsDir := filepath.Join(filepath.Dir(transcriptPath), sessionID, "subagents")
	if err := os.MkdirAll(subagentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	reportPath := writeFinishedSubagent(t, subagentsDir, "agent-review", "Finished review report.")
	command := writeClaudeTestDouble(t, fmt.Sprintf(`
printf '%%s\n' '{"type":"user","message":{"role":"user","content":"emit the verdict block"}}' >> %q
trap 'exit 0' TERM
while :; do sleep 0.02; done
`, transcriptPath))
	adapter := newPTYTestAdapter(command, root, home)
	adapter.noProgressLimit = 120 * time.Millisecond
	adapter.pollInterval = 5 * time.Millisecond
	stream, err := adapter.Resume(context.Background(), sessionID, harness.Request{
		Model: "claude-sonnet-5", Effort: config.EffortMedium, Prompt: "emit the verdict block",
	})
	if err != nil {
		t.Fatal(err)
	}
	eventCount := 0
	for range stream.Events() {
		eventCount++
	}
	if eventCount == 0 {
		t.Fatal("PTY stream emitted no resume event")
	}
	err = stream.Wait()
	var stalled *harness.SessionStalledError
	if !errors.As(err, &stalled) || len(stalled.Reports) != 1 || stalled.Reports[0].Path != reportPath {
		t.Fatalf("Wait() error = %v, reports = %#v; want the finished report not yet delivered to this session", err, stalled)
	}
}

func TestPTYRunHeldRequestRepaintsStillHitNoProgressLimit(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	const sessionID = "held-request-no-progress"
	writeAPIRequestTranscript(t, root, home, sessionID)
	fixturePath := writeHeldRequestFixture(t)
	command := writeClaudeTestDouble(t, fmt.Sprintf(`trap 'exit 0' TERM
while :; do cat %q; sleep 0.02; done
`, fixturePath))
	adapter := newPTYTestAdapter(command, root, home)
	adapter.noProgressLimit = 160 * time.Millisecond
	adapter.stallWindow = 60 * time.Millisecond
	adapter.pollInterval = 5 * time.Millisecond
	started := time.Now()
	stream, err := adapter.start(context.Background(), sessionID, nil, transcriptStartFresh, harness.CompletionTurnEnd)
	if err != nil {
		t.Fatal(err)
	}
	eventCount := 0
	for range stream.Events() {
		eventCount++
	}
	if eventCount == 0 {
		t.Fatal("PTY stream emitted no session event")
	}
	err = stream.Wait()
	var stalled *harness.SessionStalledError
	if !errors.As(err, &stalled) || stalled.Limit != harness.NoProgressLimit {
		t.Fatalf("Wait() error = %v, want no-progress limit after held-request spinner repaints", err)
	}
	if elapsed := time.Since(started); elapsed < adapter.noProgressLimit {
		t.Fatalf("Wait() returned after %s, before the no-progress limit %s", elapsed, adapter.noProgressLimit)
	}
	if !strings.Contains(err.Error(), "Unanswered api-request age:") ||
		!strings.Contains(err.Error(), "Subagents: 0 total, 0 finished") {
		t.Fatalf("Wait() error = %v, want unanswered request and no-subagent summary", err)
	}
}

func TestPTYRunHeldRequestRepaintsStillHitStallWindow(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	const sessionID = "held-request-stall-window"
	transcriptPath := writeAPIRequestTranscript(t, root, home, sessionID)
	appendTranscriptEntry(t, transcriptPath, `{"type":"queue-operation","operation":"enqueue","content":"<task-notification>done</task-notification>"}`)
	subagentsDir := filepath.Join(filepath.Dir(transcriptPath), sessionID, "subagents")
	if err := os.MkdirAll(subagentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subagentsDir, "agent-a.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subagentsDir, "agent-a.meta.json"), []byte(`{"finished":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fixturePath := writeHeldRequestFixture(t)
	command := writeClaudeTestDouble(t, fmt.Sprintf(`trap 'exit 0' TERM
while :; do cat %q; sleep 0.02; done
`, fixturePath))
	adapter := newPTYTestAdapter(command, root, home)
	adapter.stallWindow = 80 * time.Millisecond
	adapter.noProgressLimit = 500 * time.Millisecond
	adapter.pollInterval = 5 * time.Millisecond
	started := time.Now()
	stream, err := adapter.start(context.Background(), sessionID, nil, transcriptStartFresh, harness.CompletionReviewVerdict)
	if err != nil {
		t.Fatal(err)
	}
	eventCount := 0
	for range stream.Events() {
		eventCount++
	}
	if eventCount == 0 {
		t.Fatal("PTY stream emitted no session event")
	}
	err = stream.Wait()
	var stalled *harness.SessionStalledError
	if !errors.As(err, &stalled) || stalled.Limit != harness.StallWindowLimit {
		t.Fatalf("Wait() error = %v, want stall-window error despite repainting output", err)
	}
	if elapsed := time.Since(started); elapsed < adapter.stallWindow || elapsed >= adapter.noProgressLimit {
		t.Fatalf("Wait() returned after %s, want between stall window %s and no-progress limit %s", elapsed, adapter.stallWindow, adapter.noProgressLimit)
	}
}

func writeAPIRequestTranscript(t *testing.T, root, home, sessionID string) string {
	t.Helper()
	transcriptPath, err := transcript.New(home).Find(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(transcriptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	requestAt := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	contents := fmt.Sprintf("{\"type\":\"api-request\",\"timestamp\":%q}\n", requestAt)
	if err := os.WriteFile(transcriptPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return transcriptPath
}

func appendTranscriptEntry(t *testing.T, path, entry string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(file, entry); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeHeldRequestFixture(t *testing.T) string {
	t.Helper()
	fixture := readHeldRequestHeartbeatFixture(t)
	path := filepath.Join(t.TempDir(), "held-v1-messages.ansi")
	if err := os.WriteFile(path, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTerminalOutputCountsRepeatedPrintableOutput(t *testing.T) {
	output := &terminalOutput{}
	for _, text := range []string{".", ".", "same word", "same word"} {
		before := output.activityCountValue()
		output.capture([]byte(text))
		if got := output.activityCountValue(); got <= before {
			t.Fatalf("activity count after %q = %d, want greater than %d", text, got, before)
		}
	}
}

func TestTerminalOutputIgnoresHeldRequestHeartbeatAcrossReadSplits(t *testing.T) {
	captured := readHeldRequestHeartbeatFixture(t)
	output := &terminalOutput{}
	for _, value := range captured {
		output.capture([]byte{value})
	}
	if got := output.activityCountValue(); got != 0 {
		t.Fatalf("activity count for held-request heartbeat = %d, want zero", got)
	}

	output.capture([]byte("\x1b[?2026h(123s)\x1b[?2026l"))
	if got := output.activityCountValue(); got != 0 {
		t.Fatalf("activity count for captured elapsed-seconds counter = %d, want zero", got)
	}

	for _, repaintText := range []string{
		"Retrying request id 9f83c1 123s",
		"New response content",
	} {
		before := output.activityCountValue()
		output.capture([]byte("\x1b[?2026h" + repaintText + "\x1b[?2026l"))
		if got := output.activityCountValue(); got <= before {
			t.Fatalf("activity count for synchronized text %q = %d, want greater than %d", repaintText, got, before)
		}
	}
}

func TestTerminalOutputIgnoresUnsynchronizedSpinnerAndMinuteCounter(t *testing.T) {
	output := &terminalOutput{}
	for _, frame := range []string{"\x1b[31m◐ 1m 0s)\r", "123s"} {
		for _, value := range []byte(frame) {
			output.capture([]byte{value})
		}
	}
	if got := output.activityCountValue(); got != 0 {
		t.Fatalf("activity count for unsynchronized spinner frame = %d, want zero", got)
	}
}

func readHeldRequestFixture(t *testing.T) []byte {
	t.Helper()
	encoded, err := os.ReadFile("testdata/claude-held-v1-messages.ansi.b64")
	if err != nil {
		t.Fatalf("read held-model-request capture fixture: %v", err)
	}
	captured, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		t.Fatalf("decode held-model-request capture fixture: %v", err)
	}
	return captured
}

func readHeldRequestHeartbeatFixture(t *testing.T) []byte {
	t.Helper()
	captured := readHeldRequestFixture(t)
	marker := []byte("(16s)")
	position := bytes.Index(captured, marker)
	if position < 0 {
		t.Fatal("held-request capture has no elapsed-seconds frame")
	}
	start := bytes.LastIndex(captured[:position], []byte("\x1b[?2026h"))
	if start < 0 {
		t.Fatal("held-request capture has no synchronized-frame boundary")
	}
	return captured[start:]
}

func TestHeldRequestCaptureCoversMoreThanTwoMinutes(t *testing.T) {
	captured := readHeldRequestFixture(t)
	frameCount := bytes.Count(captured, []byte("\x1b[?2026h"))
	if frameCount < 700 {
		t.Fatalf("held-request capture has %d synchronized frames, want at least 700", frameCount)
	}
	if !bytes.Contains(captured, []byte("1m 0s)")) {
		t.Fatal("held-request capture has no minute-rollover counter")
	}
}

func TestPTYNoProgressLimitIncludesFinishedSubagentReports(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	const sessionID = "stalled-with-finished-subagent"
	reader := transcript.New(home)
	transcriptPath, err := reader.Find(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(transcriptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcriptPath, []byte(`{"type":"api-request"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	subagentsDir := filepath.Join(filepath.Dir(transcriptPath), sessionID, "subagents")
	if err := os.MkdirAll(subagentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	subagentPath := filepath.Join(subagentsDir, "agent-finished.jsonl")
	report := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Finished report."}]}}` + "\n"
	if err := os.WriteFile(subagentPath, []byte(report), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subagentsDir, "agent-finished.meta.json"), []byte(`{"finished":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(subagentPath)
	if err != nil {
		t.Fatal(err)
	}
	monitor := transcriptMonitor{
		reader:          reader,
		path:            transcriptPath,
		projectRoot:     root,
		sessionID:       sessionID,
		seenEntries:     1,
		subagentSizes:   map[string]int64{subagentPath: info.Size()},
		completion:      harness.CompletionReviewVerdict,
		noProgressLimit: time.Minute,
	}
	finished, err := monitor.checkNoProgressLimit()
	var stalled *harness.SessionStalledError
	if !finished || !errors.As(err, &stalled) || !errors.Is(err, harness.ErrSessionStalled) {
		t.Fatalf("checkNoProgressLimit() = (%t, %v), want typed stalled-session error", finished, err)
	}
	if len(stalled.Reports) != 1 || stalled.Reports[0].Text != "Finished report." {
		t.Fatalf("stalled reports = %#v, want finished subagent report", stalled.Reports)
	}
}

func TestPTYRunNewEmptySubagentTranscriptResetsNoProgressLimit(t *testing.T) {
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
subagents=%q/"$session_id"/subagents
mkdir -p "$subagents"
trap 'exit 0' TERM
sleep 1.8
: > "$subagents/agent-x.jsonl"
sleep 1.5
printf '%%s\n' '{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"VERDICT: approve"}]}}' >> "$transcript"
printf '\033]9;Claude is waiting for your input\007'
while :; do sleep 0.05; done
`, transcriptDir, transcriptDir))
	adapter := newPTYTestAdapter(command, root, home)
	adapter.noProgressLimit = 3 * time.Second
	stream, err := adapter.Run(context.Background(), harness.Request{
		Model: "claude-sonnet-5", Effort: config.EffortMedium, Prompt: "review it",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	events := collectHarnessEvents(t, stream)
	if !strings.Contains(eventAssistantText(events), "VERDICT: approve") {
		t.Fatalf("assistant text = %q, want main-transcript verdict", eventAssistantText(events))
	}
}

func TestPTYRunIgnoresSubagentVerdict(t *testing.T) {
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
subagents=%q/"$session_id"/subagents
mkdir -p "$subagents"
trap 'exit 0' TERM
printf '%%s\n' '{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"VERDICT: approve"}]}}' >> "$subagents/agent-x.jsonl"
while :; do sleep 0.05; done
`, transcriptDir))
	adapter := newPTYTestAdapter(command, root, home)
	adapter.noProgressLimit = time.Second
	stream, err := adapter.Run(context.Background(), harness.Request{
		Model: "claude-sonnet-5", Effort: config.EffortMedium, Prompt: "review it",
		Completion: harness.CompletionReviewVerdict,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	var events []harness.Event
	for event := range stream.Events() {
		events = append(events, event)
	}
	err = stream.Wait()
	if !errors.Is(err, harness.ErrSessionStalled) || !strings.Contains(err.Error(), "no-progress limit") {
		t.Fatalf("Wait() error = %v, want no-progress limit without a main-transcript verdict", err)
	}
	if len(events) != 1 || events[0].Type != harness.EventSession {
		t.Fatalf("events = %#v, want only the session event", events)
	}
	if got := eventAssistantText(events); got != "" {
		t.Fatalf("assistant text = %q, want no text from the subagent transcript", got)
	}
	subagentPath := filepath.Join(transcriptDir, eventSessionID(events), "subagents", "agent-x.jsonl")
	contents := readClaudeFile(t, subagentPath)
	if !strings.Contains(contents, "VERDICT: approve") {
		t.Fatalf("subagent transcript = %q, want the verdict marker used by the test", contents)
	}
}

func TestPTYRunNoProgressLimitCompletesWithoutTerminalEscape(t *testing.T) {
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
	adapter.noProgressLimit = 3 * time.Second

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
		t.Fatalf("completion check did not stop Claude: %v", err)
	}
}

func TestPTYRunCompletesFromPollWithoutTerminalEscape(t *testing.T) {
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
printf '%%s\n' '{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"VERDICT: approve\\nSUMMARY: the poll finds the verdict\\nFINDINGS:\\n"}]}}' >> "$transcript"
while :; do sleep 0.05; done
`, transcriptDir, transcriptDir, stoppedPath))
	adapter := newPTYTestAdapter(command, root, home)
	// The no-progress limit is long and the test double never writes the
	// terminal escape, so only the poll can end this run.
	adapter.noProgressLimit = 30 * time.Second
	adapter.pollInterval = 25 * time.Millisecond

	started := time.Now()
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
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("run took %s; it waited for the no-progress limit instead of the poll", elapsed)
	}
	if _, err := os.Stat(stoppedPath); err != nil {
		t.Fatalf("run did not stop Claude: %v", err)
	}
}

func TestPTYImplementerCompletesOnProseTurnEnd(t *testing.T) {
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
printf '%%s\n' '{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"Implemented the ticket."}]}}' >> "$transcript"
while :; do sleep 0.05; done
`, transcriptDir, transcriptDir, stoppedPath))
	adapter := newPTYTestAdapter(command, root, home)
	// The no-progress limit is long and the test double never writes the
	// terminal escape, so only the poll can end this run.
	adapter.noProgressLimit = 30 * time.Second
	adapter.pollInterval = 25 * time.Millisecond

	started := time.Now()
	stream, err := adapter.Run(context.Background(), harness.Request{
		Model: "claude-sonnet-5", Effort: config.EffortMedium, Prompt: "implement it", Completion: harness.CompletionTurnEnd,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	events := collectHarnessEvents(t, stream)
	if got := eventAssistantText(events); got != "Implemented the ticket." {
		t.Fatalf("assistant text = %q, want the prose report", got)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("run took %s; it waited for the no-progress limit instead of the poll", elapsed)
	}
	if _, err := os.Stat(stoppedPath); err != nil {
		t.Fatalf("run did not stop Claude: %v", err)
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
	adapter := New(t.TempDir())

	_, err := adapter.Run(context.Background(), harness.Request{
		Model: "claude-sonnet-5", Effort: config.EffortMedium, Prompt: "review it",
	})
	if err == nil || !strings.Contains(err.Error(), "run syl from a terminal") {
		t.Fatalf("Run() error = %v, want child-session refusal with terminal guidance", err)
	}
}

func newPTYTestAdapter(command, projectRoot, home string) *PTYAdapter {
	return &PTYAdapter{
		command:         command,
		projectRoot:     projectRoot,
		homeDir:         home,
		pollInterval:    10 * time.Millisecond,
		noProgressLimit: 2 * time.Second,
		terminateWait:   200 * time.Millisecond,
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

func readClaudeFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
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

func TestPTYMissingCompletionSignal(t *testing.T) {
	for _, tt := range []struct {
		name       string
		completion harness.CompletionSignal
		stopReason string
		exits      bool
		want       string
	}{
		{name: "implementer tool use", completion: harness.CompletionTurnEnd, stopReason: "tool_use", want: "without a turn end"},
		{name: "implementer no message", completion: harness.CompletionTurnEnd, want: "without a turn end"},
		{name: "reviewer prose", completion: harness.CompletionReviewVerdict, stopReason: "end_turn", want: "without a review verdict"},
		{name: "reviewer exits", completion: harness.CompletionReviewVerdict, stopReason: "end_turn", exits: true, want: "exited before producing a review verdict"},
		{name: "implementer exits", completion: harness.CompletionTurnEnd, stopReason: "tool_use", exits: true, want: "exited before producing a turn end"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			home := t.TempDir()
			transcriptDir := claudeTranscriptDir(t, home, root)
			ending := "trap 'exit 0' TERM\nwhile :; do sleep 0.05; done"
			if tt.exits {
				ending = "exit 0\n"
			}
			command := writeClaudeTestDouble(t, fmt.Sprintf(`
while [ "$#" -gt 0 ]; do
	if [ "$1" = '--session-id' ]; then session_id="$2"; break; fi
	shift
done
mkdir -p %q
if [ -n '%s' ]; then
	printf '%%s\n' '{"type":"assistant","message":{"role":"assistant","stop_reason":"%s","content":[{"type":"text","text":"Working on the ticket."}]}}' >> %q/"$session_id".jsonl
fi
%s
`, transcriptDir, tt.stopReason, tt.stopReason, transcriptDir, ending))
			adapter := newPTYTestAdapter(command, root, home)
			adapter.noProgressLimit = 2 * time.Second
			if tt.exits {
				adapter.noProgressLimit = 5 * time.Second
			}
			started := time.Now()
			stream, err := adapter.Run(context.Background(), harness.Request{
				Model: "claude-sonnet-5", Prompt: "work", Effort: config.EffortMedium, Completion: tt.completion,
			})
			if err != nil {
				t.Fatal(err)
			}
			var events []harness.Event
			for event := range stream.Events() {
				events = append(events, event)
			}
			wantText := "Working on the ticket."
			if tt.stopReason == "" {
				wantText = ""
			}
			if got := eventAssistantText(events); got != wantText {
				t.Fatalf("assistant text = %q, want %q", got, wantText)
			}
			err = stream.Wait()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Wait() error = %v, want %q", err, tt.want)
			}
			sessionID := eventSessionID(events)
			path := filepath.Join(transcriptDir, sessionID+".jsonl")
			for _, want := range []string{path, sessionID} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("Wait() error = %q, want %q", err, want)
				}
			}
			if !tt.exits && time.Since(started) < adapter.noProgressLimit {
				t.Fatal("turn completed before the no-progress limit")
			}
		})
	}
}

func TestPTYNoProgressLimitReportsMissingTranscriptAndSilentTerminal(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	command := writeClaudeTestDouble(t, "printf '   \\n\\n\\t'\ntrap 'exit 0' TERM\nwhile :; do sleep 0.05; done\n")
	adapter := newPTYTestAdapter(command, root, home)
	adapter.noProgressLimit = 200 * time.Millisecond
	stream, err := adapter.Run(context.Background(), harness.Request{
		Model: "claude-sonnet-5", Prompt: "review", Effort: config.EffortMedium,
	})
	if err != nil {
		t.Fatal(err)
	}
	var sessionID string
	for event := range stream.Events() {
		if event.Type == harness.EventSession {
			sessionID = event.SessionID
		}
	}
	err = stream.Wait()
	if err == nil {
		t.Fatal("Wait() succeeded, want idle-timeout error")
	}
	path := filepath.Join(claudeTranscriptDir(t, home, root), sessionID+".jsonl")
	if !filepath.IsAbs(path) || sessionID == "" {
		t.Fatalf("invalid session transcript path %q or session id %q", path, sessionID)
	}
	message := err.Error()
	if !errors.Is(err, harness.ErrSessionStalled) || !strings.Contains(message, "no-progress limit") {
		t.Fatalf("Wait() error = %v, want no-progress limit error", err)
	}
	for _, want := range []string{path, sessionID, "missing"} {
		if !strings.Contains(message, want) {
			t.Fatalf("Wait() error = %q, want %q", message, want)
		}
	}
	if strings.Contains(message, "Last terminal output") {
		t.Fatalf("silent session printed an empty terminal tail: %q", message)
	}
}

func TestPTYNoProgressLimitReportsTranscriptDerivedSessionState(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	const sessionID = "review-session"
	requestAt := time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339Nano)
	transcriptPath, err := transcript.New(home).Find(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(transcriptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	contents := fmt.Sprintf(`{"type":"api-request","timestamp":%q}
{"type":"queue-operation","operation":"enqueue","content":"<task-notification>one</task-notification>"}
{"type":"queue-operation","operation":"enqueue","content":"<task-notification>two</task-notification>"}
`, requestAt)
	if err := os.WriteFile(transcriptPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	subagentsDir := filepath.Join(filepath.Dir(transcriptPath), sessionID, "subagents")
	if err := os.MkdirAll(subagentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"agent-a.meta.json", "agent-b.meta.json"} {
		if err := os.WriteFile(filepath.Join(subagentsDir, name), []byte(`{"finished":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	command := writeClaudeTestDouble(t, `
printf '   \n\n\t'
trap 'exit 0' TERM
while :; do sleep 0.05; done
`)
	adapter := newPTYTestAdapter(command, root, home)
	adapter.noProgressLimit = 200 * time.Millisecond
	stream, err := adapter.start(context.Background(), sessionID, nil, transcriptStartFresh, harness.CompletionReviewVerdict)
	if err != nil {
		t.Fatal(err)
	}
	eventCount := 0
	for range stream.Events() {
		eventCount++
	}
	if eventCount == 0 {
		t.Fatal("PTY stream emitted no events")
	}
	err = stream.Wait()
	if err == nil {
		t.Fatal("Wait() succeeded, want idle-timeout error")
	}
	message := err.Error()
	if sessionID == "" || !strings.Contains(message, sessionID) {
		t.Fatalf("Wait() error = %q, want session id %q", message, sessionID)
	}
	if !strings.Contains(message, "Unanswered api-request age:") {
		t.Fatalf("Wait() error lacks unanswered api-request age: %q", message)
	}
	for _, want := range []string{
		"Task notifications: 2 enqueued, 0 dequeued or removed",
		"Subagents: 2 total, 2 finished",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("Wait() error = %q, want %q", message, want)
		}
	}
	if strings.Contains(message, "Last terminal output") {
		t.Fatalf("whitespace-only terminal output printed a tail section: %q", message)
	}
}

func TestPTYRunDetectsStalledSessionsFromTranscriptAndSubagentState(t *testing.T) {
	for _, test := range []struct {
		name        string
		finished    bool
		dequeued    bool
		completion  harness.CompletionSignal
		wantStalled bool
	}{
		{name: "finished subagent with queued notification", finished: true, wantStalled: true},
		{name: "unfinished subagent", finished: false},
		{name: "notification already dequeued", finished: true, dequeued: true},
		{name: "implementer turn end uses no-progress limit", finished: true, completion: harness.CompletionTurnEnd},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, home := t.TempDir(), t.TempDir()
			const sessionID = "stalled-review-session"
			transcriptPath, err := transcript.New(home).Find(root, sessionID)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(transcriptPath), 0o755); err != nil {
				t.Fatal(err)
			}
			contents := `{"type":"api-request","timestamp":"2026-11-06T10:00:00Z"}` + "\n" +
				`{"type":"queue-operation","operation":"enqueue","content":"<task-notification>done</task-notification>"}` + "\n"
			if test.dequeued {
				contents += `{"type":"queue-operation","operation":"dequeue","content":"<task-notification>done</task-notification>"}` + "\n"
			}
			if err := os.WriteFile(transcriptPath, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			subagentsDir := filepath.Join(filepath.Dir(transcriptPath), sessionID, "subagents")
			if err := os.MkdirAll(subagentsDir, 0o755); err != nil {
				t.Fatal(err)
			}
			finished := `{"finished":false}`
			if test.finished {
				finished = `{"finished":true}`
			}
			if err := os.WriteFile(filepath.Join(subagentsDir, "agent-a.meta.json"), []byte(finished), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(subagentsDir, "agent-a.jsonl"), []byte(
				`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Earlier report."}]}}`+"\n"+
					`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Final report."}]}}`+"\n",
			), 0o600); err != nil {
				t.Fatal(err)
			}

			command := writeClaudeTestDouble(t, "trap 'exit 0' TERM\nwhile :; do sleep 0.02; done\n")
			adapter := newPTYTestAdapter(command, root, home)
			adapter.pollInterval = 5 * time.Millisecond
			adapter.stallWindow = 60 * time.Millisecond
			adapter.noProgressLimit = 300 * time.Millisecond
			started := time.Now()
			stream, err := adapter.start(
				context.Background(), sessionID, nil, transcriptStartFresh, test.completion,
			)
			if err != nil {
				t.Fatal(err)
			}
			eventCount := 0
			for range stream.Events() {
				eventCount++
			}
			if eventCount == 0 {
				t.Fatal("PTY stream emitted no events")
			}
			err = stream.Wait()
			var stalled *harness.SessionStalledError
			if test.wantStalled {
				if !errors.As(err, &stalled) || !errors.Is(err, harness.ErrSessionStalled) {
					t.Fatalf("Wait() error = %v, want typed stalled-session error", err)
				}
				if stalled.SessionID != sessionID || !strings.Contains(stalled.StateSummary, "1 total, 1 finished") {
					t.Fatalf("stalled session details = %#v, want session id and summary", stalled)
				}
				elapsed := time.Since(started)
				if elapsed < adapter.stallWindow || elapsed >= adapter.noProgressLimit {
					t.Fatalf("stalled session returned after %s, want between limits %s and %s", elapsed, adapter.stallWindow, adapter.noProgressLimit)
				}
				if !strings.Contains(err.Error(), "stall window") {
					t.Fatalf("Wait() error = %v, want fired stall window named", err)
				}
				if len(stalled.Reports) != 1 || stalled.Reports[0].Text != "Final report." || stalled.Reports[0].Path == "" {
					t.Fatalf("stalled reports = %#v, want final assistant text and path", stalled.Reports)
				}
				return
			}
			if errors.As(err, &stalled) && stalled.Limit == harness.StallWindowLimit {
				t.Fatalf("Wait() error = %v, want no stalled-session error", err)
			}
			if !errors.Is(err, harness.ErrSessionStalled) || !strings.Contains(err.Error(), "no-progress limit") {
				t.Fatalf("Wait() error = %v, want no-progress limit error", err)
			}
		})
	}
}

func TestPTYResumeDoesNotStallOnTaskNotificationFromEarlierTurn(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	const sessionID = "resumed-review-session"
	transcriptPath, err := transcript.New(home).Find(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(transcriptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	contents := `{"type":"api-request","timestamp":"2026-11-06T10:00:00Z"}` + "\n" +
		`{"type":"queue-operation","operation":"enqueue","content":"<task-notification>earlier task</task-notification>"}` + "\n"
	if err := os.WriteFile(transcriptPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	subagentsDir := filepath.Join(filepath.Dir(transcriptPath), sessionID, "subagents")
	if err := os.MkdirAll(subagentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subagentsDir, "agent-a.meta.json"), []byte(`{"finished":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	command := writeClaudeTestDouble(t, "trap 'exit 0' TERM\nwhile :; do sleep 0.02; done\n")
	adapter := newPTYTestAdapter(command, root, home)
	adapter.pollInterval = 5 * time.Millisecond
	adapter.stallWindow = 40 * time.Millisecond
	adapter.noProgressLimit = 160 * time.Millisecond
	stream, err := adapter.Resume(context.Background(), sessionID, harness.Request{Prompt: "continue review"})
	if err != nil {
		t.Fatal(err)
	}
	eventCount := 0
	for range stream.Events() {
		eventCount++
	}
	if eventCount == 0 {
		t.Fatal("PTY stream emitted no events")
	}
	err = stream.Wait()
	var stalled *harness.SessionStalledError
	if errors.As(err, &stalled) && stalled.Limit == harness.StallWindowLimit {
		t.Fatalf("Wait() error = %v, want prior-turn notification excluded from stall detection", err)
	}
	if !errors.Is(err, harness.ErrSessionStalled) || !strings.Contains(err.Error(), "no-progress limit") {
		t.Fatalf("Wait() error = %v, want ordinary no-progress limit error", err)
	}
}

func TestTranscriptMonitorRestartsStallWindowOnMainTranscriptEntry(t *testing.T) {
	state := SessionState{
		EnqueuedTaskNotifications: 1,
		SubagentCount:             1,
		FinishedSubagentCount:     1,
	}
	monitor := transcriptMonitor{
		reader:      transcript.New(t.TempDir()),
		projectRoot: t.TempDir(),
		sessionID:   "review-session",
		readState: func() (SessionState, error) {
			return state, nil
		},
		stallWindow: time.Minute,
	}
	start := time.Date(2026, time.November, 6, 12, 0, 0, 0, time.UTC)
	for _, at := range []time.Time{start, start.Add(59 * time.Second)} {
		stalled, err := monitor.checkStalledSession(transcriptProgress{}, at)
		if stalled || err != nil {
			t.Fatalf("checkStalledSession(%s) = (%t, %v), want window still active", at, stalled, err)
		}
	}
	stalled, err := monitor.checkStalledSession(transcriptProgress{mainEntry: true}, start.Add(60*time.Second))
	if stalled || err != nil {
		t.Fatalf("main transcript progress check = (%t, %v), want timer reset", stalled, err)
	}
	stalled, err = monitor.checkStalledSession(transcriptProgress{}, start.Add(61*time.Second))
	if stalled || err != nil {
		t.Fatalf("check after reset = (%t, %v), want a fresh window", stalled, err)
	}
	stalled, err = monitor.checkStalledSession(transcriptProgress{}, start.Add(120*time.Second))
	if stalled || err != nil {
		t.Fatalf("check before reset window ends = (%t, %v), want no stall", stalled, err)
	}
	stalled, err = monitor.checkStalledSession(transcriptProgress{}, start.Add(121*time.Second))
	if !stalled || !errors.Is(err, harness.ErrSessionStalled) {
		t.Fatalf("check at reset window end = (%t, %v), want stalled-session error", stalled, err)
	}
}

func TestTranscriptMonitorStallWindowIgnoresTerminalActivity(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	output := &terminalOutput{}
	noProgress := time.NewTimer(time.Hour)
	defer noProgress.Stop()
	state := SessionState{
		EnqueuedTaskNotifications: 1,
		SubagentCount:             1,
		FinishedSubagentCount:     1,
	}
	monitor := transcriptMonitor{
		reader:          transcript.New(home),
		path:            filepath.Join(root, "missing.jsonl"),
		projectRoot:     root,
		sessionID:       "review-session",
		readState:       func() (SessionState, error) { return state, nil },
		output:          output,
		noProgress:      noProgress,
		stallWindow:     time.Minute,
		noProgressLimit: time.Hour,
	}
	start := time.Date(2026, time.November, 7, 12, 0, 0, 0, time.UTC)
	stalled, err := monitor.checkStalledSession(transcriptProgress{}, start)
	if stalled || err != nil {
		t.Fatalf("initial checkStalledSession() = (%t, %v), want active stall window", stalled, err)
	}
	output.capture([]byte("model response\n"))
	progress, err := monitor.checkProgress()
	if err != nil {
		t.Fatal(err)
	}
	if !progress.terminalActivity {
		t.Fatal("model response text was not reported as terminal activity")
	}
	stalled, err = monitor.checkStalledSession(progress, start.Add(30*time.Second))
	if stalled || err != nil || !monitor.stallSince.Equal(start) {
		t.Fatalf("terminal activity check = (%t, %v), stall since %s; want the original stall start %s", stalled, err, monitor.stallSince, start)
	}
	stalled, err = monitor.checkStalledSession(transcriptProgress{}, start.Add(time.Minute))
	if !stalled || !errors.Is(err, harness.ErrSessionStalled) {
		t.Fatalf("check at stall window end = (%t, %v), want stall-window error", stalled, err)
	}
}

func TestTranscriptMonitorDefersSessionStateReadErrorsToNoProgressLimit(t *testing.T) {
	readErr := errors.New("session state read failed")
	root := t.TempDir()
	monitor := transcriptMonitor{
		reader:      transcript.New(t.TempDir()),
		path:        filepath.Join(root, "session.jsonl"),
		projectRoot: root,
		sessionID:   "review-session",
		completion:  harness.CompletionReviewVerdict,
		readState: func() (SessionState, error) {
			return SessionState{}, readErr
		},
		output: &terminalOutput{},
	}
	finished, err := monitor.checkStalledSession(transcriptProgress{}, time.Now().UTC())
	if finished || err != nil {
		t.Fatalf("checkStalledSession() = (%t, %v), want the no-progress limit to handle the read error", finished, err)
	}
	monitor.noProgressLimit = time.Minute
	finished, err = monitor.checkNoProgressLimit()
	var stalled *harness.SessionStalledError
	if !finished || !errors.As(err, &stalled) || !errors.Is(err, harness.ErrSessionStalled) {
		t.Fatalf("checkNoProgressLimit() = (%t, %v), want typed stalled-session error", finished, err)
	}
	if stalled.Limit != harness.NoProgressLimit || stalled.StateSummary != "unavailable: session state read failed" {
		t.Fatalf("stalled error = %#v, want no-progress limit and tolerant state summary", stalled)
	}
}

func TestPTYSessionLimitDefaults(t *testing.T) {
	_, stallWindow, noProgressLimit := New(t.TempDir()).sessionTiming()
	if stallWindow != 3*time.Minute {
		t.Fatalf("stall window = %s, want 3m0s", stallWindow)
	}
	if noProgressLimit != 15*time.Minute {
		t.Fatalf("no-progress limit = %s, want 15m0s", noProgressLimit)
	}
}

func TestTranscriptMonitorAddsTolerantSummaryToTranscriptReadErrors(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	const sessionID = "malformed-session"
	reader := transcript.New(home)
	path, err := reader.Find(root, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("malformed JSON\n{\"type\":\"last-valid-entry\",\"timestamp\":\"2026-11-03T10:00:00Z\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := New(root)
	adapter.homeDir = home
	checks := []struct {
		name  string
		check func(*transcriptMonitor) error
	}{
		{
			name: "completion check",
			check: func(monitor *transcriptMonitor) error {
				_, err := monitor.checkCompletion()
				return err
			},
		},
		{
			name: "idle check",
			check: func(monitor *transcriptMonitor) error {
				_, err := monitor.checkNoProgressLimit()
				return err
			},
		},
		{
			name: "process exit",
			check: func(monitor *transcriptMonitor) error {
				return monitor.checkProcessExit(errors.New("process exited"))
			},
		},
	}
	for _, test := range checks {
		t.Run(test.name, func(t *testing.T) {
			monitor := transcriptMonitor{
				reader:      reader,
				path:        path,
				projectRoot: root,
				sessionID:   sessionID,
				readState: func() (SessionState, error) {
					return adapter.ReadSessionState(sessionID)
				},
				output: &terminalOutput{},
			}
			err := test.check(&monitor)
			if err == nil || !strings.Contains(err.Error(), "Last main transcript entry: last-valid-entry at 2026-11-03T10:00:00Z") {
				t.Fatalf("monitor error = %v, want tolerant transcript summary", err)
			}
		})
	}
}

func TestDescribeSessionFailureIncludesReadableTailWhenSummaryReadFails(t *testing.T) {
	cause := errors.New("session failed")
	output := &terminalOutput{}
	output.capture([]byte("last useful line\n"))
	monitor := transcriptMonitor{
		sessionID: "session",
		path:      filepath.Join(t.TempDir(), "missing.jsonl"),
		readState: func() (SessionState, error) {
			return SessionState{}, errors.New("summary read failed")
		},
		output: output,
	}
	err := monitor.describeSessionFailure(cause)
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "Session state summary unavailable: summary read failed") {
		t.Fatalf("describeSessionFailure() error = %v, want wrapped cause and summary read issue", err)
	}
	if !strings.Contains(err.Error(), "Last terminal output (at most 2048 bytes):\nlast useful line") {
		t.Fatalf("describeSessionFailure() error lacks readable terminal tail: %v", err)
	}
}

func TestPTYNoProgressLimitReportsBoundedReadableTerminalTail(t *testing.T) {
	for _, tt := range []struct {
		name   string
		output string
		want   string
	}{
		{
			name: "large colored output and OSC notifications",
			output: `printf 'old output\n'
i=0
while [ "$i" -lt 1000 ]; do printf 'repeated output éééééééééé\n'; i=$((i+1)); done
printf '\033[31mLAST PRINTED LINE\033[0m\n'
printf '\033]9;Claude is waiting for your input\007'
printf '\033]0;hidden title\033\\'
`,
			want: "LAST PRINTED LINE\n",
		},
		{
			name: "escapes split between terminal reads",
			output: fmt.Sprintf(`printf '\033[0%s32mGREEN TEXT\033[0m\n'
printf '\033]9;Claude is waiting for your input\007'
printf '\033]0;%s\033\\LAST PRINTED LINE\n\033[31'
`, strings.Repeat(";0", 4096), strings.Repeat("hidden title", 1024)),
			want: "GREEN TEXT\nLAST PRINTED LINE\n",
		},
		{
			name: "other terminal controls",
			output: `printf '\033c\033(BREADY\tTEXT\r\n'
printf '\033\033[0m\033[31\033[0m'
printf '\033Pignored control string\033\\'
printf '\033]0;ignored title\033\033\\LAST PRINTED LINE\n'
`,
			want: "READY\tTEXT\nLAST PRINTED LINE\n",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root, home := t.TempDir(), t.TempDir()
			transcriptDir := claudeTranscriptDir(t, home, root)
			command := writeClaudeTestDouble(t, fmt.Sprintf(`
mkdir -p %q
touch %q/"$2".jsonl
%s
trap 'exit 0' TERM
while :; do sleep 0.05; done
`, transcriptDir, transcriptDir, tt.output))
			adapter := newPTYTestAdapter(command, root, home)
			adapter.noProgressLimit = 3 * time.Second
			stream, err := adapter.Run(context.Background(), harness.Request{
				Model: "claude-sonnet-5", Prompt: "review", Effort: config.EffortMedium,
			})
			if err != nil {
				t.Fatal(err)
			}
			var events []harness.Event
			for event := range stream.Events() {
				events = append(events, event)
			}
			err = stream.Wait()
			if err == nil {
				t.Fatal("Wait() succeeded, want idle-timeout error")
			}
			message := err.Error()
			if !strings.Contains(message, eventSessionID(events)) {
				t.Fatalf("Wait() error lacks session id: %q", message)
			}
			_, tail, found := strings.Cut(message, "Last terminal output (at most 2048 bytes):\n")
			if !found {
				t.Fatalf("Wait() error lacks terminal tail: %q", message)
			}
			if len(tail) > 2048 || !utf8.ValidString(tail) {
				t.Fatalf("terminal tail is not valid UTF-8 within 2048 bytes: %d bytes", len(tail))
			}
			if !strings.HasSuffix(tail, tt.want) {
				t.Fatalf("terminal tail = %q, want suffix %q", tail, tt.want)
			}
			for _, unwanted := range []string{"\x1b", "\x07", "hidden title", "Claude is waiting", "old output", "missing"} {
				if strings.Contains(message, unwanted) {
					t.Fatalf("Wait() error contains %q: %q", unwanted, message)
				}
			}
		})
	}
}

func TestPTYImplementerResumeWaitsForNewTurnEnd(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	transcriptDir := claudeTranscriptDir(t, home, root)
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(transcriptDir, "implement-session.jsonl")
	oldTurn := `{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"Old implementation report."}]}}` + "\n"
	if err := os.WriteFile(path, []byte(oldTurn), 0o600); err != nil {
		t.Fatal(err)
	}
	command := writeClaudeTestDouble(t, fmt.Sprintf(`
trap 'exit 0' TERM
sleep 0.5
printf '%%s\n' '{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"New implementation report."}]}}' >> %q
while :; do sleep 0.05; done
`, path))
	adapter := newPTYTestAdapter(command, root, home)
	adapter.noProgressLimit = 30 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	stream, err := adapter.Resume(ctx, "implement-session", harness.Request{
		Model: "claude-sonnet-5", Prompt: "revise", Effort: config.EffortMedium, Completion: harness.CompletionTurnEnd,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := collectHarnessEvents(t, stream)
	if elapsed := time.Since(started); elapsed < 500*time.Millisecond {
		t.Fatalf("resume completed after %s, before the new turn", elapsed)
	}
	if got := eventAssistantText(events); got != "New implementation report." {
		t.Fatalf("resumed assistant text = %q, want only the new turn", got)
	}
}

func TestClaudeIgnoresSandboxMode(t *testing.T) {
	for _, build := range []struct {
		name string
		args func(string, harness.Request) ([]string, error)
	}{
		{"Run", ptyRunArgs},
		{"Resume", ptyResumeArgs},
	} {
		t.Run(build.name, func(t *testing.T) {
			request := harness.Request{Model: "claude-sonnet-5", Effort: config.EffortMedium, Prompt: "noop"}
			want, err := build.args("session-1", request)
			if err != nil {
				t.Fatal(err)
			}
			for _, mode := range []config.SandboxMode{config.SandboxModeFullAccess, config.SandboxModeWorkspaceWrite, config.SandboxModeReadOnly} {
				request.SandboxMode = mode
				got, err := build.args("session-1", request)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("%s(%q) args = %v, want %v", build.name, mode, got, want)
				}
			}
		})
	}
}
