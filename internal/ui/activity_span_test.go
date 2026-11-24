package ui

import (
	"bytes"
	"io"
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

func TestActivitySpanViewClearsAndPausesLiveRegionForQuestion(t *testing.T) {
	previousTerminal := isTerminal
	isTerminal = func(fd uintptr) bool { return fd == 42 }
	t.Cleanup(func() { isTerminal = previousTerminal })
	started := time.Date(2026, 11, 24, 18, 0, 0, 0, time.UTC)
	clock := &testClock{now: started}
	terminal := &testTerminal{}
	span := NewActivitySpanView(terminal, Caps{Width: 100, Height: 24, Unicode: true}, clock, time.Second)
	if err := span.Start("1/5 — implementing", started); err != nil {
		t.Fatal(err)
	}
	if err := span.PauseForQuestion(); err != nil {
		t.Fatalf("PauseForQuestion() error = %v", err)
	}
	pausedOutput := terminal.String()
	if !strings.Contains(pausedOutput, "1/5 — implementing\n") {
		t.Fatalf("paused output = %q, want a cleared live region replaced with the Role label", pausedOutput)
	}
	if _, err := io.WriteString(terminal, "    awaiting answer\n    ┃ Which retry budget?\nanswer> "); err != nil {
		t.Fatalf("write awaiting-answer child: %v", err)
	}
	questionOutput := terminal.String()
	span.draw()
	if got := terminal.String(); got != questionOutput {
		t.Fatalf("live region redrew while awaiting answer: before=%q after=%q", questionOutput, got)
	}
	if err := span.ResumeAfterQuestion(); err != nil {
		t.Fatalf("ResumeAfterQuestion() error = %v", err)
	}
	if got := terminal.String(); got == questionOutput || !strings.Contains(got[len(questionOutput):], "⠋ 1/5 — implementing") {
		t.Fatalf("resumed output = %q, want the Role spinner after the question block", got)
	}
	if err := span.End(started.Add(time.Second), "completed"); err != nil {
		t.Fatalf("End() error = %v", err)
	}
}

func TestActivitySpanViewPrintsChildEventsInTimeOrderWhenRedirected(t *testing.T) {
	var output bytes.Buffer
	started := time.Date(2026, 11, 24, 18, 0, 0, 0, time.UTC)
	clock := &testClock{now: started}
	span := NewActivitySpanView(&output, Caps{Width: 100}, clock, time.Second)
	if err := span.Start("reviewing", started); err != nil {
		t.Fatal(err)
	}
	if err := span.SubagentStarted("child-a", "Standards review", started); err != nil {
		t.Fatal(err)
	}
	if err := span.SubagentStarted("child-b", "Spec review", started.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := span.SubagentFinished("child-b", started.Add(2*time.Second), "completed"); err != nil {
		t.Fatal(err)
	}
	if err := span.SubagentFinished("child-a", started.Add(3*time.Second), "completed"); err != nil {
		t.Fatal(err)
	}
	if err := span.End(started.Add(4*time.Second), "completed"); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"starting reviewing",
		"    starting Standards review",
		"    starting Spec review",
		"    ✓ Spec review (1.0s)",
		"    ✓ Standards review (3.0s)",
		"finished reviewing — completed (4.0s)",
	}
	got := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("Activity span output = %#v, want chronological child lines %#v", got, want)
	}
}

func TestActivitySpanViewKeepsFinishedChildrenAndPrintsFinalTree(t *testing.T) {
	previousTerminal := isTerminal
	isTerminal = func(fd uintptr) bool { return fd == 42 }
	t.Cleanup(func() { isTerminal = previousTerminal })
	started := time.Date(2026, 11, 24, 18, 0, 0, 0, time.UTC)
	clock := &testClock{now: started}
	terminal := &testTerminal{}
	span := NewActivitySpanView(terminal, Caps{Width: 100, Unicode: true}, clock, time.Second)
	if err := span.Start("1/5 — reviewing", started); err != nil {
		t.Fatal(err)
	}
	if err := span.SubagentStarted("standards", "Standards review", started); err != nil {
		t.Fatal(err)
	}
	if err := span.SubagentStarted("spec", "Spec review", started.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := span.SubagentFinished("spec", started.Add(2*time.Minute), "completed"); err != nil {
		t.Fatal(err)
	}
	clock.now = started.Add(2*time.Minute + 31*time.Second)
	span.SubagentTool("standards", "Read", "internal/ui/stream.go")
	children := []activitySpanChild{
		{id: "standards", label: "Standards review", gist: "Read internal/ui/stream.go", started: started},
		{id: "spec", label: "Spec review", started: started.Add(10 * time.Second), ended: started.Add(2 * time.Minute), outcome: activityChildOutcomeCompleted},
	}
	live := renderActivitySpanFrame("1/5 — reviewing", "", 2*time.Minute+31*time.Second, 4, 100, children, clock.Now())
	wantLive := []string{
		"⠼ 1/5 — reviewing (2m31s)",
		"    ⠼ Standards review — Read internal/ui/stream.go (2m31s)",
		"    ✓ Spec review (1m50s)",
	}
	if strings.Join(live, "\n") != strings.Join(wantLive, "\n") {
		t.Fatalf("live Activity golden = %#v, want %#v", live, wantLive)
	}
	wantLiveWrite := "\x1b[2A\r\x1b[K" + strings.Join(wantLive, "\n\r\x1b[K")
	if got := terminal.String(); !strings.HasSuffix(got, wantLiveWrite) {
		t.Fatalf("terminal live region = %q, want final frame %q", got, wantLiveWrite)
	}

	ended := started.Add(3*time.Minute + 2*time.Second)
	if err := span.End(ended, "completed"); err != nil {
		t.Fatal(err)
	}
	children[0].ended = ended
	children[0].outcome = activityChildOutcomeCompleted
	permanent := permanentActivitySpanLines("1/5 — reviewing", started, ended, activityChildOutcomeCompleted, children)
	wantPermanent := []string{
		"1/5 — reviewing (3m02s)",
		"    - Standards review (3m02s)",
		"    - Spec review (1m50s)",
	}
	if strings.Join(permanent, "\n") != strings.Join(wantPermanent, "\n") {
		t.Fatalf("permanent Activity golden = %#v, want %#v", permanent, wantPermanent)
	}
	wantPermanentWrite := "\x1b[2A\r\x1b[K" + strings.Join(wantPermanent, "\n\r\x1b[K") + "\n"
	if got := terminal.String(); !strings.HasSuffix(got, wantPermanentWrite) {
		t.Fatalf("terminal permanent block = %q, want final block %q", got, wantPermanentWrite)
	}
}

func TestActivitySpanLiveRegionCollapsesFinishedChildren(t *testing.T) {
	started := time.Date(2026, 11, 24, 18, 0, 0, 0, time.UTC)
	children := []activitySpanChild{
		{id: "a", label: "Done A", started: started, ended: started.Add(time.Second), outcome: "completed"},
		{id: "b", label: "Done B", started: started, ended: started.Add(time.Second), outcome: "completed"},
		{id: "c", label: "Done C", started: started, ended: started.Add(time.Second), outcome: "completed"},
		{id: "d", label: "Open D", gist: "Read file.go", started: started},
		{id: "e", label: "Open E", started: started},
	}
	got := renderActivitySpanFrame("reviewing", "", time.Second, 0, 80, children, started.Add(5*time.Second))
	want := []string{
		"⠋ reviewing (1.0s)",
		"    ⠋ Open D — Read file.go (5.0s)",
		"    ⠙ Open E (5.0s)",
		"    +3 done",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("live Activity lines = %#v, want collapsed lines %#v", got, want)
	}
}

func TestActivitySpanPermanentBlockIncludesCollapsedChildren(t *testing.T) {
	previousTerminal := isTerminal
	isTerminal = func(fd uintptr) bool { return fd == 42 }
	t.Cleanup(func() { isTerminal = previousTerminal })
	started := time.Date(2026, 11, 24, 18, 0, 0, 0, time.UTC)
	clock := &testClock{now: started}
	terminal := &testTerminal{}
	span := NewActivitySpanView(terminal, Caps{Width: 100}, clock, time.Second)
	if err := span.Start("reviewing", started); err != nil {
		t.Fatal(err)
	}
	labels := []string{"Done A", "Done B", "Done C", "Open D", "Open E"}
	for index, label := range labels {
		if err := span.SubagentStarted(label, label, started.Add(time.Duration(index)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	for index, label := range labels[:3] {
		if err := span.SubagentFinished(label, started.Add(time.Duration(index+1)*time.Second), "completed"); err != nil {
			t.Fatal(err)
		}
	}
	if err := span.End(started.Add(20*time.Second), "completed"); err != nil {
		t.Fatal(err)
	}
	children := make([]activitySpanChild, len(labels))
	for index, label := range labels {
		children[index] = activitySpanChild{label: label, started: started.Add(time.Duration(index) * time.Second)}
		if index < 3 {
			children[index].ended = started.Add(time.Duration(index+1) * time.Second)
			children[index].outcome = activityChildOutcomeCompleted
		} else {
			children[index].ended = started.Add(20 * time.Second)
			children[index].outcome = activityChildOutcomeCompleted
		}
	}
	permanent := permanentActivitySpanLines("reviewing", started, started.Add(20*time.Second), activityChildOutcomeCompleted, children)
	wantPermanent := []string{
		"reviewing (20.0s)",
		"    - Done A (1.0s)",
		"    - Done B (1.0s)",
		"    - Done C (1.0s)",
		"    - Open D (17.0s)",
		"    - Open E (16.0s)",
	}
	if strings.Join(permanent, "\n") != strings.Join(wantPermanent, "\n") {
		t.Fatalf("permanent collapsed Activity golden = %#v, want %#v", permanent, wantPermanent)
	}
	wantPermanentWrite := "\x1b[3A\r\x1b[K" + strings.Join(wantPermanent, "\n\r\x1b[K") + "\n"
	if got := terminal.String(); !strings.HasSuffix(got, wantPermanentWrite) {
		t.Fatalf("terminal collapsed permanent block = %q, want final block %q", got, wantPermanentWrite)
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
	if !strings.HasSuffix(got, "\x1b[1A\r\x1b[K1/2 — implementing (4.1s)\n\r\x1b[K") {
		t.Fatalf("terminal output = %q, want permanent duration line after clearing live region", got)
	}
	if !strings.Contains(got, "\n\r\x1b[K  └ Bash — a very long tool gis…") {
		t.Fatalf("terminal output = %q, want the truncated tool gist below the Activity label", got)
	}
	visible := stripANSI(strings.ReplaceAll(got, "\r", "\n"))
	for _, row := range strings.Split(visible, "\n") {
		if width := lipgloss.Width(row); width > 32 {
			t.Fatalf("live row width = %d, want at most terminal width 32: %q", width, row)
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
		"\r\x1b[K⠙ 1/2 — implementing (0.0s)\n\r\x1b[K  └ Bash — go test ./..." +
		"\x1b[1A\r\x1b[K1/2 — implementing (2.0s)\n\r\x1b[K"
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
	span.Tool("Bash", "go test ./ui")
	if err := span.End(clock.Now().Add(time.Second), "completed"); err != nil {
		t.Fatalf("End() error = %v", err)
	}

	got := terminal.String()
	for _, line := range []string{"⠋ reviewing (0.0s)", "reviewing (1.0s)"} {
		want := activitySpanLabelStyle.Render(line)
		if !strings.Contains(got, want) {
			t.Errorf("terminal output = %q, want heading color around %q", got, line)
		}
	}
	if want := activityToneStyles[0].Render("  └ Bash — go test ./ui"); !strings.Contains(got, want) {
		t.Errorf("terminal output = %q, want muted color around tool gist", got)
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

func TestActivitySpanPauseKeepsToolGistBelowLabelAndAboveVerboseOutput(t *testing.T) {
	previousTerminal := isTerminal
	isTerminal = func(fd uintptr) bool { return fd == 42 }
	t.Cleanup(func() { isTerminal = previousTerminal })
	clock := &testClock{now: time.Date(2026, 11, 23, 18, 0, 0, 0, time.UTC)}
	terminal := &testTerminal{}
	span := NewActivitySpanView(terminal, Caps{Width: 80, Height: 24}, clock, time.Second)
	if err := span.Start("1/1 — reviewing", clock.Now()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	span.Tool("Bash", "go test ./ui")
	span.Pause()
	verboseOutput := "Verbose prose.\n"
	if _, err := terminal.WriteString(verboseOutput); err != nil {
		t.Fatal(err)
	}
	span.RecordOutput(verboseOutput)
	span.Resume()
	if err := span.End(clock.Now().Add(time.Second), "completed"); err != nil {
		t.Fatalf("End() error = %v", err)
	}

	got := terminal.String()
	if !strings.Contains(got, "\x1b[3A\r\x1b[K⠹ 1/1 — reviewing (0.0s)\n\r\x1b[K  └ Bash — go test ./ui\x1b[2B\r") {
		t.Fatalf("terminal output = %q, want the two-row Activity region redrawn above the cursor", got)
	}
	if !strings.Contains(got, "\nVerbose prose.\n") {
		t.Fatalf("terminal output = %q, want verbose output preserved below the Activity region", got)
	}
	if !strings.Contains(got, "\x1b[3A\r\x1b[K1/1 — reviewing (1.0s)\n\r\x1b[K\x1b[2B\r") {
		t.Fatalf("terminal output = %q, want the completed Activity line to clear its tool row", got)
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
	if !strings.HasSuffix(got, verboseOutput+"\r\nreviewi…\n") {
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
