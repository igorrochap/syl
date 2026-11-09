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
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/creack/pty"
	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/harness/claude/transcript"
)

const (
	claudeIdleEscape       = "\x1b]9;Claude is waiting for your input"
	verdictMarker          = "VERDICT:"
	questionStartMarker    = "QUESTION:\n"
	questionEndMarker      = "\nEND QUESTION"
	terminalTailLimit      = 2048
	defaultPollInterval    = 250 * time.Millisecond
	defaultStallWindow     = 3 * time.Minute
	defaultNoProgressLimit = 15 * time.Minute
	defaultTerminateWait   = 2 * time.Second
)

// PTYAdapter drives Claude Code sessions through a pseudo-terminal.
type PTYAdapter struct {
	command         string
	projectRoot     string
	homeDir         string
	pollInterval    time.Duration
	stallWindow     time.Duration
	noProgressLimit time.Duration
	terminateWait   time.Duration
}

type transcriptStartMode int

type sessionParams struct {
	transcript         transcript.Reader
	transcriptPath     string
	sessionID          string
	subagentSizes      map[string]int64
	subagentStartState subagentStartState
	seenEntries        int
	completion         harness.CompletionSignal
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
		command:         "claude",
		projectRoot:     projectRoot,
		pollInterval:    defaultPollInterval,
		stallWindow:     defaultStallWindow,
		noProgressLimit: defaultNoProgressLimit,
		terminateWait:   defaultTerminateWait,
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
	return a.start(ctx, sessionID, args, transcriptStartFresh, request.Completion)
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
	return a.start(ctx, sessionID, args, transcriptStartResume, request.Completion)
}

func (a *PTYAdapter) Attach(ctx context.Context, request harness.Request) error {
	return a.attach(ctx, request)
}

func (a *PTYAdapter) start(
	ctx context.Context,
	sessionID string,
	args []string,
	mode transcriptStartMode,
	completion harness.CompletionSignal,
) (harness.Stream, error) {
	reader := transcript.New(a.homeDir)
	transcriptPath, err := reader.Find(a.projectRoot, sessionID)
	if err != nil {
		return nil, fmt.Errorf("find Claude transcript: %w", err)
	}
	transcriptPath, err = filepath.Abs(transcriptPath)
	if err != nil {
		return nil, fmt.Errorf("resolve Claude transcript path: %w", err)
	}
	seenEntries := 0
	if mode == transcriptStartResume {
		entries, readErr := reader.Read(transcriptPath)
		if readErr != nil {
			return nil, fmt.Errorf("read Claude transcript before resume: %w", readErr)
		}
		seenEntries = len(entries)
	}
	subagentStartState, err := snapshotSubagents(reader, a.projectRoot, sessionID)
	if err != nil {
		return nil, fmt.Errorf("read Claude subagent transcripts before start: %w", err)
	}
	session := sessionParams{
		transcript:         reader,
		transcriptPath:     transcriptPath,
		sessionID:          sessionID,
		subagentSizes:      subagentStartState.sizes,
		subagentStartState: subagentStartState,
		seenEntries:        seenEntries,
		completion:         completion,
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
	output := &terminalOutput{}
	go watchClaudeTerminal(terminal, idleSignals, output)

	events <- harness.Event{Type: harness.EventSession, SessionID: session.sessionID}
	processExited, monitorErr := a.monitorSession(
		ctx,
		session,
		events,
		idleSignals,
		processDone,
		output,
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
	output *terminalOutput,
) (bool, error) {
	pollInterval, stallWindow, noProgressLimit := a.sessionTiming()
	poll := time.NewTicker(pollInterval)
	defer poll.Stop()
	noProgress := time.NewTimer(noProgressLimit)
	defer noProgress.Stop()
	transcripts := transcriptMonitor{
		reader:      session.transcript,
		path:        session.transcriptPath,
		projectRoot: a.projectRoot,
		sessionID:   session.sessionID,
		readState: func() (SessionState, error) {
			return a.ReadSessionStateSince(session.sessionID, session.seenEntries)
		},
		subagentSizes:      session.subagentSizes,
		subagentStartState: session.subagentStartState,
		output:             output,
		events:             events,
		noProgress:         noProgress,
		stallWindow:        stallWindow,
		noProgressLimit:    noProgressLimit,
		seenEntries:        session.seenEntries,
		completion:         session.completion,
	}

	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case waitErr := <-processDone:
			return true, transcripts.checkProcessExit(waitErr)
		case <-poll.C:
			// Polling also catches a terminal escape that arrived before the
			// transcript reached disk, or never arrived at all.
			finished, err := transcripts.checkCompletion()
			if finished {
				return false, err
			}
		case <-idleSignals:
			finished, err := transcripts.checkCompletion()
			if finished {
				return false, err
			}
		case <-noProgress.C:
			finished, err := transcripts.checkNoProgressLimit()
			if finished {
				return false, err
			}
		}
	}
}

func (a *PTYAdapter) sessionTiming() (time.Duration, time.Duration, time.Duration) {
	pollInterval := a.pollInterval
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}
	noProgressLimit := a.noProgressLimit
	if noProgressLimit <= 0 {
		noProgressLimit = defaultNoProgressLimit
	}
	stallWindow := a.stallWindow
	if stallWindow <= 0 {
		stallWindow = defaultStallWindow
	}
	return pollInterval, stallWindow, noProgressLimit
}

type transcriptProgress struct {
	hasProgress      bool
	mainEntry        bool
	terminalActivity bool
	complete         bool
}

type transcriptMonitor struct {
	reader             transcript.Reader
	path               string
	projectRoot        string
	sessionID          string
	readState          func() (SessionState, error)
	subagentSizes      map[string]int64
	subagentStartState subagentStartState
	output             *terminalOutput
	events             chan<- harness.Event
	seenEntries        int
	completion         harness.CompletionSignal
	// Completion stays latched across later polls that contain no new entries.
	complete              bool
	noProgress            *time.Timer
	stallWindow           time.Duration
	stallSince            time.Time
	noProgressLimit       time.Duration
	terminalActivityCount int64
}

func (m *transcriptMonitor) checkProcessExit(waitErr error) error {
	progress, err := m.checkProgress()
	if err != nil {
		return m.describeSessionFailure(err)
	}
	if progress.complete {
		return nil
	}
	if waitErr != nil {
		return fmt.Errorf("claude code exited unsuccessfully: %w", waitErr)
	}
	return m.describeSessionFailure(fmt.Errorf("claude code exited before producing a %s", m.completion))
}

func (m *transcriptMonitor) checkCompletion() (bool, error) {
	progress, err := m.checkProgress()
	if err != nil {
		return true, m.describeSessionFailure(err)
	}
	if progress.complete {
		return true, nil
	}
	return m.checkStalledSession(progress, time.Now().UTC())
}

func (m *transcriptMonitor) checkNoProgressLimit() (bool, error) {
	progress, err := m.checkProgress()
	if err != nil {
		return true, m.describeSessionFailure(err)
	}
	if progress.complete {
		return true, nil
	}
	stalled, stallErr := m.checkStalledSession(progress, time.Now().UTC())
	if stalled || stallErr != nil {
		return true, stallErr
	}
	if progress.hasProgress {
		return false, nil
	}
	stateSummary := "unavailable"
	if m.readState != nil {
		state, stateErr := m.readState()
		if stateErr == nil {
			stateSummary = state.String()
		} else {
			stateSummary = "unavailable: " + stateErr.Error()
		}
	}
	limitError := &harness.SessionStalledError{
		SessionID:    m.sessionID,
		Limit:        harness.NoProgressLimit,
		Completion:   m.completion.String(),
		StateSummary: stateSummary,
		Reports:      readFinishedSubagentReports(m.reader, m.projectRoot, m.sessionID, m.path, m.subagentStartState),
	}
	return true, m.describeSessionFailure(limitError)
}

func (m *transcriptMonitor) checkStalledSession(progress transcriptProgress, now time.Time) (bool, error) {
	// The queued-subagent pattern is checked only for review sessions. Implementer
	// sessions use the no-progress limit until their own detector is added.
	if m.completion != harness.CompletionReviewVerdict {
		m.stallSince = time.Time{}
		return false, nil
	}
	if progress.mainEntry {
		m.stallSince = time.Time{}
		return false, nil
	}
	if m.readState == nil {
		m.stallSince = time.Time{}
		return false, nil
	}
	state, err := m.readState()
	if err != nil {
		m.stallSince = time.Time{}
		// Defer state read failures to the no-progress limit so recovery receives
		// a typed stall error with a tolerant summary.
		return false, nil
	}
	if !sessionStateCanStall(state) {
		m.stallSince = time.Time{}
		return false, nil
	}
	if m.stallSince.IsZero() {
		m.stallSince = now
		return false, nil
	}
	if now.Sub(m.stallSince) < m.stallWindow {
		return false, nil
	}
	stalled := &harness.SessionStalledError{
		SessionID:    m.sessionID,
		Limit:        harness.StallWindowLimit,
		StateSummary: state.String(),
		Reports:      readFinishedSubagentReports(m.reader, m.projectRoot, m.sessionID, m.path, m.subagentStartState),
	}
	return true, m.describeSessionFailure(stalled)
}

func sessionStateCanStall(state SessionState) bool {
	// The queued-subagent pattern requires a pending notification and finished subagents.
	hasPendingTaskNotification := state.EnqueuedTaskNotifications > state.DequeuedOrRemovedTaskNotifications
	hasFinishedSubagents := state.SubagentCount > 0 && state.SubagentCount == state.FinishedSubagentCount
	return hasPendingTaskNotification && hasFinishedSubagents
}

func (m *transcriptMonitor) describeSessionFailure(err error) error {
	status := ""
	if _, statErr := os.Stat(m.path); errors.Is(statErr, os.ErrNotExist) {
		status = " (missing: file does not exist)"
	}
	var stalled *harness.SessionStalledError
	stateSummary := ""
	if !errors.As(err, &stalled) {
		stateSummary = "Session state summary unavailable"
		if m.readState != nil {
			state, stateErr := m.readState()
			if stateErr == nil {
				stateSummary = "Session state:\n" + state.String()
			} else {
				stateSummary += ": " + stateErr.Error()
			}
		}
	}
	details := fmt.Sprintf("Session id: %s\nSession transcript: %s%s",
		m.sessionID, m.path, status)
	if stateSummary != "" {
		details += "\n" + stateSummary
	}
	if m.output != nil {
		if tail := m.output.describeTail(); tail != "" {
			details += "\n" + tail
		}
	}
	return fmt.Errorf("%w\n%s", err, details)
}

func (m *transcriptMonitor) checkProgress() (transcriptProgress, error) {
	entryCount, complete, err := readTranscript(m.reader, m.path, m.seenEntries, m.events, m.completion)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return transcriptProgress{}, err
		}
		entryCount = m.seenEntries
	}
	newMainEntries := entryCount > m.seenEntries
	if newMainEntries {
		m.seenEntries = entryCount
	}
	if complete {
		m.complete = true
	}
	newSubagentActivity, err := m.checkSubagentProgress()
	if err != nil {
		return transcriptProgress{}, err
	}
	terminalActivityCount := m.terminalActivityCount
	if m.output != nil {
		terminalActivityCount = m.output.activityCountValue()
	}
	newTerminalActivity := terminalActivityCount > m.terminalActivityCount
	m.terminalActivityCount = terminalActivityCount
	progress := transcriptProgress{
		hasProgress:      newMainEntries || newSubagentActivity || newTerminalActivity,
		mainEntry:        newMainEntries,
		terminalActivity: newTerminalActivity,
		complete:         m.complete,
	}
	if progress.hasProgress {
		resetTimer(m.noProgress, m.noProgressLimit)
	}
	return progress, nil
}

func (m *transcriptMonitor) checkSubagentProgress() (bool, error) {
	sizes, err := subagentTranscriptSizes(m.reader, m.projectRoot, m.sessionID)
	if err != nil {
		return false, err
	}
	hasProgress := false
	for path, size := range sizes {
		previousSize, exists := m.subagentSizes[path]
		isNewOrGrown := !exists || size > previousSize
		if isNewOrGrown {
			hasProgress = true
		}
	}
	m.subagentSizes = sizes
	return hasProgress, nil
}

func subagentTranscriptSizes(reader transcript.Reader, projectRoot, sessionID string) (map[string]int64, error) {
	paths, err := reader.FindSubagents(projectRoot, sessionID)
	if err != nil {
		return nil, err
	}
	sizes := make(map[string]int64, len(paths))
	for _, path := range paths {
		name := filepath.Base(path)
		isAgentTranscript := strings.HasPrefix(name, "agent-") && strings.HasSuffix(name, ".jsonl")
		if !isAgentTranscript {
			continue
		}
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("stat Claude subagent transcript %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		sizes[path] = info.Size()
	}
	return sizes, nil
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

type terminalOutput struct {
	mu                sync.Mutex
	state             terminalEscapeState
	escapeSequence    []byte
	tail              []byte
	activityCount     int64
	activitySegment   []byte
	synchronizedFrame []byte
	synchronized      bool
	frameTooLarge     bool
}

type terminalEscapeState int

const (
	terminalText terminalEscapeState = iota
	terminalEscape
	terminalCSI
	terminalEscapeIntermediate
	terminalString
	terminalStringEscape
)

func (o *terminalOutput) capture(data []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	// Keep parser state across reads so split or truncated escapes never leak
	// into the bounded tail, even when their payload exceeds the tail limit.
	for _, value := range data {
		o.captureByte(value)
	}
	o.flushActivitySegment()
	if len(o.tail) > terminalTailLimit {
		start := len(o.tail) - terminalTailLimit
		copy(o.tail, o.tail[start:])
		o.tail = o.tail[:terminalTailLimit]
	}
}

func (o *terminalOutput) activityCountValue() int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.activityCount
}

func (o *terminalOutput) captureByte(value byte) {
	switch o.state {
	case terminalText:
		o.captureText(value)
	case terminalEscape:
		o.state = beginTerminalEscape(value)
		if o.state == terminalCSI {
			o.escapeSequence = o.escapeSequence[:0]
		}
	case terminalCSI:
		o.captureCSIByte(value)
	case terminalEscapeIntermediate:
		o.finishTerminalEscape(value)
	case terminalString, terminalStringEscape:
		o.finishTerminalString(value)
	}
}

func (o *terminalOutput) captureText(value byte) {
	if value == '\x1b' {
		o.state = terminalEscape
		return
	}
	if value == '\r' {
		if !o.synchronized {
			o.finishActivitySegment()
		}
		return
	}
	if value == '\n' || value == '\t' {
		if !o.synchronized {
			o.finishActivitySegment()
		}
		o.tail = append(o.tail, value)
		return
	}
	o.capturePrintable(value)
}

func (o *terminalOutput) capturePrintable(value byte) {
	if value < ' ' || value == '\x7f' {
		o.finishActivitySegment()
		return
	}
	o.tail = append(o.tail, value)
	if o.synchronized {
		if len(o.synchronizedFrame) < terminalActivitySegmentLimit {
			o.synchronizedFrame = append(o.synchronizedFrame, value)
		} else {
			o.frameTooLarge = true
		}
		return
	}
	o.activitySegment = append(o.activitySegment, value)
	if len(o.activitySegment) >= terminalActivitySegmentLimit {
		o.finishActivitySegment()
	}
}

const terminalActivitySegmentLimit = 4096

// The held-request capture shows spinner glyphs and elapsed-time counters in
// synchronized frames. Match their visible characters so cursor geometry,
// read boundaries, and missing synchronization markers do not affect progress.
func (o *terminalOutput) finishActivitySegment() {
	if len(o.activitySegment) == 0 {
		return
	}
	heartbeat, complete := capturedHeldRequestHeartbeat(o.activitySegment)
	if heartbeat {
		if complete {
			o.activitySegment = o.activitySegment[:0]
		}
		return
	}
	o.activityCount++
	o.activitySegment = o.activitySegment[:0]
}

func (o *terminalOutput) flushActivitySegment() {
	if o.synchronized {
		return
	}
	o.finishActivitySegment()
}

func (o *terminalOutput) captureCSIByte(value byte) {
	if value == '\x1b' {
		o.escapeSequence = o.escapeSequence[:0]
		o.state = terminalEscape
		return
	}
	if len(o.escapeSequence) < 8 {
		o.escapeSequence = append(o.escapeSequence, value)
	}
	if value < 0x40 || value > 0x7e {
		return
	}
	switch string(o.escapeSequence) {
	case "?2026h":
		o.finishActivitySegment()
		o.synchronized = true
		o.synchronizedFrame = o.synchronizedFrame[:0]
		o.frameTooLarge = false
	case "?2026l":
		if o.synchronized {
			if o.frameTooLarge || !isCapturedHeldRequestHeartbeat(o.synchronizedFrame) {
				o.activityCount++
			}
			o.synchronized = false
			o.synchronizedFrame = o.synchronizedFrame[:0]
			o.frameTooLarge = false
		}
	}
	o.escapeSequence = o.escapeSequence[:0]
	o.state = terminalText
}

func isCapturedHeldRequestHeartbeat(frame []byte) bool {
	heartbeat, complete := capturedHeldRequestHeartbeat(frame)
	return heartbeat && complete
}

func capturedHeldRequestHeartbeat(frame []byte) (heartbeat, complete bool) {
	for len(frame) > 0 {
		if !utf8.FullRune(frame) {
			return true, false
		}
		r, size := utf8.DecodeRune(frame)
		if !isHeldRequestHeartbeatRune(r) {
			return false, true
		}
		frame = frame[size:]
	}
	return true, true
}

func isHeldRequestHeartbeatRune(value rune) bool {
	return unicode.Is(unicode.So, value) || unicode.IsSpace(value) || value == 'h' || value == 'm' || value == 's' ||
		value == '(' || value == ')' || value >= '0' && value <= '9' || value == '·'
}

func beginTerminalEscape(value byte) terminalEscapeState {
	switch value {
	case '\x1b':
		return terminalEscape
	case '[':
		return terminalCSI
	case ']', 'P', 'X', '^', '_':
		return terminalString
	}
	if value >= 0x20 && value <= 0x2f {
		return terminalEscapeIntermediate
	}
	return terminalText
}

func (o *terminalOutput) finishTerminalEscape(value byte) {
	if value == '\x1b' {
		o.state = terminalEscape
		return
	}
	finalStart := byte(0x40)
	if o.state == terminalEscapeIntermediate {
		finalStart = 0x30
	}
	if value >= finalStart && value <= 0x7e {
		o.state = terminalText
	}
}

func (o *terminalOutput) finishTerminalString(value byte) {
	isTerminator := value == '\x07' || o.state == terminalStringEscape && value == '\\'
	if isTerminator {
		o.state = terminalText
		return
	}
	o.state = terminalString
	if value == '\x1b' {
		o.state = terminalStringEscape
	}
}

func (o *terminalOutput) describeTail() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if strings.TrimSpace(string(o.tail)) == "" {
		return ""
	}
	// A byte bound can split a UTF-8 rune at either end; omit partial runes.
	tail := bytes.ToValidUTF8(o.tail, nil)
	return fmt.Sprintf("Last terminal output (at most %d bytes):\n%s", terminalTailLimit, tail)
}

func watchClaudeTerminal(terminal io.Reader, idleSignals chan<- struct{}, output *terminalOutput) {
	marker := []byte(claudeIdleEscape)
	buffer := make([]byte, 4096)
	var tail []byte
	for {
		count, err := terminal.Read(buffer)
		if count > 0 {
			output.capture(buffer[:count])
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
	completion harness.CompletionSignal,
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
	return len(entries), latestCompletedTurnIsComplete(newEntries, completion), nil
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

func latestCompletedTurnIsComplete(entries []transcript.Entry, completion harness.CompletionSignal) bool {
	for index := len(entries) - 1; index >= 0; index-- {
		var message transcriptMessage
		if err := json.Unmarshal(entries[index].Raw, &message); err != nil {
			return false
		}
		if !isAssistantTranscriptMessage(message) || message.Message.StopReason != "end_turn" {
			continue
		}
		if completion == harness.CompletionTurnEnd {
			return true
		}
		return containsCompletionMarker(message)
	}
	return false
}

func containsCompletionMarker(message transcriptMessage) bool {
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
	return strings.Contains(text, verdictMarker) || containsQuestionBlock(text)
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
