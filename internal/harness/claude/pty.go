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
	claudeIdleEscape    = "\x1b]9;Claude is waiting for your input"
	verdictMarker       = "VERDICT:"
	questionStartMarker = "QUESTION:\n"
	questionEndMarker   = "\nEND QUESTION"
)

// PTYAdapter drives Claude Code sessions through a pseudo-terminal.
type PTYAdapter struct {
	command       string
	projectRoot   string
	homeDir       string
	pollInterval  time.Duration
	idleTimeout   time.Duration
	terminateWait time.Duration
}

type transcriptStartMode int

type sessionParams struct {
	transcript     transcript.Reader
	transcriptPath string
	sessionID      string
	seenEntries    int
}

type ptyStream struct {
	stream    processStream
	sessionID string
}

const (
	transcriptStartFresh transcriptStartMode = iota
	transcriptStartResume
)

var _ harness.Adapter = (*PTYAdapter)(nil)
var _ harness.SessionStream = ptyStream{}

// New returns a Claude Code adapter that runs terminal sessions and reads their transcripts.
func New(projectRoot string) *PTYAdapter {
	return &PTYAdapter{
		command:       "claude",
		projectRoot:   projectRoot,
		pollInterval:  250 * time.Millisecond,
		idleTimeout:   5 * time.Minute,
		terminateWait: 2 * time.Second,
	}
}

func (a *PTYAdapter) Run(ctx context.Context, request harness.Request) (harness.Stream, error) {
	if err := checkNotChildSession(); err != nil {
		return nil, err
	}
	sessionID, err := newSessionID()
	if err != nil {
		return nil, err
	}
	args, err := ptyRunArgs(sessionID, request)
	if err != nil {
		return nil, err
	}
	return a.start(ctx, sessionID, args, transcriptStartFresh)
}

func (a *PTYAdapter) Resume(
	ctx context.Context,
	sessionID string,
	request harness.Request,
) (harness.Stream, error) {
	if err := checkNotChildSession(); err != nil {
		return nil, err
	}
	args, err := ptyResumeArgs(sessionID, request)
	if err != nil {
		return nil, err
	}
	return a.start(ctx, sessionID, args, transcriptStartResume)
}

func (a *PTYAdapter) Attach(ctx context.Context, request harness.Request) error {
	return a.attach(ctx, request)
}

func (a *PTYAdapter) start(
	ctx context.Context,
	sessionID string,
	args []string,
	mode transcriptStartMode,
) (harness.Stream, error) {
	reader := transcript.New(a.homeDir)
	transcriptPath, err := reader.Find(a.projectRoot, sessionID)
	if err != nil {
		return nil, fmt.Errorf("find Claude transcript: %w", err)
	}
	seenEntries := 0
	if mode == transcriptStartResume {
		entries, readErr := reader.Read(transcriptPath)
		if readErr != nil {
			return nil, fmt.Errorf("read Claude transcript before resume: %w", readErr)
		}
		seenEntries = len(entries)
	}
	session := sessionParams{
		transcript:     reader,
		transcriptPath: transcriptPath,
		sessionID:      sessionID,
		seenEntries:    seenEntries,
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
	go a.runSession(
		ctx,
		process,
		terminal,
		session,
		events,
		done,
	)
	return ptyStream{
		stream:    processStream{events: events, done: done},
		sessionID: sessionID,
	}, nil
}

func (s ptyStream) Events() <-chan harness.Event { return s.stream.Events() }

func (s ptyStream) Wait() error { return s.stream.Wait() }

func (s ptyStream) SessionID() string { return s.sessionID }

func (a *PTYAdapter) runSession(
	ctx context.Context,
	process *exec.Cmd,
	terminal *os.File,
	session sessionParams,
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

	events <- harness.Event{Type: harness.EventSession, SessionID: session.sessionID}
	processExited, monitorErr := a.monitorSession(
		ctx,
		session,
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
	session sessionParams,
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
		reader:      session.transcript,
		path:        session.transcriptPath,
		events:      events,
		idle:        idle,
		idleTimeout: idleTimeout,
		seenEntries: session.seenEntries,
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
			progress, readErr := transcripts.checkProgress()
			if readErr != nil {
				return false, readErr
			}
			// The poll is the only completion signal that always arrives. The
			// session never exits by itself, and the terminal escape can be
			// missed: it is emitted once, and a signal consumed before the
			// transcript reaches disk leaves nothing to wake the run again.
			if progress.complete {
				return false, nil
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
	// Completion stays latched across later polls that contain no new entries.
	complete    bool
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
	}
	if complete {
		m.complete = true
	}
	progress.complete = m.complete
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
	args := []string{"--session-id", sessionID}
	args = append(args, basePTYArgs(request)...)
	return append(args,
		"--model", request.Model,
		"--effort", effort,
		request.Prompt,
	), nil
}

func ptyResumeArgs(sessionID string, request harness.Request) ([]string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("cannot resume Claude Code session without a session id")
	}
	if strings.TrimSpace(request.Prompt) == "" {
		return nil, errors.New("cannot resume Claude Code session without a prompt")
	}
	args := []string{"--resume", sessionID}
	args = append(args, basePTYArgs(request)...)
	return append(args, request.Prompt), nil
}

func basePTYArgs(request harness.Request) []string {
	args := []string{"--permission-mode", "bypassPermissions"}
	if !request.MCP {
		args = append(args, "--strict-mcp-config")
	}
	return args
}

func checkNotChildSession() error {
	if _, childSession := os.LookupEnv("CLAUDE_CODE_CHILD_SESSION"); !childSession {
		return nil
	}
	return errors.New("cannot run Claude Code from another Claude Code session; run syl from a terminal")
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
	newEntries := entries[seenEntries:]
	for _, entry := range newEntries {
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
	return len(entries), latestCompletedTurnIsComplete(newEntries), nil
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

func latestCompletedTurnIsComplete(entries []transcript.Entry) bool {
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
		var assistantText strings.Builder
		for _, block := range blocks {
			if block.Type != "text" {
				continue
			}
			assistantText.WriteString(block.Text)
		}
		text := assistantText.String()
		if strings.Contains(text, verdictMarker) || containsQuestionBlock(text) {
			return true
		}
		return false
	}
	return false
}

func containsQuestionBlock(text string) bool {
	searchFrom := 0
	for {
		start := strings.Index(text[searchFrom:], questionStartMarker)
		if start == -1 {
			return false
		}
		start += searchFrom
		if (start == 0 || text[start-1] == '\n') && strings.Contains(
			text[start+len(questionStartMarker):],
			questionEndMarker,
		) {
			return true
		}
		searchFrom = start + len(questionStartMarker)
	}
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
