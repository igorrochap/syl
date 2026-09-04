package ui

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
	"github.com/muesli/termenv"
)

// StreamOptions controls the live rendering of one harness Role.
type StreamOptions struct {
	Gutter         string
	ShowTools      bool
	Activity       bool
	Role           string
	Clock          Clock
	TickerInterval time.Duration
}

// Clock supplies time and ticker events to a live Stream. It is an interface
// so activity elapsed time can be tested without waiting on wall-clock time.
type Clock interface {
	Now() time.Time
	NewTicker(time.Duration) Ticker
}

// Ticker is the clock's periodic signal for refreshing an activity line.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

// Stream renders assistant prose and tool activity for one harness Role.
type Stream struct {
	output io.Writer
	caps   Caps
	style  styleSet
	gutter string

	showTools             bool
	pending               string
	eventBoundary         bool
	lastEventWasAssistant bool
	atLineStart           bool
	lineWidth             int
	fenced                bool
	literalLine           bool
	mu                    sync.Mutex
	activity              *activityLine
}

// NewStream constructs a live Role renderer.
func NewStream(output io.Writer, caps Caps, options StreamOptions) *Stream {
	renderer := New(output, caps)
	if options.Clock == nil {
		options.Clock = wallClock{}
	}
	if options.TickerInterval <= 0 {
		options.TickerInterval = 100 * time.Millisecond
	}
	stream := &Stream{
		output:        renderer.output,
		caps:          renderer.caps,
		style:         renderer.style,
		gutter:        streamGutter(renderer.caps, options.Gutter),
		showTools:     options.ShowTools,
		eventBoundary: true,
		atLineStart:   true,
	}
	stream.activity = newActivityLine(stream, options.Activity, options.Role, options.Clock, options.TickerInterval)
	stream.StartTurn()
	return stream
}

func streamGutter(caps Caps, gutter string) string {
	if !caps.Color || caps.Width <= 0 {
		return ""
	}
	return gutter
}

// NewLive is an alternate constructor name for callers that prefer the
// lifecycle terminology used by the command layer.
func NewLive(output io.Writer, caps Caps, options StreamOptions) *Stream {
	return NewStream(output, caps, options)
}

// Unwrap exposes the underlying writer for terminal capability detection.
func (s *Stream) Unwrap() io.Writer {
	return s.output
}

// AtLineStart reports whether the next prose or tool line starts a new line.
func (s *Stream) AtLineStart() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.atLineStart
}

// BeforeEvent finishes any activity from the previous Harness event, flushes
// a buffered partial word, and records the next event boundary.
func (s *Stream) BeforeEvent() error {
	s.activity.Stop()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.flushPendingLocked(); err != nil {
		return err
	}
	s.eventBoundary = true
	return nil
}

// Assistant appends streamed assistant prose. Complete words are rendered
// immediately while a final partial word remains buffered until the next event
// or the end of the turn.
func (s *Stream) Assistant(text string) error {
	if strings.TrimSpace(text) == "" {
		s.mu.Lock()
		s.eventBoundary = false
		s.mu.Unlock()
		return nil
	}
	s.activity.Stop()
	s.mu.Lock()
	resume := false
	defer func() {
		s.mu.Unlock()
		if resume {
			s.activity.Resume()
		}
	}()
	separatorWritten := false
	if s.eventBoundary && s.lastEventWasAssistant {
		if err := s.writeAssistantSeparatorLocked(); err != nil {
			return err
		}
		separatorWritten = true
	}
	s.eventBoundary = false
	s.lastEventWasAssistant = true
	s.pending += text
	err := s.flushCompleteWordsLocked()
	resume = err == nil && s.atLineStart && !separatorWritten
	return err
}

// Tool renders a permanent tool line or starts the terminal-only activity
// line, depending on the stream options.
func (s *Stream) Tool(name, gist string) error {
	s.activity.Stop()
	if !s.showTools {
		s.mu.Lock()
		if err := s.flushPendingLocked(); err != nil {
			s.mu.Unlock()
			return err
		}
		if err := s.ensureLineStartLocked(); err != nil {
			s.mu.Unlock()
			return err
		}
		// The activity line is transient, so preserve the previous assistant state.
		s.eventBoundary = false
		s.mu.Unlock()
		s.activity.Start(name, gist)
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.flushPendingLocked(); err != nil {
		return err
	}
	if err := s.ensureLineStartLocked(); err != nil {
		return err
	}
	line := "tool: " + name
	if gist != "" {
		line += " — " + gist
	}
	if err := s.writeLineLocked(line, s.style.label); err != nil {
		return err
	}
	s.eventBoundary = false
	s.lastEventWasAssistant = false
	return nil
}

// EndTurn flushes the final partial word and clears transient activity.
func (s *Stream) EndTurn() error {
	s.activity.EndTurn()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.flushPendingLocked(); err != nil {
		return err
	}
	if !s.atLineStart {
		if err := s.writeLocked("\n"); err != nil {
			return err
		}
		s.atLineStart = true
	}
	s.eventBoundary = true
	s.lastEventWasAssistant = false
	return nil
}

// StartTurn starts the transient activity for a new Role turn. It is safe to
// call when the stream has already started its current turn.
func (s *Stream) StartTurn() {
	s.activity.StartTurn()
}

// Write lets Stream satisfy io.Writer for wrappers that pass visible text
// through a presentation writer.
func (s *Stream) Write(value []byte) (int, error) {
	if err := s.Assistant(string(value)); err != nil {
		return 0, err
	}
	return len(value), nil
}

func (s *Stream) flushCompleteWordsLocked() error {
	boundary := lastWordBoundary(s.pending)
	if boundary == 0 {
		return nil
	}
	boundary = safeInlineBoundary(s.pending, boundary)
	if boundary == 0 {
		return nil
	}
	visible := s.pending[:boundary]
	if !endsWithLineBreak(visible) {
		trimmed := strings.TrimRightFunc(visible, unicode.IsSpace)
		if trimmed != "" {
			boundary = len(trimmed)
			visible = s.pending[:boundary]
		}
	}
	s.pending = s.pending[boundary:]
	return s.writeProseLocked(visible)
}

func (s *Stream) flushPendingLocked() error {
	if s.pending == "" {
		return nil
	}
	visible := s.pending
	s.pending = ""
	return s.writeProseLocked(visible)
}

func (s *Stream) writeAssistantSeparatorLocked() error {
	if !s.atLineStart {
		if err := s.writeLineBreakLocked(); err != nil {
			return err
		}
	}
	if err := s.writeGutterLocked(); err != nil {
		return err
	}
	return s.writeLineBreakLocked()
}

func (s *Stream) writeProseLocked(value string) error {
	lines := strings.Split(value, "\n")
	for lineIndex, line := range lines {
		isTrailingEmptyLine := lineIndex == len(lines)-1 && line == ""
		if isTrailingEmptyLine {
			continue
		}
		if err := s.writeProseLineLocked(line, lineIndex == 0, lineIndex == len(lines)-1); err != nil {
			return err
		}
	}
	return nil
}

func (s *Stream) writeProseLineLocked(line string, allowAppend, lastLine bool) error {
	if allowAppend && !s.atLineStart {
		appended, err := s.appendProseLineLocked(line)
		if err != nil {
			return err
		}
		if appended {
			return nil
		}
		line = strings.TrimLeftFunc(line, unicode.IsSpace)
	}
	s.literalLine = s.fenced || isLiteralBlockLine(line)
	wrapped := wrapLine(line, s.contentWidth())
	if err := s.writeWrappedProseLineLocked(wrapped, lastLine); err != nil {
		return err
	}
	if isFenceLine(line) {
		s.fenced = !s.fenced
	}
	return nil
}

func (s *Stream) appendProseLineLocked(line string) (bool, error) {
	if !s.canAppendLocked(line) {
		if err := s.writeLocked("\n"); err != nil {
			return false, err
		}
		s.atLineStart = true
		s.lineWidth = 0
		return false, nil
	}
	if err := s.writeInlineLocked(line); err != nil {
		return false, err
	}
	s.lineWidth += lipgloss.Width(line)
	return true, nil
}

func (s *Stream) writeWrappedProseLineLocked(parts []string, lastLine bool) error {
	for partIndex, part := range parts {
		if err := s.writeProsePartLocked(part); err != nil {
			return err
		}
		lastPart := partIndex == len(parts)-1
		if lastPart && lastLine {
			continue
		}
		if err := s.writeLineBreakLocked(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Stream) writeProsePartLocked(part string) error {
	if err := s.ensureLineStartLocked(); err != nil {
		return err
	}
	if err := s.writeGutterLocked(); err != nil {
		return err
	}
	if err := s.writeInlineLocked(part); err != nil {
		return err
	}
	s.atLineStart = false
	s.lineWidth += lipgloss.Width(part)
	return nil
}

func (s *Stream) writeLineBreakLocked() error {
	if err := s.writeLocked("\n"); err != nil {
		return err
	}
	s.atLineStart = true
	s.lineWidth = 0
	return nil
}

func (s *Stream) contentWidth() int {
	if s.caps.Width <= 0 {
		return 0
	}
	return s.caps.Width - lipgloss.Width(s.gutter)
}

func (s *Stream) writeInlineLocked(value string) error {
	if !s.caps.Color || s.literalLine {
		return s.writeLocked(value)
	}
	for _, part := range inlineParts(value, s.style) {
		if err := s.writeLocked(part.style.Render(part.text)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Stream) writeLineLocked(value string, style lipgloss.Style) error {
	if err := s.writeGutterLocked(); err != nil {
		return err
	}
	if err := s.writeLocked(style.Render(value)); err != nil {
		return err
	}
	if err := s.writeLocked("\n"); err != nil {
		return err
	}
	s.atLineStart = true
	s.lineWidth = 0
	return nil
}

func (s *Stream) writeGutterLocked() error {
	if s.gutter == "" {
		return nil
	}
	style := lipgloss.NewStyle()
	if s.caps.Color {
		style = s.style.muted
	}
	if err := s.writeLocked(style.Render(s.gutter)); err != nil {
		return err
	}
	s.lineWidth = 0
	return nil
}

func (s *Stream) ensureLineStartLocked() error {
	if s.atLineStart {
		return nil
	}
	if err := s.writeLocked("\n"); err != nil {
		return err
	}
	s.atLineStart = true
	s.lineWidth = 0
	return nil
}

func (s *Stream) canAppendLocked(value string) bool {
	if s.caps.Width <= 0 {
		return true
	}
	return s.lineWidth+lipgloss.Width(value) <= s.contentWidth()
}

func (s *Stream) writeLocked(value string) error {
	written, err := io.WriteString(s.output, value)
	if err != nil {
		return fmt.Errorf("write live output: %w", err)
	}
	if written != len(value) {
		return io.ErrShortWrite
	}
	return nil
}

func lastWordBoundary(value string) int {
	boundary := 0
	for index, r := range value {
		if unicode.IsSpace(r) {
			boundary = index + len(string(r))
		}
	}
	return boundary
}

func endsWithLineBreak(value string) bool {
	return strings.HasSuffix(value, "\n") || strings.HasSuffix(value, "\r")
}

func safeInlineBoundary(value string, boundary int) int {
	for boundary > 0 {
		open, found := unclosedInlineMarker(value[:boundary])
		if !found {
			return boundary
		}
		boundary = lastWordBoundary(value[:open])
	}
	return 0
}

type inlinePart struct {
	text  string
	style lipgloss.Style
}

func inlineParts(value string, styles styleSet) []inlinePart {
	if value == "" {
		return nil
	}
	parts := make([]inlinePart, 0, 3)
	if isLiteralBlockLine(value) {
		return []inlinePart{{text: value, style: lipgloss.NewStyle()}}
	}
	if strings.HasPrefix(value, "- ") {
		parts = append(parts, inlinePart{text: "- ", style: styles.heading})
		value = value[2:]
	}
	for len(value) > 0 {
		markerIndex := firstInlineMarker(value)
		if markerIndex > 0 {
			parts = appendInlinePart(parts, value[:markerIndex], lipgloss.NewStyle())
			value = value[markerIndex:]
			continue
		}
		marker, style, markerLength := inlineMarker(value, styles)
		if marker == "" {
			return appendInlinePart(parts, value, lipgloss.NewStyle())
		}
		closing := strings.Index(value[markerLength:], marker)
		if closing == -1 {
			parts = appendInlinePart(parts, marker, lipgloss.NewStyle())
			value = value[markerLength:]
			continue
		}
		contentStart := markerLength
		content := value[contentStart : contentStart+closing]
		if content == "" {
			parts = appendInlinePart(parts, marker, lipgloss.NewStyle())
			value = value[markerLength:]
			continue
		}
		parts = appendInlinePart(parts, content, style)
		value = value[contentStart+closing+markerLength:]
	}
	return parts
}

func inlineMarker(value string, styles styleSet) (string, lipgloss.Style, int) {
	if strings.HasPrefix(value, "`") {
		return "`", styles.code, 1
	}
	if strings.HasPrefix(value, "**") {
		return "**", styles.bold, 2
	}
	if strings.HasPrefix(value, "*") {
		return "*", styles.italic, 1
	}
	if strings.HasPrefix(value, "__") {
		return "__", styles.bold, 2
	}
	if strings.HasPrefix(value, "_") {
		return "_", styles.italic, 1
	}
	return "", lipgloss.NewStyle(), 0
}

func firstInlineMarker(value string) int {
	for index, r := range value {
		switch r {
		case '`', '*', '_':
			return index
		}
	}
	return -1
}

func appendInlinePart(parts []inlinePart, text string, style lipgloss.Style) []inlinePart {
	if text == "" {
		return parts
	}
	return append(parts, inlinePart{text: text, style: style})
}

func isLiteralBlockLine(value string) bool {
	trimmed := strings.TrimLeftFunc(value, unicode.IsSpace)
	if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
		return true
	}
	if strings.HasPrefix(trimmed, "|") || strings.Contains(trimmed, " | ") {
		return true
	}
	return isATXHeading(trimmed)
}

func isATXHeading(value string) bool {
	if value == "" || value[0] != '#' {
		return false
	}
	index := 0
	for index < len(value) && value[index] == '#' {
		index++
	}
	return index < len(value) && unicode.IsSpace(rune(value[index]))
}

func isFenceLine(value string) bool {
	trimmed := strings.TrimLeftFunc(value, unicode.IsSpace)
	return strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
}

func unclosedInlineMarker(value string) (int, bool) {
	for index := 0; index < len(value); index++ {
		marker := ""
		if strings.HasPrefix(value[index:], "**") {
			marker = "**"
		} else if value[index] == '`' || value[index] == '*' || value[index] == '_' {
			marker = value[index : index+1]
		}
		if marker == "" {
			continue
		}
		closing := strings.Index(value[index+len(marker):], marker)
		if closing == -1 {
			return index, true
		}
		index += len(marker) + closing
	}
	return 0, false
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

func (wallClock) NewTicker(interval time.Duration) Ticker {
	return wallTicker{ticker: time.NewTicker(interval)}
}

type wallTicker struct{ ticker *time.Ticker }

func (t wallTicker) C() <-chan time.Time { return t.ticker.C }

func (t wallTicker) Stop() { t.ticker.Stop() }

type activityLine struct {
	stream   *Stream
	terminal io.Writer
	enabled  bool
	clock    Clock
	interval time.Duration
	role     string

	mu         sync.Mutex
	active     bool
	turnActive bool
	// label is the last action syl knows about, not a live tool status. There
	// is no tool-completion event, so a tool label may outlive the tool itself.
	label   string
	gist    string
	started time.Time
	frame   int
	stopCh  chan struct{}
	doneCh  chan struct{}
}

func newActivityLine(stream *Stream, enabled bool, role string, clock Clock, interval time.Duration) *activityLine {
	terminal, terminalOutput := terminalWriter(stream.output)
	return &activityLine{
		stream:   stream,
		enabled:  enabled && terminalOutput,
		clock:    clock,
		interval: interval,
		terminal: terminal,
		role:     activityRoleLabel(role),
	}
}

func (a *activityLine) StartTurn() {
	if !a.enabled {
		return
	}
	a.mu.Lock()
	if a.turnActive {
		a.mu.Unlock()
		return
	}
	a.turnActive = true
	a.label = a.role
	a.gist = ""
	a.started = a.clock.Now()
	a.frame = 0
	stopCh, doneCh := a.activateLocked()
	a.mu.Unlock()
	a.start(stopCh, doneCh)
}

func (a *activityLine) Start(label, gist string) {
	if !a.enabled {
		return
	}
	a.mu.Lock()
	if !a.turnActive {
		a.turnActive = true
		a.started = a.clock.Now()
		a.frame = 0
	}
	a.label = label
	a.gist = gist
	stopCh, doneCh := a.activateLocked()
	active := stopCh == nil
	a.mu.Unlock()
	if active {
		a.draw()
		return
	}
	a.start(stopCh, doneCh)
}

func (a *activityLine) Resume() {
	a.Start(a.role, "")
}

func (a *activityLine) activateLocked() (chan struct{}, chan struct{}) {
	if a.active {
		return nil, nil
	}
	a.active = true
	a.stopCh = make(chan struct{})
	a.doneCh = make(chan struct{})
	return a.stopCh, a.doneCh
}

func (a *activityLine) start(stopCh, doneCh chan struct{}) {
	a.draw()
	if stopCh == nil {
		return
	}
	go a.run(stopCh, doneCh)
}

func (a *activityLine) run(stopCh chan struct{}, doneCh chan struct{}) {
	defer close(doneCh)
	ticker := a.clock.NewTicker(a.interval)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C():
			a.draw()
		}
	}
}

func (a *activityLine) draw() {
	a.mu.Lock()
	if !a.active {
		a.mu.Unlock()
		return
	}
	label, gist, started, frame := a.label, a.gist, a.started, a.frame
	a.frame++
	a.mu.Unlock()
	line := renderActivityFrame(label, gist, a.clock.Now().Sub(started), frame)
	if !a.stream.caps.Color {
		line = removeANSI(line)
	}
	a.stream.mu.Lock()
	defer a.stream.mu.Unlock()
	_, _ = fmt.Fprintf(a.terminal, "\r\x1b[K%s", line)
}

func (a *activityLine) Stop() {
	if !a.enabled {
		return
	}
	a.mu.Lock()
	if !a.active {
		a.mu.Unlock()
		return
	}
	a.active = false
	stopCh, doneCh := a.stopCh, a.doneCh
	a.mu.Unlock()
	close(stopCh)
	<-doneCh
	a.stream.mu.Lock()
	defer a.stream.mu.Unlock()
	_, _ = fmt.Fprint(a.terminal, "\r\x1b[K")
}

func (a *activityLine) EndTurn() {
	a.Stop()
	a.mu.Lock()
	a.turnActive = false
	a.mu.Unlock()
}

func activityRoleLabel(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "implement":
		return "Implementing"
	case "review":
		return "Reviewing"
	default:
		return "Working"
	}
}

var activityFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const activityToneFrameCount = 5

var activityToneStyles = []lipgloss.Style{
	newActivityToneStyle("#626262"),
	newActivityToneStyle("#858585"),
}

func newActivityToneStyle(color string) lipgloss.Style {
	renderer := lipgloss.NewRenderer(io.Discard, termenv.WithProfile(termenv.TrueColor), termenv.WithUnsafe())
	renderer.SetColorProfile(termenv.TrueColor)
	return renderer.NewStyle().Foreground(lipgloss.Color(color))
}

// renderActivityFrame produces one complete transient activity line. The
// function is pure so the ticker can be tested with deterministic frame input.
func renderActivityFrame(label, gist string, elapsed time.Duration, frameIndex int) string {
	frame := activityFrames[positiveModulo(frameIndex, len(activityFrames))]
	line := frame + " " + label
	if gist != "" {
		line += " — " + shortGist(gist, 48)
	}
	line += " (" + formatElapsed(elapsed) + ")"
	tone := positiveModulo(frameIndex/activityToneFrameCount, len(activityToneStyles))
	return activityToneStyles[tone].Render(line)
}

func positiveModulo(value, modulus int) int {
	value %= modulus
	if value < 0 {
		return value + modulus
	}
	return value
}

func shortGist(value string, width int) string {
	if lipgloss.Width(value) <= width {
		return value
	}
	limit := width - 1
	if limit < 1 {
		return "…"
	}
	var builder strings.Builder
	used := 0
	for _, r := range value {
		runeWidth := lipgloss.Width(string(r))
		if used+runeWidth > limit {
			break
		}
		builder.WriteRune(r)
		used += runeWidth
	}
	return builder.String() + "…"
}

func formatElapsed(elapsed time.Duration) string {
	if elapsed < 0 {
		elapsed = 0
	}
	if elapsed < time.Minute {
		// Keep one-decimal rounding from displaying 60.0s in the seconds range.
		if elapsed >= time.Minute-50*time.Millisecond {
			return "59.9s"
		}
		return fmt.Sprintf("%.1fs", elapsed.Seconds())
	}
	if elapsed < time.Hour {
		minutes := elapsed / time.Minute
		seconds := (elapsed / time.Second) % 60
		return fmt.Sprintf("%dm%02ds", minutes, seconds)
	}
	hours := elapsed / time.Hour
	minutes := (elapsed / time.Minute) % 60
	return fmt.Sprintf("%dh%02dm", hours, minutes)
}

func removeANSI(value string) string {
	for {
		start := strings.Index(value, "\x1b[")
		if start == -1 {
			return value
		}
		end := strings.IndexByte(value[start:], 'm')
		if end == -1 {
			return value[:start]
		}
		value = value[:start] + value[start+end+1:]
	}
}

func terminalWriter(output io.Writer) (io.Writer, bool) {
	for output != nil {
		if file, ok := output.(interface{ Fd() uintptr }); ok {
			if isTerminal(file.Fd()) {
				return output, true
			}
			return nil, false
		}
		unwrapper, ok := output.(interface{ Unwrap() io.Writer })
		if !ok {
			return nil, false
		}
		output = unwrapper.Unwrap()
	}
	return nil, false
}

var isTerminal = func(fd uintptr) bool {
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}
