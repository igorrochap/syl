package claude

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/harness/claude/transcript"
)

const (
	claudeIdleEscape = "\x1b]9;Claude is waiting for your input"
	verdictMarker    = "VERDICT:"
)

// PTYAdapter drives a fresh Claude Code session through a pseudo-terminal.
type PTYAdapter struct {
	command       string
	projectRoot   string
	homeDir       string
	pollInterval  time.Duration
	idleTimeout   time.Duration
	terminateWait time.Duration
}

var _ harness.Adapter = (*PTYAdapter)(nil)

// NewPTY returns a Claude Code adapter that reads events from session transcripts.
func NewPTY(projectRoot string) *PTYAdapter {
	return &PTYAdapter{
		command:       "claude",
		projectRoot:   projectRoot,
		pollInterval:  250 * time.Millisecond,
		idleTimeout:   5 * time.Minute,
		terminateWait: 2 * time.Second,
	}
}

func (a *PTYAdapter) Run(ctx context.Context, request harness.Request) (harness.Stream, error) {
	if _, childSession := os.LookupEnv("CLAUDE_CODE_CHILD_SESSION"); childSession {
		return nil, errors.New(
			"cannot run Claude review in a pty from a Claude Code child session; run syl from a terminal",
		)
	}
	sessionID, err := newSessionID()
	if err != nil {
		return nil, err
	}
	args, err := ptyRunArgs(sessionID, request)
	if err != nil {
		return nil, err
	}
	return a.start(ctx, sessionID, args)
}

func (*PTYAdapter) Resume(context.Context, string, harness.Request) (harness.Stream, error) {
	return nil, errors.New("Claude Code pty sessions cannot be resumed yet")
}

func (a *PTYAdapter) Attach(ctx context.Context, request harness.Request) error {
	adapter := &Adapter{command: a.command, projectRoot: a.projectRoot}
	return adapter.Attach(ctx, request)
}

func (a *PTYAdapter) start(
	ctx context.Context,
	sessionID string,
	args []string,
) (harness.Stream, error) {
	reader := transcript.New(a.homeDir)
	transcriptPath, err := reader.Find(a.projectRoot, sessionID)
	if err != nil {
		return nil, fmt.Errorf("find Claude transcript: %w", err)
	}
	command := a.command
	if command == "" {
		command = "claude"
	}
	process := exec.Command(command, args...)
	if a.projectRoot != "" {
		process.Dir = a.projectRoot
	}
	terminal, err := pty.Start(process)
	if err != nil {
		return nil, fmt.Errorf("start Claude Code in pty: %w", err)
	}

	events := make(chan harness.Event)
	done := make(chan error, 1)
	go a.runSession(ctx, process, terminal, reader, transcriptPath, sessionID, events, done)
	return processStream{events: events, done: done}, nil
}

func (a *PTYAdapter) runSession(
	ctx context.Context,
	process *exec.Cmd,
	terminal *os.File,
	reader transcript.Reader,
	transcriptPath string,
	sessionID string,
	events chan<- harness.Event,
	done chan<- error,
) {
	defer close(events)
	defer terminal.Close()

	processDone := make(chan error, 1)
	go func() {
		processDone <- process.Wait()
	}()
	idleSignals := make(chan struct{}, 1)
	go watchClaudeTerminal(terminal, idleSignals)

	events <- harness.Event{Type: harness.EventSession, SessionID: sessionID}
	processExited, monitorErr := a.monitorSession(
		ctx,
		reader,
		transcriptPath,
		events,
		idleSignals,
		processDone,
	)
	if !processExited {
		if err := terminateProcess(process, processDone, a.terminateWait); err != nil {
			monitorErr = errors.Join(monitorErr, err)
		}
	}
	done <- monitorErr
}

func (a *PTYAdapter) monitorSession(
	ctx context.Context,
	reader transcript.Reader,
	transcriptPath string,
	events chan<- harness.Event,
	idleSignals <-chan struct{},
	processDone <-chan error,
) (bool, error) {
	pollInterval := a.pollInterval
	if pollInterval <= 0 {
		pollInterval = 250 * time.Millisecond
	}
	idleTimeout := a.idleTimeout
	if idleTimeout <= 0 {
		idleTimeout = 5 * time.Minute
	}
	poll := time.NewTicker(pollInterval)
	defer poll.Stop()
	idle := time.NewTimer(idleTimeout)
	defer idle.Stop()
	transcripts := transcriptMonitor{
		reader:      reader,
		path:        transcriptPath,
		events:      events,
		idle:        idle,
		idleTimeout: idleTimeout,
	}

	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case waitErr := <-processDone:
			progress, readErr := transcripts.checkProgress()
			if readErr != nil {
				return true, readErr
			}
			if progress.complete {
				return true, nil
			}
			if waitErr != nil {
				return true, fmt.Errorf("Claude Code exited unsuccessfully: %w", waitErr)
			}
			return true, errors.New("Claude Code exited before producing a review verdict")
		case <-poll.C:
			_, readErr := transcripts.checkProgress()
			if readErr != nil {
				return false, readErr
			}
		case <-idleSignals:
			progress, readErr := transcripts.checkProgress()
			if readErr != nil {
				return false, readErr
			}
			if progress.complete {
				return false, nil
			}
		case <-idle.C:
			progress, readErr := transcripts.checkProgress()
			if readErr != nil {
				return false, readErr
			}
			if progress.newEntries {
				if progress.complete {
					return false, nil
				}
				continue
			}
			if progress.complete {
				return false, nil
			}
			return false, fmt.Errorf(
				"Claude Code pty session was idle for %s without a review verdict",
				idleTimeout,
			)
		}
	}
}

type transcriptProgress struct {
	newEntries bool
	complete   bool
}

type transcriptMonitor struct {
	reader      transcript.Reader
	path        string
	events      chan<- harness.Event
	seenEntries int
	idle        *time.Timer
	idleTimeout time.Duration
}

func (m *transcriptMonitor) checkProgress() (transcriptProgress, error) {
	entryCount, complete, err := readTranscript(m.reader, m.path, m.seenEntries, m.events)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return transcriptProgress{}, nil
		}
		return transcriptProgress{}, err
	}
	progress := transcriptProgress{
		newEntries: entryCount > m.seenEntries,
		complete:   complete,
	}
	if progress.newEntries {
		m.seenEntries = entryCount
		resetTimer(m.idle, m.idleTimeout)
	}
	return progress, nil
}

func ptyRunArgs(sessionID string, request harness.Request) ([]string, error) {
	effort, err := claudeEffortFlag(request.Effort)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.Model) == "" {
		return nil, errors.New("Claude Code model is required")
	}
	if strings.TrimSpace(request.Prompt) == "" {
		return nil, errors.New("Claude Code prompt is required")
	}
	args := []string{
		"--session-id", sessionID,
		"--permission-mode", "bypassPermissions",
	}
	if !request.MCP {
		args = append(args, "--strict-mcp-config")
	}
	return append(args,
		"--model", request.Model,
		"--effort", effort,
		request.Prompt,
	), nil
}

func newSessionID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate Claude Code session id: %w", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf(
		"%x-%x-%x-%x-%x",
		value[0:4],
		value[4:6],
		value[6:8],
		value[8:10],
		value[10:16],
	), nil
}

func watchClaudeTerminal(terminal io.Reader, idleSignals chan<- struct{}) {
	marker := []byte(claudeIdleEscape)
	buffer := make([]byte, 4096)
	var tail []byte
	for {
		count, err := terminal.Read(buffer)
		if count > 0 {
			combined := append(tail, buffer[:count]...)
			if bytes.Contains(combined, marker) {
				select {
				case idleSignals <- struct{}{}:
				default:
				}
			}
			keep := len(marker) - 1
			if len(combined) < keep {
				keep = len(combined)
			}
			tail = append(tail[:0], combined[len(combined)-keep:]...)
		}
		if err != nil {
			return
		}
	}
}

type transcriptMessage struct {
	Type    string `json:"type"`
	Message struct {
		Role       string          `json:"role"`
		StopReason string          `json:"stop_reason"`
		Content    json.RawMessage `json:"content"`
	} `json:"message"`
}

func readTranscript(
	reader transcript.Reader,
	path string,
	seenEntries int,
	events chan<- harness.Event,
) (int, bool, error) {
	entries, err := reader.Read(path)
	if err != nil {
		return seenEntries, false, err
	}
	if seenEntries > len(entries) {
		seenEntries = 0
	}
	for _, entry := range entries[seenEntries:] {
		decoded, decodeErr := decodeTranscriptEntry(entry.Raw)
		if decodeErr != nil {
			return seenEntries, false, fmt.Errorf(
				"decode Claude transcript %s line %d: %w",
				path,
				entry.Line,
				decodeErr,
			)
		}
		for _, event := range decoded {
			events <- event
		}
	}
	return len(entries), latestCompletedTurnHasVerdict(entries), nil
}

func decodeTranscriptEntry(raw json.RawMessage) ([]harness.Event, error) {
	var message transcriptMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		return nil, err
	}
	rawLine := string(raw) + "\n"
	events := make([]harness.Event, 0, len(message.Message.Content))
	if isAssistantTranscriptMessage(message) {
		blocks, err := transcriptContentBlocks(message.Message.Content)
		if err != nil {
			return nil, err
		}
		for _, block := range blocks {
			switch block.Type {
			case "text":
				events = append(events, harness.Event{
					Type: harness.EventAssistantText,
					Text: block.Text,
				})
			case "tool_use":
				events = append(events, harness.Event{
					Type:         harness.EventToolUse,
					ToolName:     block.Name,
					ArgumentGist: argumentGist(block.Input),
				})
			}
		}
	}
	if len(events) == 0 {
		return []harness.Event{{Type: harness.EventRaw, Raw: rawLine}}, nil
	}
	events[0].Raw = rawLine
	return events, nil
}

func latestCompletedTurnHasVerdict(entries []transcript.Entry) bool {
	for index := len(entries) - 1; index >= 0; index-- {
		var message transcriptMessage
		if err := json.Unmarshal(entries[index].Raw, &message); err != nil {
			return false
		}
		if !isAssistantTranscriptMessage(message) || message.Message.StopReason != "end_turn" {
			continue
		}
		blocks, err := transcriptContentBlocks(message.Message.Content)
		if err != nil {
			return false
		}
		for _, block := range blocks {
			if block.Type == "text" && strings.Contains(block.Text, verdictMarker) {
				return true
			}
		}
		return false
	}
	return false
}

func isAssistantTranscriptMessage(message transcriptMessage) bool {
	return message.Type == "assistant" || message.Message.Role == "assistant"
}

func transcriptContentBlocks(raw json.RawMessage) ([]claudeContentBlock, error) {
	var blocks []claudeContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, fmt.Errorf("decode assistant content: %w", err)
	}
	return blocks, nil
}

func terminateProcess(
	process *exec.Cmd,
	processDone <-chan error,
	wait time.Duration,
) error {
	if err := process.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("stop Claude Code pty session: %w", err)
	}
	select {
	case <-processDone:
		return nil
	case <-time.After(wait):
	}
	if err := process.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("kill Claude Code pty session: %w", err)
	}
	<-processDone
	return nil
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}
