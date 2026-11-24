package ui

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/creack/pty"
)

func TestActivitySpanViewWritesStartAndEndLinesForRedirectedOutput(t *testing.T) {
	var output bytes.Buffer
	clock := &testClock{now: time.Date(2026, 11, 23, 18, 0, 0, 0, time.UTC)}
	span := NewActivitySpanView(&output, Caps{Width: 80}, clock, time.Second)
	if err := span.Start("2/3 — revising 4 finding(s)", clock.Now()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	span.Tool("Bash", "go test ./internal/ui")
	clock.now = clock.now.Add(2*time.Minute + 3*time.Second)
	if err := span.End(clock.Now(), "completed"); err != nil {
		t.Fatalf("End() error = %v", err)
	}
	got := output.String()
	if strings.ContainsAny(got, "\x1b\r") {
		t.Fatalf("redirected output = %q, want no cursor controls", got)
	}
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 2 || lines[0] != "starting 2/3 — revising 4 finding(s)" || lines[1] != "finished 2/3 — revising 4 finding(s) — completed (2m03s)" {
		t.Fatalf("span lines = %#v, want one start and one outcome/duration line", lines)
	}
}

func TestActivitySpanViewReadsMissingTerminalGeometry(t *testing.T) {
	master, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = master.Close()
		_ = terminal.Close()
	})
	if err := pty.Setsize(terminal, &pty.Winsize{Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}

	view := NewActivitySpanView(terminal, Caps{}, nil, 0)
	if view.caps.Width != 80 || view.caps.Height != 24 {
		t.Fatalf("terminal geometry = %dx%d, want 80x24", view.caps.Width, view.caps.Height)
	}
	if view.interval != DefaultActivityTickerInterval {
		t.Fatalf("ticker interval = %s, want default %s", view.interval, DefaultActivityTickerInterval)
	}
}

func TestActivitySpanViewRedrawsThenLeavesPermanentTerminalLine(t *testing.T) {
	previousTerminal := isTerminal
	isTerminal = func(fd uintptr) bool { return fd == 42 }
	t.Cleanup(func() { isTerminal = previousTerminal })
	clock := &testClock{now: time.Date(2026, 11, 23, 18, 0, 0, 0, time.UTC), tickCh: make(chan time.Time, 1)}
	terminal := &testTerminal{}
	span := NewActivitySpanView(terminal, Caps{Width: 32, Unicode: true}, clock, time.Second)
	if err := span.Start("1/2 — implementing", clock.Now()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	span.Tool("Bash", "a very long tool gist that must fit the terminal width")
	clock.now = clock.now.Add(4*time.Second + 120*time.Millisecond)
	clock.tick()
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(terminal.String(), "4.1s") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(terminal.String(), "4.1s") {
		t.Fatalf("live span = %q, want fake-clock elapsed time", terminal.String())
	}
	if err := span.End(clock.Now(), "completed"); err != nil {
		t.Fatalf("End() error = %v", err)
	}
	got := terminal.String()
	if !strings.HasSuffix(got, "\r\x1b[K1/2 — implementing (4.1s)\n") {
		t.Fatalf("terminal output = %q, want permanent duration line after clearing live region", got)
	}
	for _, line := range strings.Split(got, "\r") {
		if strings.Contains(line, "\x1b[K") {
			visible := stripANSI(strings.TrimPrefix(line, "\x1b[K"))
			if width := lipgloss.Width(visible); width > 32 {
				t.Fatalf("live line width = %d, want at most terminal width 32: %q", width, visible)
			}
		}
	}
}

func TestActivitySpanViewDefaultTerminalGolden(t *testing.T) {
	previousTerminal := isTerminal
	isTerminal = func(fd uintptr) bool { return fd == 42 }
	t.Cleanup(func() { isTerminal = previousTerminal })
	clock := &testClock{now: time.Date(2026, 11, 23, 18, 0, 0, 0, time.UTC)}
	terminal := &testTerminal{}
	span := NewActivitySpanView(terminal, Caps{Width: 80, Unicode: true}, clock, time.Second)
	if err := span.Start("1/2 — implementing", clock.Now()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	span.Tool("Bash", "go test ./...")
	ended := clock.Now().Add(2 * time.Second)
	if err := span.End(ended, "completed"); err != nil {
		t.Fatalf("End() error = %v", err)
	}
	want := "\r\x1b[K⠋ 1/2 — implementing (0.0s)" +
		"\r\x1b[K⠙ 1/2 — implementing — Bash — go test ./... (0.0s)" +
		"\r\x1b[K1/2 — implementing (2.0s)\n"
	if got := terminal.String(); got != want {
		t.Fatalf("terminal output = %q, want golden %q", got, want)
	}
}

func TestActivitySpanViewKeepsMutedColorForLiveAndPermanentLines(t *testing.T) {
	previousTerminal := isTerminal
	isTerminal = func(fd uintptr) bool { return fd == 42 }
	t.Cleanup(func() { isTerminal = previousTerminal })
	clock := &testClock{now: time.Date(2026, 11, 23, 18, 0, 0, 0, time.UTC)}
	terminal := &testTerminal{}
	span := NewActivitySpanView(terminal, Caps{Color: true, Width: 80}, clock, time.Second)
	if err := span.Start("reviewing", clock.Now()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := span.End(clock.Now().Add(time.Second), "completed"); err != nil {
		t.Fatalf("End() error = %v", err)
	}

	got := terminal.String()
	for _, line := range []string{"⠋ reviewing (0.0s)", "reviewing (1.0s)"} {
		want := activityToneStyles[0].Render(line)
		if !strings.Contains(got, want) {
			t.Errorf("terminal output = %q, want muted color around %q", got, line)
		}
	}
}

func TestActivitySpanViewFailureEndShowsOutcomeOnTerminal(t *testing.T) {
	previousTerminal := isTerminal
	isTerminal = func(fd uintptr) bool { return fd == 42 }
	t.Cleanup(func() { isTerminal = previousTerminal })
	clock := &testClock{now: time.Date(2026, 11, 23, 18, 0, 0, 0, time.UTC)}
	terminal := &testTerminal{}
	span := NewActivitySpanView(terminal, Caps{Width: 80}, clock, time.Second)
	if err := span.Start("reviewing", clock.Now()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := span.End(clock.Now().Add(time.Second), "failed"); err != nil {
		t.Fatalf("End() error = %v", err)
	}
	if !strings.HasSuffix(terminal.String(), "\r\x1b[Kreviewing — failed (1.0s)\n") {
		t.Fatalf("terminal output = %q, want failed outcome in permanent line", terminal.String())
	}
}

func TestActivitySpanPauseKeepsVerboseOutputBelowAnchoredSpan(t *testing.T) {
	previousTerminal := isTerminal
	isTerminal = func(fd uintptr) bool { return fd == 42 }
	t.Cleanup(func() { isTerminal = previousTerminal })
	clock := &testClock{now: time.Date(2026, 11, 23, 18, 0, 0, 0, time.UTC)}
	terminal := &testTerminal{}
	span := NewActivitySpanView(terminal, Caps{Width: 48, Height: 24}, clock, time.Second)
	if err := span.Start("1/1 — reviewing", clock.Now()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	stream := NewStream(terminal, Caps{Width: 48}, StreamOptions{Span: span})
	if err := stream.Assistant("Verbose prose.\n"); err != nil {
		t.Fatalf("Assistant() error = %v", err)
	}
	if err := stream.EndTurn(); err != nil {
		t.Fatalf("EndTurn() error = %v", err)
	}
	if err := span.End(clock.Now().Add(time.Second), "completed"); err != nil {
		t.Fatalf("End() error = %v", err)
	}
	got := terminal.String()
	if strings.Contains(got, "\n\n") {
		t.Fatalf("terminal output = %q, want no blank row before verbose prose", got)
	}
	if !strings.Contains(got, "(0.0s)\nVerbose prose.\n\x1b[2A\r\x1b[K") {
		t.Fatalf("terminal output = %q, want verbose prose under an anchored span", got)
	}
	if !strings.Contains(got, "\x1b[2A\r\x1b[K1/1 — reviewing (1.0s)\x1b[2B\r") {
		t.Fatalf("terminal output = %q, want final span line rewritten above verbose prose", got)
	}
}

func TestActivitySpanDoesNotRedrawAfterVerboseOutputScrollsOffScreen(t *testing.T) {
	previousTerminal := isTerminal
	isTerminal = func(fd uintptr) bool { return fd == 42 }
	t.Cleanup(func() { isTerminal = previousTerminal })
	clock := &testClock{now: time.Date(2026, 11, 23, 18, 0, 0, 0, time.UTC)}
	terminal := &testTerminal{}
	span := NewActivitySpanView(terminal, Caps{Width: 24, Height: 4}, clock, time.Second)
	if err := span.Start("reviewing", clock.Now()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	span.Pause()
	verboseOutput := strings.Repeat("verbose output line\n", 8)
	if _, err := terminal.WriteString(verboseOutput); err != nil {
		t.Fatal(err)
	}
	span.RecordOutput(verboseOutput)
	span.Resume()
	span.Tool("Bash", "latest tool")
	clock.now = clock.now.Add(time.Second)
	if err := span.End(clock.Now(), "completed"); err != nil {
		t.Fatalf("End() error = %v", err)
	}

	got := terminal.String()
	if regexp.MustCompile(`\x1b\[[0-9]+A`).MatchString(got) {
		t.Fatalf("terminal output = %q, want no cursor-up after the anchor scrolled off screen", got)
	}
	if !strings.Contains(got, "verbose output line\n") || !strings.HasSuffix(got, "verbose output line\nreviewing (1.0s)\n") {
		t.Fatalf("terminal output = %q, want all verbose rows preserved and the permanent span appended", got)
	}
}

func TestActivitySpanCountsWrappedVerboseOutput(t *testing.T) {
	previousTerminal := isTerminal
	isTerminal = func(fd uintptr) bool { return fd == 42 }
	t.Cleanup(func() { isTerminal = previousTerminal })
	clock := &testClock{now: time.Date(2026, 11, 23, 18, 0, 0, 0, time.UTC)}
	terminal := &testTerminal{}
	span := NewActivitySpanView(terminal, Caps{Width: 8, Height: 4}, clock, time.Second)
	if err := span.Start("reviewing", clock.Now()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	span.Pause()
	verboseOutput := strings.Repeat("abcdefgh", 4) + "xy"
	if _, err := terminal.WriteString(verboseOutput); err != nil {
		t.Fatal(err)
	}
	span.RecordOutput(verboseOutput)
	span.Resume()
	if err := span.End(clock.Now().Add(time.Second), "completed"); err != nil {
		t.Fatalf("End() error = %v", err)
	}

	got := terminal.String()
	if regexp.MustCompile(`\x1b\[[0-9]+A`).MatchString(got) {
		t.Fatalf("terminal output = %q, want wrapped rows to detach the old span line", got)
	}
	if !strings.HasSuffix(got, verboseOutput+"\r\nreviewing (1.0s)\n") {
		t.Fatalf("terminal output = %q, want permanent line after wrapped output", got)
	}
}

func TestActivitySpanDoesNotRedrawWhenTerminalWidthIsUnknown(t *testing.T) {
	previousTerminal := isTerminal
	isTerminal = func(fd uintptr) bool { return fd == 42 }
	t.Cleanup(func() { isTerminal = previousTerminal })
	clock := &testClock{now: time.Date(2026, 11, 23, 18, 0, 0, 0, time.UTC)}
	terminal := &testTerminal{}
	span := NewActivitySpanView(terminal, Caps{Height: 4}, clock, time.Second)
	if err := span.Start("reviewing", clock.Now()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	span.Pause()
	verboseOutput := strings.Repeat("unwrapped output that may wrap on the real terminal", 4) + "\n"
	if _, err := terminal.WriteString(verboseOutput); err != nil {
		t.Fatal(err)
	}
	span.RecordOutput(verboseOutput)
	span.Resume()
	span.Tool("Bash", "latest tool")
	if err := span.End(clock.Now().Add(time.Second), "completed"); err != nil {
		t.Fatalf("End() error = %v", err)
	}

	got := terminal.String()
	if regexp.MustCompile(`\x1b\[[0-9]+[AB]`).MatchString(got) {
		t.Fatalf("terminal output = %q, want no vertical cursor movement with unknown width", got)
	}
	if !strings.HasSuffix(got, verboseOutput+"reviewing (1.0s)\n") {
		t.Fatalf("terminal output = %q, want permanent span appended after verbose output", got)
	}
}

func TestStreamEndTurnRedrawsActivitySpanAfterFlushingTrailingProse(t *testing.T) {
	previousTerminal := isTerminal
	isTerminal = func(fd uintptr) bool { return fd == 42 }
	t.Cleanup(func() { isTerminal = previousTerminal })
	clock := &testClock{now: time.Date(2026, 11, 23, 18, 0, 0, 0, time.UTC)}
	terminal := &testTerminal{}
	span := NewActivitySpanView(terminal, Caps{Width: 48, Height: 24}, clock, time.Second)
	if err := span.Start("1/1 — reviewing", clock.Now()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	stream := NewStream(terminal, Caps{Width: 48}, StreamOptions{Span: span})
	if err := stream.Assistant("final prose without a newline"); err != nil {
		t.Fatalf("Assistant() error = %v", err)
	}
	if err := stream.EndTurn(); err != nil {
		t.Fatalf("EndTurn() error = %v", err)
	}
	clock.now = clock.now.Add(time.Second)
	if err := span.End(clock.Now(), "completed"); err != nil {
		t.Fatalf("End() error = %v", err)
	}
	got := terminal.String()
	if !strings.Contains(got, " newline\n\x1b[3A\r\x1b[K") {
		t.Fatalf("terminal output = %q, want trailing prose before the resumed live region", got)
	}
	if !strings.Contains(got, "\x1b[3A\r\x1b[K1/1 — reviewing (1.0s)\x1b[3B\r") {
		t.Fatalf("terminal output = %q, want permanent line rewritten above the prose", got)
	}
}
