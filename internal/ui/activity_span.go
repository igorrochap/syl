package ui

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/term"
)

// ActivitySpanView renders one recorded Activity span in the terminal.
type ActivitySpanView struct {
	output   io.Writer
	terminal io.Writer
	caps     Caps
	clock    Clock
	interval time.Duration

	mu            sync.Mutex
	renderMu      sync.Mutex
	active        bool
	paused        bool
	anchored      bool
	anchorVisible bool
	rowsBelow     int
	outputColumn  int
	label         string
	gist          string
	started       time.Time
	frame         int
	stopCh        chan struct{}
	doneCh        chan struct{}
	writeErr      error
}

// NewActivitySpanView constructs a renderer for Activity span events.
func NewActivitySpanView(output io.Writer, caps Caps, clock Clock, interval time.Duration) *ActivitySpanView {
	if output == nil {
		output = io.Discard
	}
	clock = normalizeClock(clock)
	interval = normalizedTickerInterval(interval)
	terminal, isTTY := terminalWriter(output)
	view := &ActivitySpanView{output: output, caps: caps, clock: clock, interval: interval}
	if isTTY {
		view.terminal = terminal
		if view.caps.Width <= 0 || view.caps.Height <= 0 {
			if file, ok := terminal.(interface{ Fd() uintptr }); ok {
				if width, height, err := term.GetSize(file.Fd()); err == nil {
					if view.caps.Width <= 0 && width > 0 {
						view.caps.Width = width
					}
					if view.caps.Height <= 0 && height > 0 {
						view.caps.Height = height
					}
				}
			}
		}
	}
	return view
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
	view.started = started
	view.frame = 0
	view.anchored = false
	view.rowsBelow = 0
	view.outputColumn = 0
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

// Pause anchors the live line above verbose output and suspends redraws until Resume.
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
		view.rowsBelow = 1
		view.outputColumn = 0
		view.anchorVisible = view.caps.Width > 0 && view.caps.Height > 1
	}
	view.mu.Unlock()
	if firstPause {
		view.recordWriteError(view.writeTerminal("\n"))
	}
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
	view.active = false
	stopCh, doneCh := view.stopCh, view.doneCh
	terminal := view.terminal
	view.mu.Unlock()

	if terminal != nil {
		close(stopCh)
		<-doneCh
		view.renderMu.Lock()
		view.mu.Lock()
		anchored, anchorVisible, rowsBelow, outputColumn := view.anchored, view.anchorVisible, view.rowsBelow, view.outputColumn
		view.mu.Unlock()
		line := terminalActivitySpanLine(label, ended.Sub(started), outcome)
		if anchored {
			if anchorVisible {
				view.recordWriteError(view.writeTerminal(cursorUp(rowsBelow) + "\r\x1b[K" + line + cursorDown(rowsBelow) + "\r"))
			} else {
				lineBreak := ""
				if outputColumn != 0 {
					lineBreak = "\r\n"
				}
				view.recordWriteError(view.writeTerminal(lineBreak + line + "\n"))
			}
		} else {
			view.clear()
			view.recordWriteError(view.writeTerminal(line + "\n"))
		}
		view.renderMu.Unlock()
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
	view.mu.Lock()
	if !view.active || view.paused || view.terminal == nil {
		view.mu.Unlock()
		return
	}
	label, gist, started, frame := view.label, view.gist, view.started, view.frame
	anchored, anchorVisible, rowsBelow := view.anchored, view.anchorVisible, view.rowsBelow
	view.frame++
	view.mu.Unlock()
	if anchored && !anchorVisible {
		return
	}
	line := renderActivitySpanFrame(label, gist, view.clock.Now().Sub(started), frame, view.caps.Width)
	if anchored {
		view.recordWriteError(view.writeTerminal(cursorUp(rowsBelow) + "\r\x1b[K" + line + cursorDown(rowsBelow) + "\r"))
	} else {
		view.recordWriteError(view.writeTerminal("\r\x1b[K" + line))
	}
}

func (view *ActivitySpanView) clear() {
	view.recordWriteError(view.writeTerminal("\r\x1b[K"))
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
			start := index
			index = skipANSISequence(value, index)
			if index > start+1 && value[start+1] == '[' && !safeOutputCSI(value, index) {
				view.anchorVisible = false
				view.outputColumn = -1
			}
			continue
		}
		runeValue, size := utf8.DecodeRuneInString(value[index:])
		index += size
		if runeValue == '\n' {
			view.advanceOutputRowLocked()
			view.outputColumn = 0
			continue
		}
		if runeValue == '\r' {
			view.outputColumn = 0
			continue
		}
		if runeValue == '\b' {
			if view.outputColumn > 0 {
				view.outputColumn--
			}
			continue
		}
		if runeValue == '\t' {
			if view.caps.Width <= 0 || view.outputColumn < 0 {
				view.outputColumn = -1
				continue
			}
			tabStop := (view.outputColumn/8 + 1) * 8
			for view.outputColumn < tabStop {
				view.advanceOutputCellLocked(1)
			}
			continue
		}
		runeWidth := lipgloss.Width(string(runeValue))
		if runeWidth <= 0 {
			continue
		}
		if view.caps.Width <= 0 || view.outputColumn < 0 {
			view.outputColumn = -1
			continue
		}
		view.advanceOutputCellLocked(runeWidth)
	}
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
		for index := start + 2; index < len(value); index++ {
			if value[index] >= 0x40 && value[index] <= 0x7e {
				return index + 1
			}
		}
	case ']':
		for index := start + 2; index < len(value); index++ {
			if value[index] == '\a' {
				return index + 1
			}
			if value[index] == '\x1b' && index+1 < len(value) && value[index+1] == '\\' {
				return index + 2
			}
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
	if outcome != "completed" {
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

func renderActivitySpanFrame(label, gist string, elapsed time.Duration, frame, width int) string {
	spinner := activityFrames[positiveModulo(frame, len(activityFrames))]
	timeText := " (" + formatElapsed(elapsed) + ")"
	line := spinner + " " + label
	if gist != "" {
		line += " — " + gist
	}
	line += timeText
	if width <= 0 || lipgloss.Width(line) <= width {
		return line
	}
	base := spinner + " " + label + timeText
	available := width - lipgloss.Width(base) - lipgloss.Width(" — ")
	if available <= 0 {
		return truncateDisplayWidth(base, width)
	}
	return spinner + " " + label + " — " + truncateDisplayWidth(gist, available) + timeText
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
