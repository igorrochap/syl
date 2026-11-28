package ui

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/term"
)

const activitySpanLiveChildLimit = 4

const (
	activityChildOutcomeCompleted = "completed"
	activityChildOutcomeGreen     = "green"
	activitySpanOutcomeRed        = "red"
)

type activitySpanChild struct {
	id      string
	label   string
	gist    string
	started time.Time
	ended   time.Time
	outcome string
}

func (child activitySpanChild) finished() bool {
	return !child.ended.IsZero()
}

// ActivitySpanView renders one recorded Activity span in the terminal.
type ActivitySpanView struct {
	output   io.Writer
	terminal io.Writer
	caps     Caps
	clock    Clock
	size     TerminalSize
	interval time.Duration

	mu            sync.Mutex
	renderMu      sync.Mutex
	active        bool
	paused        bool
	anchored      bool
	anchorVisible bool
	rowsBelow     int
	outputColumn  int
	renderedRows  int
	// renderedWidth is the width the last live frame was drawn at, so a
	// redraw can tell that the terminal was resized under the frame.
	renderedWidth int
	label         string
	gist          string
	children      []activitySpanChild
	childIndexes  map[string]int
	started       time.Time
	frame         int
	stopCh        chan struct{}
	doneCh        chan struct{}
	writeErr      error
}

// TerminalSize reports the current terminal geometry. The live region asks
// it before every redraw because the user can resize the terminal at any time.
// It is an interface so resize handling can be tested without a real terminal.
type TerminalSize interface {
	Size() (width, height int, err error)
}

// ActivitySpanOption customizes an ActivitySpanView.
type ActivitySpanOption interface {
	apply(*ActivitySpanView)
}

type terminalSizeOption struct {
	size TerminalSize
}

func (option terminalSizeOption) apply(view *ActivitySpanView) {
	view.size = option.size
}

// WithTerminalSize replaces the terminal size source of the live region.
func WithTerminalSize(size TerminalSize) ActivitySpanOption {
	return terminalSizeOption{size: size}
}

type fileTerminalSize struct {
	fileDescriptor uintptr
}

func (size fileTerminalSize) Size() (int, int, error) {
	return term.GetSize(size.fileDescriptor)
}

// NewActivitySpanView constructs a renderer for Activity span events.
func NewActivitySpanView(output io.Writer, caps Caps, clock Clock, interval time.Duration, options ...ActivitySpanOption) *ActivitySpanView {
	if output == nil {
		output = io.Discard
	}
	clock = normalizeClock(clock)
	interval = normalizedTickerInterval(interval)
	terminal, isTTY := terminalWriter(output)
	view := &ActivitySpanView{output: output, caps: caps, clock: clock, interval: interval}
	if !isTTY {
		return view
	}
	view.terminal = terminal
	if file, ok := terminal.(interface{ Fd() uintptr }); ok {
		view.size = fileTerminalSize{fileDescriptor: file.Fd()}
	}
	for _, option := range options {
		option.apply(view)
	}
	view.mu.Lock()
	view.refreshTerminalSizeLocked()
	view.mu.Unlock()
	return view
}

// refreshTerminalSizeLocked reads the live size, which wins over the Caps
// given at construction because those go stale on a resize. A flaky size
// source must not drop truncation mid-Run.
func (view *ActivitySpanView) refreshTerminalSizeLocked() {
	if view.size == nil {
		return
	}
	width, height, err := view.size.Size()
	if err != nil || width <= 0 {
		return
	}
	view.caps.Width = width
	if height > 0 {
		view.caps.Height = height
	}
}

func normalizedTickerInterval(interval time.Duration) time.Duration {
	if interval <= 0 {
		return DefaultActivityTickerInterval
	}
	return interval
}

// Start begins the span's live region or prints its start line when redirected.
func (view *ActivitySpanView) Start(label string, started time.Time) error {
	label = strings.TrimSpace(label)
	if label == "" {
		return fmt.Errorf("start Activity span: label is empty")
	}
	view.mu.Lock()
	if view.active {
		view.mu.Unlock()
		return fmt.Errorf("start Activity span %q: another span is active", label)
	}
	view.active = true
	view.label = label
	view.gist = ""
	view.children = nil
	view.childIndexes = make(map[string]int)
	view.started = started
	view.frame = 0
	view.paused = false
	view.resetLiveRegionLocked()
	if view.terminal == nil {
		view.mu.Unlock()
		return view.writeLine("starting " + label)
	}
	view.stopCh = make(chan struct{})
	view.doneCh = make(chan struct{})
	stopCh, doneCh := view.stopCh, view.doneCh
	view.mu.Unlock()
	view.draw()
	go view.run(stopCh, doneCh)
	return nil
}

func (view *ActivitySpanView) resetLiveRegionLocked() {
	view.anchored = false
	view.anchorVisible = false
	view.rowsBelow = 0
	view.outputColumn = 0
	view.renderedRows = 0
	view.renderedWidth = 0
}

// SubagentStarted adds a child span under the open Activity span. The caller
// supplies normalized child IDs and labels.
func (view *ActivitySpanView) SubagentStarted(id, label string, started time.Time) error {
	view.mu.Lock()
	if !view.active {
		view.mu.Unlock()
		return fmt.Errorf("start child Activity span %q: parent Activity span is closed", label)
	}
	if _, exists := view.childIndexes[id]; exists {
		view.mu.Unlock()
		return fmt.Errorf("start child Activity span %q: id is already active", id)
	}
	view.childIndexes[id] = len(view.children)
	view.children = append(view.children, activitySpanChild{id: id, label: label, started: started})
	terminal := view.terminal
	view.mu.Unlock()
	if terminal == nil {
		return view.writeLine("    starting " + label)
	}
	view.draw()
	return nil
}

// SubagentTool updates the latest tool gist for one open child span.
func (view *ActivitySpanView) SubagentTool(id, name, gist string) {
	view.mu.Lock()
	index, found := view.childIndexes[id]
	if !view.active || !found || view.children[index].finished() {
		view.mu.Unlock()
		return
	}
	tool := strings.TrimSpace(name)
	if detail := strings.TrimSpace(gist); detail != "" {
		tool += " " + detail
	}
	view.children[index].gist = tool
	view.mu.Unlock()
	view.draw()
}

// SubagentFinished closes one child span and refreshes the live region.
func (view *ActivitySpanView) SubagentFinished(id string, ended time.Time, outcome string) error {
	view.mu.Lock()
	index, found := view.childIndexes[id]
	if !view.active || !found {
		view.mu.Unlock()
		return fmt.Errorf("finish child Activity span %q: no child span is open", id)
	}
	if view.children[index].finished() {
		view.mu.Unlock()
		return nil
	}
	view.children[index].ended = ended
	view.children[index].outcome = outcome
	child := view.children[index]
	terminal := view.terminal
	view.mu.Unlock()
	if terminal == nil {
		return view.writeLine(finishedChildEventLine(child))
	}
	view.draw()
	return nil
}

// Tool updates the live span with the latest tool call gist.
func (view *ActivitySpanView) Tool(name, gist string) {
	view.mu.Lock()
	if !view.active {
		view.mu.Unlock()
		return
	}
	view.gist = strings.TrimSpace(name)
	if strings.TrimSpace(gist) != "" {
		view.gist += " — " + strings.TrimSpace(gist)
	}
	view.mu.Unlock()
	view.draw()
}

// Pause anchors the live region above verbose output and suspends redraws until Resume.
func (view *ActivitySpanView) Pause() {
	view.renderMu.Lock()
	defer view.renderMu.Unlock()
	view.mu.Lock()
	if !view.active || view.terminal == nil {
		view.mu.Unlock()
		return
	}
	firstPause := !view.anchored
	view.paused = true
	if firstPause {
		view.anchored = true
		view.rowsBelow = max(1, view.renderedRows)
		view.outputColumn = 0
		view.anchorVisible = view.caps.Width > 0 && view.caps.Height > view.rowsBelow
	}
	view.mu.Unlock()
	if firstPause {
		view.recordWriteError(view.writeTerminal("\n"))
	}
}

// PauseForQuestion clears the live frame and leaves a static Role label before
// the caller prints an awaiting-answer child span.
func (view *ActivitySpanView) PauseForQuestion() error {
	view.renderMu.Lock()
	defer view.renderMu.Unlock()
	view.mu.Lock()
	if !view.active {
		view.mu.Unlock()
		return errors.New("pause Activity span for question: no span is active")
	}
	view.paused = true
	if view.terminal == nil {
		view.mu.Unlock()
		return nil
	}
	label := view.label
	renderedRows := max(1, view.renderedRows)
	anchored := view.anchored
	rowsBelow := view.rowsBelow
	view.mu.Unlock()

	start := renderedRows - 1
	if anchored {
		start = rowsBelow
	}
	value := cursorUp(start) + "\r\x1b[K" + label + "\n"
	for range max(0, renderedRows-1) {
		value += "\r\x1b[K\n"
	}
	if err := view.writeTerminal(value); err != nil {
		return err
	}

	view.mu.Lock()
	view.resetLiveRegionLocked()
	view.mu.Unlock()
	return nil
}

// Resume redraws the live region after verbose output.
func (view *ActivitySpanView) Resume() {
	view.mu.Lock()
	if !view.active || !view.paused || view.terminal == nil {
		view.mu.Unlock()
		return
	}
	view.paused = false
	view.mu.Unlock()
	view.draw()
}

// ResumeAfterQuestion starts a fresh live region below the printed answer.
func (view *ActivitySpanView) ResumeAfterQuestion() error {
	view.renderMu.Lock()
	defer view.renderMu.Unlock()
	view.mu.Lock()
	if !view.active || !view.paused {
		view.mu.Unlock()
		return nil
	}
	view.paused = false
	view.resetLiveRegionLocked()
	view.frame = 0
	terminal := view.terminal
	view.mu.Unlock()
	if terminal == nil {
		return view.writeLine("")
	}
	view.drawLocked()
	return nil
}

// RecordOutput tracks lines written below the anchored span so redraws can
// return to the span row without moving the prose cursor.
func (view *ActivitySpanView) RecordOutput(value string) {
	view.mu.Lock()
	defer view.mu.Unlock()
	if !view.active || !view.anchored || view.terminal == nil {
		return
	}
	if view.caps.Width <= 0 {
		view.anchorVisible = false
	}
	view.advanceOutputLocked(value)
}

// End replaces the live region or start line with a permanent outcome line.
func (view *ActivitySpanView) End(ended time.Time, outcome string) error {
	view.mu.Lock()
	if !view.active {
		view.mu.Unlock()
		return fmt.Errorf("end Activity span: no span is active")
	}
	label, started := view.label, view.started
	children := view.finishOpenChildrenLocked(ended, outcome)
	view.active = false
	stopCh, doneCh := view.stopCh, view.doneCh
	terminal := view.terminal
	view.mu.Unlock()

	if terminal != nil {
		view.finishTerminalSpan(stopCh, doneCh, label, started, ended, outcome, children)
	} else {
		if err := view.writeLine(fmt.Sprintf("finished %s — %s (%s)", label, outcome, formatElapsed(ended.Sub(started)))); err != nil {
			return err
		}
	}
	view.mu.Lock()
	writeErr := view.writeErr
	view.writeErr = nil
	view.mu.Unlock()
	return writeErr
}

func (view *ActivitySpanView) finishOpenChildrenLocked(ended time.Time, outcome string) []activitySpanChild {
	for index := range view.children {
		if view.children[index].finished() {
			continue
		}
		view.children[index].ended = ended
		view.children[index].outcome = outcome
	}
	return append([]activitySpanChild(nil), view.children...)
}

func (view *ActivitySpanView) finishTerminalSpan(stopCh chan struct{}, doneCh chan struct{}, label string, started, ended time.Time, outcome string, children []activitySpanChild) {
	close(stopCh)
	<-doneCh
	view.renderMu.Lock()
	defer view.renderMu.Unlock()

	view.mu.Lock()
	anchored, anchorVisible := view.anchored, view.anchorVisible
	rowsBelow, outputColumn, renderedRows := view.rowsBelow, view.outputColumn, view.renderedRows
	view.refreshTerminalSizeLocked()
	caps := view.caps
	resized := view.resizedUnderFrameLocked()
	view.mu.Unlock()

	lines := permanentActivitySpanLines(label, started, ended, outcome, children)
	lines = truncateActivitySpanLines(lines, caps.Width)
	if caps.Color {
		lines = styleActivitySpanFrame(lines, 0, finishedActivitySpanStyle(label, outcome))
	}
	if !anchored {
		view.finishUnanchoredTerminalSpan(lines, renderedRows, resized)
		return
	}
	view.finishAnchoredTerminalSpan(lines, anchorVisible, rowsBelow, outputColumn, renderedRows)
}

func (view *ActivitySpanView) finishUnanchoredTerminalSpan(lines []string, renderedRows int, resized bool) {
	if resized {
		view.recordWriteError(view.writeTerminal(clearScreenToTop + strings.Join(lines, "\r\n") + "\r\n"))
		return
	}
	value := cursorUp(max(0, renderedRows-1)) + "\r\x1b[K" + strings.Join(lines, "\n\r\x1b[K") + "\n"
	for range max(0, renderedRows-len(lines)) {
		value += "\r\x1b[K"
	}
	view.recordWriteError(view.writeTerminal(value))
}

func (view *ActivitySpanView) finishAnchoredTerminalSpan(lines []string, anchorVisible bool, rowsBelow, outputColumn, renderedRows int) {
	if !anchorVisible {
		lineBreak := ""
		if outputColumn != 0 {
			lineBreak = "\r\n"
		}
		view.recordWriteError(view.writeTerminal(lineBreak + strings.Join(lines, "\n") + "\n"))
		return
	}
	extraRows := max(0, len(lines)-renderedRows)
	value := ""
	if extraRows > 0 {
		value += cursorUp(max(0, rowsBelow-renderedRows)) + insertLines(extraRows) + cursorUp(renderedRows)
	}
	value += cursorUp(rowsBelow) + "\r\x1b[K" + strings.Join(lines, "\n\r\x1b[K")
	clearedRows := max(0, renderedRows-len(lines))
	for range clearedRows {
		value += "\n\r\x1b[K"
	}
	value += cursorDown(max(0, rowsBelow+extraRows-max(len(lines), renderedRows)+1)) + "\r"
	view.recordWriteError(view.writeTerminal(value))
}

func (view *ActivitySpanView) run(stopCh chan struct{}, doneCh chan struct{}) {
	defer close(doneCh)
	ticker := view.clock.NewTicker(view.interval)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C():
			view.draw()
		}
	}
}

func (view *ActivitySpanView) draw() {
	view.renderMu.Lock()
	defer view.renderMu.Unlock()
	view.drawLocked()
}

func (view *ActivitySpanView) drawLocked() {
	view.mu.Lock()
	if !view.active || view.paused || view.terminal == nil {
		view.mu.Unlock()
		return
	}
	label, gist, started, frame := view.label, view.gist, view.started, view.frame
	children := append([]activitySpanChild(nil), view.children...)
	anchored, anchorVisible, rowsBelow, renderedRows := view.anchored, view.anchorVisible, view.rowsBelow, view.renderedRows
	view.frame++
	view.mu.Unlock()
	view.mu.Lock()
	view.refreshTerminalSizeLocked()
	caps := view.caps
	resized := view.resizedUnderFrameLocked()
	view.mu.Unlock()
	lines := renderActivitySpanFrame(label, gist, view.clock.Now().Sub(started), frame, caps.Width, children, view.clock.Now())
	if caps.Color {
		lines = styleActivitySpanFrame(lines, frame, activitySpanLabelStyle)
	}
	if anchored && !anchorVisible {
		view.mu.Lock()
		view.renderedRows = len(lines)
		view.mu.Unlock()
		return
	}
	var value string
	if anchored {
		extraRows := 0
		if len(children) > 0 {
			extraRows = max(0, len(lines)-renderedRows)
		}
		if extraRows > 0 {
			outputRowsBelow := max(0, rowsBelow-renderedRows)
			value = cursorUp(outputRowsBelow) + insertLines(extraRows) + cursorDown(outputRowsBelow+extraRows)
			rowsBelow += extraRows
			view.mu.Lock()
			view.rowsBelow = rowsBelow
			view.mu.Unlock()
		}
		value += anchoredActivityFrame(lines, renderedRows, rowsBelow)
	} else {
		value = unanchoredActivityFrame(lines, renderedRows, resized)
		view.mu.Lock()
		view.renderedWidth = caps.Width
		view.mu.Unlock()
	}
	view.recordWriteError(view.writeTerminal(value))
	view.mu.Lock()
	view.renderedRows = len(lines)
	view.mu.Unlock()
}

// clearScreenToTop erases the whole visible screen and moves to its top. When
// the terminal is resized under a frame, terminals reflow the frame's rows and
// move the cursor in ways no row count or cursor report can follow: iTerm2
// keeps the cursor's screen row while content shifts, and Terminal.app moves
// the cursor to the bottom row. Clearing the screen is the only redraw that
// leaves no stale frame rows; the cost, accepted for #319, is that earlier
// output visible on screen is cleared on a resize. Frames a terminal wrapped
// because it reflowed before reporting its new size are cleared with it.
const clearScreenToTop = "\x1b[H\x1b[2J"

func (view *ActivitySpanView) resizedUnderFrameLocked() bool {
	return view.renderedRows > 0 && view.renderedWidth != view.caps.Width
}

func unanchoredActivityFrame(lines []string, previousRows int, resized bool) string {
	if resized {
		return clearScreenToTop + strings.Join(lines, "\r\n")
	}
	value := cursorUp(max(0, previousRows-1)) + "\r\x1b[K" + strings.Join(lines, "\n\r\x1b[K")
	if previousRows > len(lines) {
		for range previousRows - len(lines) {
			value += "\n\r\x1b[K"
		}
		return value + cursorUp(previousRows-len(lines)) + "\r"
	}
	return value
}

func anchoredActivityFrame(lines []string, previousRows, rowsBelow int) string {
	value := cursorUp(rowsBelow) + "\r\x1b[K" + strings.Join(lines, "\n\r\x1b[K")
	occupiedRows := max(len(lines), previousRows) - 1
	for row := len(lines); row < previousRows; row++ {
		value += "\n\r\x1b[K"
	}
	return value + cursorDown(max(0, rowsBelow-occupiedRows)) + "\r"
}

func insertLines(count int) string {
	if count <= 0 {
		return ""
	}
	return fmt.Sprintf("\x1b[%dL", count)
}

func (view *ActivitySpanView) recordWriteError(err error) {
	if err == nil {
		return
	}
	view.mu.Lock()
	if view.writeErr == nil {
		view.writeErr = err
	}
	view.mu.Unlock()
}

func (view *ActivitySpanView) advanceOutputLocked(value string) {
	for index := 0; index < len(value); {
		if value[index] == '\x1b' {
			index = view.advanceANSIOutputLocked(value, index)
			continue
		}
		runeValue, size := utf8.DecodeRuneInString(value[index:])
		index += size
		view.advanceOutputRuneLocked(runeValue)
	}
}

func (view *ActivitySpanView) advanceANSIOutputLocked(value string, start int) int {
	end := skipANSISequence(value, start)
	if end > start+1 && value[start+1] == '[' && !safeOutputCSI(value, end) {
		view.anchorVisible = false
		view.outputColumn = -1
	}
	return end
}

func (view *ActivitySpanView) advanceOutputRuneLocked(runeValue rune) {
	switch runeValue {
	case '\n':
		view.advanceOutputRowLocked()
		view.outputColumn = 0
	case '\r':
		view.outputColumn = 0
	case '\b':
		if view.outputColumn > 0 {
			view.outputColumn--
		}
	case '\t':
		view.advanceOutputTabLocked()
	default:
		view.advanceOutputCharacterLocked(runeValue)
	}
}

func (view *ActivitySpanView) advanceOutputTabLocked() {
	if view.caps.Width <= 0 || view.outputColumn < 0 {
		view.outputColumn = -1
		return
	}
	tabStop := (view.outputColumn/8 + 1) * 8
	for view.outputColumn < tabStop {
		view.advanceOutputCellLocked(1)
	}
}

func (view *ActivitySpanView) advanceOutputCharacterLocked(runeValue rune) {
	runeWidth := lipgloss.Width(string(runeValue))
	if runeWidth <= 0 {
		return
	}
	if view.caps.Width <= 0 || view.outputColumn < 0 {
		view.outputColumn = -1
		return
	}
	view.advanceOutputCellLocked(runeWidth)
}

func (view *ActivitySpanView) advanceOutputCellLocked(width int) {
	if view.outputColumn >= view.caps.Width || view.outputColumn+width > view.caps.Width {
		view.advanceOutputRowLocked()
		view.outputColumn = 0
	}
	view.outputColumn += width
}

func (view *ActivitySpanView) advanceOutputRowLocked() {
	if view.caps.Height <= 0 {
		view.anchorVisible = false
		return
	}
	if view.rowsBelow < view.caps.Height {
		view.rowsBelow++
	}
	if view.rowsBelow >= view.caps.Height {
		view.anchorVisible = false
	}
}

func safeOutputCSI(value string, end int) bool {
	if end <= 0 || end > len(value) {
		return false
	}
	final := value[end-1]
	return final == 'm' || final == 'K'
}

func skipANSISequence(value string, start int) int {
	if start+1 >= len(value) {
		return start + 1
	}
	switch value[start+1] {
	case '[':
		return skipCSISequence(value, start)
	case ']':
		return skipOSCSequence(value, start)
	}
	return min(start+2, len(value))
}

func skipCSISequence(value string, start int) int {
	for index := start + 2; index < len(value); index++ {
		if value[index] >= 0x40 && value[index] <= 0x7e {
			return index + 1
		}
	}
	return min(start+2, len(value))
}

func skipOSCSequence(value string, start int) int {
	for index := start + 2; index < len(value); index++ {
		if value[index] == '\a' {
			return index + 1
		}
		if value[index] == '\x1b' && index+1 < len(value) && value[index+1] == '\\' {
			return index + 2
		}
	}
	return min(start+2, len(value))
}

func cursorUp(rows int) string {
	if rows <= 0 {
		return ""
	}
	return fmt.Sprintf("\x1b[%dA", rows)
}

func cursorDown(rows int) string {
	if rows <= 0 {
		return ""
	}
	return fmt.Sprintf("\x1b[%dB", rows)
}

func terminalActivitySpanLine(label string, duration time.Duration, outcome string) string {
	if outcome != activityChildOutcomeCompleted {
		label += " — " + outcome
	}
	return fmt.Sprintf("%s (%s)", label, formatElapsed(duration))
}

func (view *ActivitySpanView) writeLine(value string) error {
	if _, err := fmt.Fprintln(view.output, value); err != nil {
		return fmt.Errorf("write Activity span: %w", err)
	}
	return nil
}

func (view *ActivitySpanView) writeTerminal(value string) error {
	written, err := io.WriteString(view.terminal, value)
	if err != nil {
		return fmt.Errorf("write live Activity span: %w", err)
	}
	if written != len(value) {
		return io.ErrShortWrite
	}
	return nil
}

func renderActivitySpanFrame(label, gist string, elapsed time.Duration, frame, width int, children []activitySpanChild, now time.Time) []string {
	spinner := activityFrames[positiveModulo(frame, len(activityFrames))]
	timeText := " (" + formatElapsed(elapsed) + ")"
	labelPrefix := spinner + " "
	if width > 0 {
		available := width - lipgloss.Width(labelPrefix) - lipgloss.Width(timeText)
		if available <= 0 {
			return []string{truncateDisplayWidth(labelPrefix+label+timeText, width)}
		}
		label = truncateDisplayWidth(label, available)
	}
	lines := []string{labelPrefix + label + timeText}
	if gist != "" {
		lines = append(lines, truncateActivityLine("  └ "+gist, width))
	}
	visibleChildren, doneCount := liveActivityChildren(children)
	for childIndex, child := range visibleChildren {
		lines = append(lines, renderActivityChild(child, frame+childIndex, width, now))
	}
	if doneCount > 0 {
		lines = append(lines, truncateActivityLine(fmt.Sprintf("    +%d done", doneCount), width))
	}
	return lines
}

func liveActivityChildren(children []activitySpanChild) ([]activitySpanChild, int) {
	if len(children) <= activitySpanLiveChildLimit {
		return children, 0
	}
	var open []activitySpanChild
	doneCount := 0
	for _, child := range children {
		if child.finished() {
			doneCount++
			continue
		}
		open = append(open, child)
	}
	return open, doneCount
}

func renderActivityChild(child activitySpanChild, frame, width int, now time.Time) string {
	if child.finished() {
		return truncateActivityLine(finishedChildEventLine(child), width)
	}
	spinner := activityFrames[positiveModulo(frame, len(activityFrames))]
	line := fmt.Sprintf("    %s %s", spinner, child.label)
	if child.gist != "" {
		line += " — " + child.gist
	}
	line += " (" + formatElapsed(now.Sub(child.started)) + ")"
	return truncateActivityLine(line, width)
}

func truncateActivityLine(line string, width int) string {
	if width <= 0 {
		return line
	}
	return truncateDisplayWidth(line, width)
}

func permanentChildLine(child activitySpanChild) string {
	return fmt.Sprintf("    - %s (%s)", child.label, formatElapsed(child.ended.Sub(child.started)))
}

func finishedChildEventLine(child activitySpanChild) string {
	icon := "✓"
	if !successfulActivityChildOutcome(child.outcome) {
		icon = "×"
	}
	return fmt.Sprintf("    %s %s (%s)", icon, child.label, formatElapsed(child.ended.Sub(child.started)))
}

func successfulActivityChildOutcome(outcome string) bool {
	return outcome == activityChildOutcomeCompleted || outcome == activityChildOutcomeGreen
}

func permanentActivitySpanLines(label string, started, ended time.Time, outcome string, children []activitySpanChild) []string {
	lines := []string{terminalActivitySpanLine(label, ended.Sub(started), outcome)}
	for _, child := range children {
		lines = append(lines, permanentChildLine(child))
	}
	return lines
}

func truncateActivitySpanLines(lines []string, width int) []string {
	if width <= 0 {
		return lines
	}
	truncated := make([]string, len(lines))
	for index, line := range lines {
		truncated[index] = truncateDisplayWidth(line, width)
	}
	return truncated
}

func finishedActivitySpanStyle(label, outcome string) lipgloss.Style {
	switch outcome {
	case activityChildOutcomeGreen:
		return activitySpanPositiveStyle
	case activitySpanOutcomeRed:
		return activitySpanNegativeStyle
	case activityChildOutcomeCompleted:
		if strings.HasPrefix(label, "approved — ") {
			return activitySpanPositiveStyle
		}
	}
	return activitySpanLabelStyle
}

func styleActivitySpanFrame(lines []string, frame int, labelStyle lipgloss.Style) []string {
	styled := append([]string(nil), lines...)
	styled[0] = labelStyle.Render(styled[0])
	for index := 1; index < len(styled); index++ {
		tone := positiveModulo(frame/activityToneFrameCount+index-1, len(activityToneStyles))
		styled[index] = activityToneStyles[tone].Render(styled[index])
	}
	return styled
}

func truncateDisplayWidth(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(value) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	var builder strings.Builder
	used := 0
	for _, char := range value {
		runeWidth := lipgloss.Width(string(char))
		if used+runeWidth > width-1 {
			break
		}
		builder.WriteRune(char)
		used += runeWidth
	}
	return builder.String() + "…"
}
