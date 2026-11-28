package ui

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// virtualTerminal models the escape sequences the live region writes. It
// models auto-wrap with a pending-wrap column and, on resize, either reflows
// wrapped rows or keeps rows and truncates them, optionally moving the cursor
// away from the frame. Its own width and the width the operating system
// reports are separate because iTerm2 reflows its screen before the program
// can read the new size.
type virtualTerminal struct {
	mu             sync.Mutex
	writes         int
	width          int
	reportedWidth  int
	height         int
	reportedHeight int
	kind           terminalKind
	rows           []virtualRow
	row            int
	column         int
	pending        bool
}

type virtualRow struct {
	cells   []rune
	wrapped bool
}

const virtualTerminalHeight = 24

// terminalKind is how an emulated terminal treats its screen on a resize.
// The cursor placement follows what real terminals did in the #319 checks:
// Terminal.app moved the cursor to the bottom row after reflowing.
type terminalKind struct {
	name                   string
	reflows                bool
	movesCursorToBottomRow bool
}

var terminalKinds = []terminalKind{
	{name: "reflowing", reflows: true},
	{name: "non-reflowing"},
	{name: "reflowing, cursor moved to bottom row", reflows: true, movesCursorToBottomRow: true},
}

func newVirtualTerminal(width int, kind terminalKind) *virtualTerminal {
	return &virtualTerminal{
		width:          width,
		reportedWidth:  width,
		height:         virtualTerminalHeight,
		reportedHeight: virtualTerminalHeight,
		kind:           kind,
		rows:           []virtualRow{{}},
	}
}

func (terminal *virtualTerminal) Fd() uintptr { return 42 }

func (terminal *virtualTerminal) Size() (int, int, error) {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	return terminal.reportedWidth, terminal.reportedHeight, nil
}

func (terminal *virtualTerminal) Write(value []byte) (int, error) {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	terminal.writes++
	terminal.interpret(stripSGR(string(value)))
	return len(value), nil
}

func stripSGR(value string) string {
	var builder strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] != '\x1b' || index+1 >= len(value) || value[index+1] != '[' {
			builder.WriteByte(value[index])
			continue
		}
		end := index + 2
		for end < len(value) && (value[end] < 0x40 || value[end] > 0x7e) {
			end++
		}
		if end < len(value) && value[end] != 'm' {
			builder.WriteString(value[index : end+1])
		}
		index = end
	}
	return builder.String()
}

func (terminal *virtualTerminal) interpret(value string) {
	runes := []rune(value)
	for index := 0; index < len(runes); index++ {
		switch runes[index] {
		case '\r':
			terminal.column, terminal.pending = 0, false
		case '\n':
			terminal.lineFeed()
		case '\x1b':
			index = terminal.control(runes, index)
		default:
			terminal.print(runes[index])
		}
	}
}

func (terminal *virtualTerminal) control(runes []rune, start int) int {
	end := start + 2
	for end < len(runes) && (runes[end] < '@' || runes[end] > '~') {
		end++
	}
	parameter, err := strconv.Atoi(string(runes[start+2 : end]))
	count := parameter
	if err != nil {
		parameter, count = 0, 1
	}
	terminal.pending = false
	switch runes[end] {
	case 'A':
		terminal.row = max(0, terminal.row-count)
	case 'B':
		terminal.row += count
		terminal.ensureRow()
	case 'C':
		terminal.column = min(terminal.width-1, terminal.column+count)
	case 'K':
		terminal.eraseRowFromCursor()
	case 'H':
		terminal.row, terminal.column = 0, 0
	case 'J':
		terminal.eraseDisplay(parameter)
	}
	return end
}

func (terminal *virtualTerminal) eraseDisplay(mode int) {
	if mode == 2 {
		for index := range terminal.rows {
			terminal.rows[index] = virtualRow{}
		}
		return
	}
	terminal.eraseRowFromCursor()
	terminal.rows = terminal.rows[:terminal.row+1]
}

func (terminal *virtualTerminal) eraseRowFromCursor() {
	current := &terminal.rows[terminal.row]
	current.cells = current.cells[:min(len(current.cells), terminal.column)]
	current.wrapped = false
}

func (terminal *virtualTerminal) lineFeed() {
	terminal.row++
	terminal.pending = false
	terminal.ensureRow()
}

func (terminal *virtualTerminal) ensureRow() {
	for len(terminal.rows) <= terminal.row {
		terminal.rows = append(terminal.rows, virtualRow{})
	}
}

func (terminal *virtualTerminal) print(value rune) {
	if terminal.pending {
		terminal.rows[terminal.row].wrapped = true
		terminal.row++
		terminal.column, terminal.pending = 0, false
		terminal.ensureRow()
		terminal.rows[terminal.row] = virtualRow{}
	}
	current := &terminal.rows[terminal.row]
	for len(current.cells) < terminal.column {
		current.cells = append(current.cells, ' ')
	}
	if terminal.column < len(current.cells) {
		current.cells[terminal.column] = value
	} else {
		current.cells = append(current.cells, value)
	}
	terminal.column++
	if terminal.column >= terminal.width {
		terminal.column, terminal.pending = terminal.width-1, true
	}
}

func (terminal *virtualTerminal) resize(width int) {
	terminal.resizeBeforeReporting(width)
	terminal.reportSize()
}

func (terminal *virtualTerminal) reportSize() {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	terminal.reportedWidth = terminal.width
	terminal.reportedHeight = terminal.height
}

func (terminal *virtualTerminal) resizeHeight(height int) {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	terminal.height = height
	terminal.reportedHeight = height
}

func (terminal *virtualTerminal) resizeBeforeReporting(width int) {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	if terminal.kind.reflows {
		terminal.reflow(width)
	} else {
		terminal.truncate(width)
	}
	terminal.width = width
	terminal.pending = false
	if terminal.kind.movesCursorToBottomRow {
		terminal.row = len(terminal.rows) + 3
		terminal.ensureRow()
	}
}

func (terminal *virtualTerminal) truncate(width int) {
	for index := range terminal.rows {
		row := &terminal.rows[index]
		row.cells = row.cells[:min(len(row.cells), width)]
		row.wrapped = false
	}
	terminal.column = min(terminal.column, width-1)
}

func (terminal *virtualTerminal) reflow(width int) {
	var rows []virtualRow
	cursorRow, cursorColumn := 0, 0
	var logical []rune
	cursorOffset := -1
	for index, row := range terminal.rows {
		if index == terminal.row {
			cursorOffset = len(logical) + terminal.column
		}
		logical = append(logical, row.cells...)
		if row.wrapped && index+1 < len(terminal.rows) {
			continue
		}
		start := len(rows)
		for offset := 0; offset == 0 || offset < len(logical); offset += width {
			chunk := logical[offset:min(len(logical), offset+width)]
			rows = append(rows, virtualRow{cells: append([]rune(nil), chunk...), wrapped: offset+width < len(logical)})
		}
		if cursorOffset >= 0 {
			cursorRow = start + min(cursorOffset/width, len(rows)-start-1)
			cursorColumn = min(cursorOffset-(cursorRow-start)*width, width-1)
			cursorOffset = -1
		}
		logical = nil
	}
	terminal.rows, terminal.row, terminal.column = rows, cursorRow, cursorColumn
}

func (terminal *virtualTerminal) screen() []string {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	var screen []string
	for _, row := range terminal.rows {
		screen = append(screen, strings.TrimRight(string(row.cells), " "))
	}
	for len(screen) > 0 && screen[len(screen)-1] == "" {
		screen = screen[:len(screen)-1]
	}
	return screen
}

type resizeHarness struct {
	t        *testing.T
	terminal *virtualTerminal
	clock    *testClock
	span     *ActivitySpanView
}

func newResizeHarness(t *testing.T, width int, kind terminalKind, options ...ActivitySpanOption) *resizeHarness {
	t.Helper()
	previousTerminal := isTerminal
	isTerminal = func(fd uintptr) bool { return fd == 42 }
	t.Cleanup(func() { isTerminal = previousTerminal })
	terminal := newVirtualTerminal(width, kind)
	clock := &testClock{now: time.Date(2026, 11, 28, 15, 0, 0, 0, time.UTC), tickCh: make(chan time.Time)}
	options = append([]ActivitySpanOption{WithTerminalSize(terminal)}, options...)
	span := NewActivitySpanView(terminal, Caps{Unicode: true, Color: true}, clock, time.Second, options...)
	return &resizeHarness{t: t, terminal: terminal, clock: clock, span: span}
}

func (harness *resizeHarness) start() {
	harness.t.Helper()
	if _, err := harness.terminal.Write([]byte(historyRow + "\n")); err != nil {
		harness.t.Fatal(err)
	}
	if err := harness.span.Start(resizeSpanLabel, harness.clock.Now()); err != nil {
		harness.t.Fatalf("Start() error = %v", err)
	}
}

func (terminal *virtualTerminal) writeCount() int {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	return terminal.writes
}

// redraw fires one ticker frame and waits until it is written, so the test
// controls exactly which frames land between resizes.
func (harness *resizeHarness) redraw() {
	harness.t.Helper()
	before := harness.terminal.writeCount()
	harness.clock.now = harness.clock.now.Add(time.Second)
	harness.clock.tick()
	deadline := time.Now().Add(time.Second)
	for harness.terminal.writeCount() == before && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if harness.terminal.writeCount() == before {
		harness.t.Fatal("ticker frame was not drawn")
	}
}

func (harness *resizeHarness) end() {
	harness.t.Helper()
	if err := harness.span.End(harness.clock.Now(), "completed"); err != nil {
		harness.t.Fatalf("End() error = %v", err)
	}
}

func (harness *resizeHarness) writeOutput(value string) {
	harness.t.Helper()
	value = "\r" + value
	if _, err := harness.terminal.Write([]byte(value)); err != nil {
		harness.t.Fatal(err)
	}
	harness.span.RecordOutput(value)
}

const (
	resizeTerminalWidth            = 26
	resizeOutcomeLineAtNarrowWidth = "1/5 — implementing the re…"
	resizeSpanLabel                = "1/5 — implementing the resize-aware live region"
)

func assertActivityOutcome(t *testing.T, screen []string) {
	t.Helper()
	if got := countRowsContaining(screen, resizeOutcomeLineAtNarrowWidth); got != 1 {
		t.Fatalf("permanent outcome rows = %d, want 1; screen = %q", got, screen)
	}
	assertRowsFit(t, screen, resizeTerminalWidth)
}

func assertResizedAnchorUnchanged(t *testing.T, before string, after []string) {
	t.Helper()
	if got := strings.Join(after, "\n"); got != before {
		t.Fatalf("screen after anchored redraw = %q, want unchanged resized screen %q", got, before)
	}
	if got := countRowsContaining(after, "1/5 —"); got != 1 {
		t.Fatalf("span label rows = %d, want 1; screen = %q", got, after)
	}
}

func countRowsContaining(screen []string, text string) int {
	count := 0
	for _, row := range screen {
		if strings.Contains(row, text) {
			count++
		}
	}
	return count
}

func assertRowsFit(t *testing.T, screen []string, width int) {
	t.Helper()
	for _, row := range screen {
		if lipgloss.Width(row) > width {
			t.Fatalf("screen row %q is %d cells wide, want at most %d; screen = %q", row, lipgloss.Width(row), width, screen)
		}
	}
}

const historyRow = "permanent history row"

// assertScreenClearedForResize pins the trade-off accepted for #319: after
// a resize the live region clears the visible screen, so earlier output is
// gone and the frame starts on the top row.
func assertScreenClearedForResize(t *testing.T, screen []string) {
	t.Helper()
	if countRowsContaining(screen, historyRow) != 0 || len(screen) == 0 || !strings.Contains(screen[0], "1/5 — ") {
		t.Fatalf("screen = %q, want the frame alone at the top after a resize", screen)
	}
}

// assertHistoryKept checks that a redraw without a resize never reaches above
// the frame.
func assertHistoryKept(t *testing.T, screen []string) {
	t.Helper()
	if len(screen) == 0 || screen[0] != historyRow {
		t.Fatalf("screen = %q, want permanent history above the live region", screen)
	}
}

func eachTerminalKind(t *testing.T, test func(t *testing.T, kind terminalKind)) {
	for _, kind := range terminalKinds {
		t.Run(kind.name, func(t *testing.T) { test(t, kind) })
	}
}

func TestActivitySpanRedrawsInPlaceAfterNarrowerResize(t *testing.T) {
	eachTerminalKind(t, func(t *testing.T, kind terminalKind) {
		harness := newResizeHarness(t, 80, kind)
		harness.start()
		harness.redraw()
		harness.terminal.resize(30)
		for range 4 {
			harness.redraw()
		}

		screen := harness.terminal.screen()
		if got := countRowsContaining(screen, "1/5 — implementing"); got != 1 {
			t.Fatalf("label rows = %d, want 1; screen = %q", got, screen)
		}
		assertRowsFit(t, screen, 30)
		assertScreenClearedForResize(t, screen)
	})
}

func TestActivitySpanShowsFullLabelAfterWiderResize(t *testing.T) {
	eachTerminalKind(t, func(t *testing.T, kind terminalKind) {
		harness := newResizeHarness(t, 30, kind)
		harness.start()
		harness.redraw()
		harness.terminal.resize(100)
		harness.redraw()
		harness.redraw()

		screen := harness.terminal.screen()
		if got := countRowsContaining(screen, resizeSpanLabel); got != 1 {
			t.Fatalf("full label rows = %d, want 1; screen = %q", got, screen)
		}
		if len(screen) != 1 {
			t.Fatalf("screen = %q, want one live row only", screen)
		}
		assertScreenClearedForResize(t, screen)
	})
}

func TestActivitySpanRedrawsMultiRowFrameInPlaceAfterResize(t *testing.T) {
	eachTerminalKind(t, func(t *testing.T, kind terminalKind) {
		harness := newResizeHarness(t, 90, kind)
		harness.start()
		harness.span.Tool("Bash", "go test ./internal/ui/... -run TestActivitySpan -count=1")
		if err := harness.span.SubagentStarted("a", "explore the live region code", harness.clock.Now()); err != nil {
			t.Fatal(err)
		}
		if err := harness.span.SubagentStarted("b", "review the terminal emulator", harness.clock.Now()); err != nil {
			t.Fatal(err)
		}
		harness.redraw()
		harness.terminal.resize(28)
		harness.redraw()
		harness.redraw()
		harness.terminal.resize(60)
		harness.redraw()
		harness.terminal.resize(24)
		harness.redraw()

		screen := harness.terminal.screen()
		for _, text := range []string{"1/5 — impl", "└ Bash", "explore", "review"} {
			if got := countRowsContaining(screen, text); got != 1 {
				t.Fatalf("rows containing %q = %d, want 1; screen = %q", text, got, screen)
			}
		}
		assertRowsFit(t, screen, 24)
		assertScreenClearedForResize(t, screen)
	})
}

func TestActivitySpanEndWritesPermanentLineAtWidthOfEnd(t *testing.T) {
	eachTerminalKind(t, func(t *testing.T, kind terminalKind) {
		harness := newResizeHarness(t, 90, kind)
		harness.start()
		harness.span.Tool("Bash", "go test ./internal/ui/... -run TestActivitySpan -count=1")
		harness.redraw()
		harness.terminal.resize(26)
		harness.end()

		screen := harness.terminal.screen()
		live := 0
		for _, frame := range activityFrames {
			live += countRowsContaining(screen, frame)
		}
		if live != 0 || countRowsContaining(screen, "└") != 0 {
			t.Fatalf("screen = %q, want no live-frame rows after End", screen)
		}
		if got := countRowsContaining(screen, "1/5 — implementing"); got != 1 {
			t.Fatalf("permanent label rows = %d, want 1; screen = %q", got, screen)
		}
		assertRowsFit(t, screen, 26)
		assertScreenClearedForResize(t, screen)
	})
}

type failingTerminalSize struct {
	width int
	err   error
}

func (size failingTerminalSize) Size() (int, int, error) { return size.width, 0, size.err }

// flakyTerminalSize reports a real width once, then fails like a terminal
// whose size query starts erroring mid-Run.
type flakyTerminalSize struct {
	mu      sync.Mutex
	first   int
	queried bool
	failure failingTerminalSize
}

func (size *flakyTerminalSize) Size() (int, int, error) {
	size.mu.Lock()
	defer size.mu.Unlock()
	if size.queried {
		return size.failure.Size()
	}
	size.queried = true
	return size.first, 24, nil
}

func TestActivitySpanKeepsLastKnownSizeWhenSizeQueryFails(t *testing.T) {
	for name, failure := range map[string]failingTerminalSize{
		"error":      {err: errors.New("no terminal")},
		"zero width": {width: 0},
	} {
		t.Run(name, func(t *testing.T) {
			size := &flakyTerminalSize{first: 40, failure: failure}
			harness := newResizeHarness(t, 40, terminalKinds[0], WithTerminalSize(size))
			harness.start()
			for range 3 {
				harness.redraw()
			}

			screen := harness.terminal.screen()
			if got := countRowsContaining(screen, "1/5 — implementing"); got != 1 {
				t.Fatalf("label rows = %d, want 1; screen = %q", got, screen)
			}
			assertRowsFit(t, screen, 40)
		})
	}
}

func TestActivitySpanRendersUntruncatedWhenNoSizeIsKnown(t *testing.T) {
	harness := newResizeHarness(t, 200, terminalKinds[0], WithTerminalSize(failingTerminalSize{err: errors.New("no terminal")}))
	harness.start()
	harness.redraw()
	harness.redraw()

	screen := harness.terminal.screen()
	if got := countRowsContaining(screen, resizeSpanLabel); got != 1 {
		t.Fatalf("full label rows = %d, want 1; screen = %q", got, screen)
	}
}

func TestActivitySpanRedrawsInPlaceWhenTerminalReflowsBeforeReportingSize(t *testing.T) {
	eachTerminalKind(t, func(t *testing.T, kind terminalKind) {
		harness := newResizeHarness(t, 90, kind)
		harness.start()
		harness.span.Tool("Bash", "go test ./internal/ui/... -run TestActivitySpan -count=1")
		if err := harness.span.SubagentStarted("a", "explore the live region code", harness.clock.Now()); err != nil {
			t.Fatal(err)
		}
		harness.redraw()
		harness.terminal.resizeBeforeReporting(28)
		harness.redraw()
		harness.redraw()
		harness.terminal.reportSize()
		harness.redraw()

		screen := harness.terminal.screen()
		for _, text := range []string{"1/5 — impl", "└ Bash", "explore"} {
			if got := countRowsContaining(screen, text); got != 1 {
				t.Fatalf("rows containing %q = %d, want 1; screen = %q", text, got, screen)
			}
		}
		assertRowsFit(t, screen, 28)
		assertScreenClearedForResize(t, screen)
	})
}

func TestActivitySpanKeepsHistoryWhenWidthDoesNotChange(t *testing.T) {
	eachTerminalKind(t, func(t *testing.T, kind terminalKind) {
		harness := newResizeHarness(t, 90, kind)
		harness.start()
		harness.span.Tool("Bash", "go test ./internal/ui/...")
		for range 3 {
			harness.redraw()
		}
		harness.end()

		screen := harness.terminal.screen()
		assertHistoryKept(t, screen)
		if got := countRowsContaining(screen, "1/5 — implementing"); got != 1 {
			t.Fatalf("label rows = %d, want 1; screen = %q", got, screen)
		}
	})
}

func TestActivitySpanResizePreservesOutputBelowAnchoredSpan(t *testing.T) {
	eachTerminalKind(t, func(t *testing.T, kind terminalKind) {
		harness := newResizeHarness(t, 90, kind)
		harness.start()
		harness.span.Pause()
		harness.writeOutput("Assistant prose remains untouched after the terminal gets narrower.")
		harness.terminal.resize(resizeTerminalWidth)

		beforeRedraw := strings.Join(harness.terminal.screen(), "\n")
		harness.span.Tool("Bash", "redraw after resize")
		harness.span.Tool("Bash", "redraw again after resize")
		afterRedraw := harness.terminal.screen()
		assertResizedAnchorUnchanged(t, beforeRedraw, afterRedraw)
		if !strings.Contains(strings.Join(afterRedraw, ""), "Assistant prose") {
			t.Fatalf("screen = %q, want the assistant prose intact", afterRedraw)
		}
	})
}

func TestActivitySpanHeightResizePreservesOutputBelowAnchoredSpan(t *testing.T) {
	harness := newResizeHarness(t, 90, terminalKinds[0])
	harness.start()
	harness.span.Pause()
	harness.writeOutput("Assistant prose remains visible after the terminal gets shorter.")
	harness.terminal.resizeHeight(5)

	beforeRedraw := strings.Join(harness.terminal.screen(), "\n")
	harness.span.Tool("Bash", "redraw after height resize")
	afterRedraw := harness.terminal.screen()
	assertResizedAnchorUnchanged(t, beforeRedraw, afterRedraw)
}

func TestActivitySpanQuestionPauseAfterResizePrintsLabelBelowProse(t *testing.T) {
	for _, test := range []struct {
		name        string
		width       int
		height      int
		endsNewline bool
	}{
		{name: "width resize mid-line", width: resizeTerminalWidth},
		{name: "width resize after newline", width: resizeTerminalWidth, endsNewline: true},
		{name: "height resize mid-line", height: 5},
		{name: "height resize after newline", height: 5, endsNewline: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newResizeHarness(t, 90, terminalKinds[0])
			harness.start()
			harness.span.Pause()
			prose := "Assistant prose before question prose-tail-marker"
			if test.endsNewline {
				prose += "\r\n"
			}
			harness.writeOutput(prose)
			if test.width > 0 {
				harness.terminal.resize(test.width)
			} else {
				harness.terminal.resizeHeight(test.height)
			}

			if err := harness.span.PauseForQuestion(); err != nil {
				t.Fatalf("PauseForQuestion() error = %v", err)
			}
			if _, err := harness.terminal.Write([]byte("\r    awaiting answer\n")); err != nil {
				t.Fatal(err)
			}

			screen := harness.terminal.screen()
			label := resizeSpanLabel
			if test.width > 0 {
				label = resizeOutcomeLineAtNarrowWidth
			}
			labelRow, proseRow, questionRow := -1, -1, -1
			for index, row := range screen {
				if row == label {
					labelRow = index
				}
				if strings.Contains(row, "prose-tail-marker") {
					proseRow = index
				}
				if strings.Contains(row, "awaiting answer") {
					questionRow = index
				}
			}
			if proseRow < 0 || labelRow <= proseRow || questionRow <= labelRow {
				t.Fatalf("screen = %q, want prose, static Role label, and awaiting answer in order", screen)
			}
			if test.endsNewline && labelRow != proseRow+1 {
				t.Fatalf("screen = %q, want no blank row before the static Role label", screen)
			}
			width := test.width
			if width == 0 {
				width = 90
			}
			assertRowsFit(t, screen, width)
			harness.end()
		})
	}
}

func TestActivitySpanEndAfterAnchoredResizePrintsOneFittingOutcome(t *testing.T) {
	eachTerminalKind(t, func(t *testing.T, kind terminalKind) {
		harness := newResizeHarness(t, 90, kind)
		harness.start()
		harness.span.Pause()
		harness.writeOutput("Assistant prose ends in the middle of this line")
		harness.terminal.resize(resizeTerminalWidth)
		harness.end()

		screen := harness.terminal.screen()
		assertActivityOutcome(t, screen)
		proseRow, outcomeRow := -1, -1
		permanentLine := resizeOutcomeLineAtNarrowWidth
		for index, row := range screen {
			if strings.Contains(row, "Assistant prose") {
				proseRow = index
			}
			if row == permanentLine {
				outcomeRow = index
			}
		}
		if proseRow < 0 || outcomeRow <= proseRow {
			t.Fatalf("screen = %q, want the permanent line after the partial prose line", screen)
		}
	})
}

func TestActivitySpanEndAfterHeightResizePrintsBelowMidLineProse(t *testing.T) {
	harness := newResizeHarness(t, 90, terminalKinds[0])
	harness.start()
	harness.span.Pause()
	harness.writeOutput("Assistant prose ends at a visible marker")
	harness.terminal.resizeHeight(5)
	harness.end()

	screen := harness.terminal.screen()
	proseRow, outcomeRow := -1, -1
	for index, row := range screen {
		if strings.Contains(row, "visible marker") {
			proseRow = index
		}
		if strings.Contains(row, "1/5 — implementing the resize-aware live region (0.0s)") {
			outcomeRow = index
		}
	}
	if proseRow < 0 || outcomeRow <= proseRow {
		t.Fatalf("screen = %q, want the outcome below the partial prose line", screen)
	}
	assertRowsFit(t, screen, 90)
}

func TestActivitySpanEndAfterWidthResizeAfterNewlineHasNoBlankRow(t *testing.T) {
	harness := newResizeHarness(t, 90, terminalKinds[0])
	harness.start()
	harness.span.Pause()
	harness.writeOutput("Assistant prose ends cleanly with a final marker\r\n")
	harness.terminal.resize(resizeTerminalWidth)
	harness.end()

	screen := harness.terminal.screen()
	proseRow, outcomeRow := -1, -1
	for index, row := range screen {
		if strings.Contains(row, "final marker") {
			proseRow = index
		}
		if row == resizeOutcomeLineAtNarrowWidth {
			outcomeRow = index
		}
	}
	if proseRow < 0 || outcomeRow != proseRow+1 {
		t.Fatalf("screen = %q, want the outcome immediately after the newline-terminated prose", screen)
	}
}

func TestActivitySpanResizeWhilePausedPreservesOutputAndEndsBelowIt(t *testing.T) {
	eachTerminalKind(t, func(t *testing.T, kind terminalKind) {
		harness := newResizeHarness(t, 90, kind)
		harness.start()
		harness.span.Pause()
		harness.writeOutput("Verbose output remains intact across a paused resize")
		harness.terminal.resize(resizeTerminalWidth)

		beforeResume := strings.Join(harness.terminal.screen(), "\n")
		harness.span.Resume()
		harness.span.Tool("Bash", "redraw after resume")
		afterResume := harness.terminal.screen()
		assertResizedAnchorUnchanged(t, beforeResume, afterResume)
		if !strings.Contains(strings.Join(afterResume, ""), "Verbose output remains") {
			t.Fatalf("screen = %q, want verbose output intact", afterResume)
		}

		harness.end()
		assertActivityOutcome(t, harness.terminal.screen())
	})
}

func TestActivitySpanQuestionResumeUsesCurrentTerminalWidth(t *testing.T) {
	harness := newResizeHarness(t, 90, terminalKinds[0])
	harness.start()
	harness.span.Pause()
	harness.writeOutput("Assistant prose before the framed question")
	harness.terminal.resize(resizeTerminalWidth)

	if err := harness.span.PauseForQuestion(); err != nil {
		t.Fatalf("PauseForQuestion() error = %v", err)
	}
	pausedScreen := harness.terminal.screen()
	if !strings.Contains(strings.Join(pausedScreen, ""), "Assistant prose before the framed question") ||
		pausedScreen[len(pausedScreen)-1] != resizeOutcomeLineAtNarrowWidth {
		t.Fatalf("screen after pausing for question = %q, want the static Role label below the prose", pausedScreen)
	}
	if _, err := harness.terminal.Write([]byte("\rFramed question?\n")); err != nil {
		t.Fatal(err)
	}
	if err := harness.span.ResumeAfterQuestion(); err != nil {
		t.Fatalf("ResumeAfterQuestion() error = %v", err)
	}

	screen := harness.terminal.screen()
	assertRowsFit(t, screen, resizeTerminalWidth)
	if len(screen) == 0 || !strings.Contains(screen[len(screen)-1], "1/5 —") {
		t.Fatalf("screen = %q, want a new live region below the question at width 26", screen)
	}
}
