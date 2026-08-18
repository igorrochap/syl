package orchestration

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/igorrochap/syl/internal/harness"
)

// fakeTerminal is a writer that reports as a real terminal so spinner logic can
// be exercised without a PTY. It records everything written straight to it.
type fakeTerminal struct{ bytes.Buffer }

func (t *fakeTerminal) Fd() uintptr { return 42 }

// stubTerminalDetection makes isTerminalFd recognize fakeTerminal's descriptor.
func stubTerminalDetection(t *testing.T) {
	t.Helper()
	original := isTerminalFd
	isTerminalFd = func(fd uintptr) bool { return fd == 42 }
	t.Cleanup(func() { isTerminalFd = original })
}

func TestTerminalWriterUnwrapsRoleLabelWriter(t *testing.T) {
	stubTerminalDetection(t)
	terminal := &fakeTerminal{}
	labeled := &roleLabelWriter{output: terminal, prefix: "[implement] ", atLineStart: true}

	got, ok := terminalWriter(labeled)
	if !ok {
		t.Fatalf("terminalWriter did not find the terminal through the label writer")
	}
	if got != terminal {
		t.Fatalf("terminalWriter returned %#v, want the underlying terminal", got)
	}
}

// TestQuietRenderSpinnerBypassesLabelWriter reproduces the empty-label
// regression: the spinner must draw straight on the terminal, never through the
// role label writer, so its cursor-control escapes are not turned into bare
// "[implement] " lines.
func TestQuietRenderSpinnerBypassesLabelWriter(t *testing.T) {
	stubTerminalDetection(t)
	terminal := &fakeTerminal{}
	labeled := &roleLabelWriter{output: terminal, prefix: "[implement] ", atLineStart: true}
	sp := newSpinner(labeled)
	if !sp.enabled {
		t.Fatal("spinner should be enabled for terminal output")
	}
	if sp.output != terminal {
		t.Fatalf("spinner draws through %#v, want the raw terminal so escapes are not labeled", sp.output)
	}

	atLineStart := true
	parser := newQuestionParser()
	var pendingRaw []harness.Event
	events := []harness.Event{
		{Type: harness.EventAssistantText, Text: "Working on it"},
		{Type: harness.EventToolUse, ToolName: "edit"},
		{Type: harness.EventToolUse, ToolName: "edit"},
		{Type: harness.EventAssistantText, Text: "Done.\n"},
	}
	for _, event := range events {
		parsed := questionParseResult{}
		if event.Type == harness.EventAssistantText {
			parsed = parser.Feed(event.Text)
		}
		if err := renderHarnessEvent(labeled, QuietHarnessOutput, event, parsed, parser, &pendingRaw, sp, &atLineStart); err != nil {
			t.Fatalf("renderHarnessEvent: %v", err)
		}
	}
	sp.Stop()

	got := terminal.String()
	// Prose is labeled once per line; the tool calls are hidden.
	for _, want := range []string{"[implement] Working on it", "[implement] Done."} {
		if !strings.Contains(got, want) {
			t.Fatalf("output = %q, want it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "edit") {
		t.Fatalf("output = %q, want tool calls suppressed", got)
	}
	// The regression: a label followed only by end-of-line/whitespace.
	emptyLabel := regexp.MustCompile(`\[implement\] *(\r|\n|$)`)
	strippedFinalClear := strings.TrimSuffix(got, "\r\x1b[K")
	if loc := emptyLabel.FindString(strippedFinalClear); loc != "" {
		t.Fatalf("output = %q, found empty label %q", got, loc)
	}
}

// TestReviewProgressWriterEnablesSpinner guards the reviewer/implementer parity
// bug: the verdict-hiding progress writer must be unwrappable so the spinner
// reaches the terminal and animates for the reviewer just like the implementer.
func TestReviewProgressWriterEnablesSpinner(t *testing.T) {
	stubTerminalDetection(t)
	terminal := &fakeTerminal{}
	labeled := &roleLabelWriter{output: terminal, prefix: "[review] ", atLineStart: true}
	progress := newReviewProgressWriter(labeled)

	sp := newSpinner(progress)
	if !sp.enabled {
		t.Fatal("reviewer spinner is disabled; progress writer must expose the terminal")
	}
	if sp.output != terminal {
		t.Fatalf("spinner draws through %#v, want the raw terminal", sp.output)
	}
}

// TestQuietProseDeltasStayOnOneLine guards the delta-splitting bug: harnesses
// like claude stream prose token-by-token with control frames (pings,
// content-block markers that decode to EventRaw) interleaved between the
// deltas. Those silent frames must not restart the spinner mid-prose, which
// would inject a newline and label after every token.
func TestQuietProseDeltasStayOnOneLine(t *testing.T) {
	stubTerminalDetection(t)
	terminal := &fakeTerminal{}
	labeled := &roleLabelWriter{output: terminal, prefix: "[review] ", atLineStart: true}
	progress := newReviewProgressWriter(labeled)
	sp := newSpinner(progress)
	sp.Start()

	atLineStart := true
	parser := newQuestionParser()
	var pendingRaw []harness.Event
	events := []harness.Event{
		{Type: harness.EventRaw, Raw: `{"event":{"type":"message_start"}}`},
		{Type: harness.EventAssistantText, Text: "R"},
		{Type: harness.EventRaw, Raw: `{"event":{"type":"ping"}}`},
		{Type: harness.EventAssistantText, Text: "efreshD"},
		{Type: harness.EventRaw, Raw: `{"event":{"type":"ping"}}`},
		{Type: harness.EventAssistantText, Text: "atabase is consistent.\n"},
	}
	for _, event := range events {
		parsed := questionParseResult{}
		if event.Type == harness.EventAssistantText {
			parsed = parser.Feed(event.Text)
		}
		if err := renderHarnessEvent(progress, QuietHarnessOutput, event, parsed, parser, &pendingRaw, sp, &atLineStart); err != nil {
			t.Fatalf("renderHarnessEvent: %v", err)
		}
	}
	sp.Stop()

	got := terminal.String()
	if !strings.Contains(got, "[review] RefreshDatabase is consistent.") {
		t.Fatalf("output = %q, want the prose deltas joined on one labeled line", got)
	}
	// The token boundaries must not have grown their own labels.
	if strings.Count(got, "[review]") != 1 {
		t.Fatalf("output = %q, want exactly one label, got %d", got, strings.Count(got, "[review]"))
	}
}

// TestSpinnerDrawsFirstFrameImmediately guards the "never appears" bug: work
// shorter than one tick interval must still draw a frame, so the animation
// shows up instead of waiting for the first tick.
func TestSpinnerDrawsFirstFrameImmediately(t *testing.T) {
	stubTerminalDetection(t)
	terminal := &fakeTerminal{}
	sp := newSpinner(terminal)

	sp.Start()
	sp.Stop() // joins the goroutine, which always draws once before exiting

	if got := terminal.String(); !strings.Contains(got, "Working") {
		t.Fatalf("output = %q, want an immediately drawn %q frame", got, "Working")
	}
}

// TestQuietSpinnerSurvivesSilentEvents guards the flicker-out bug: session ids
// and tool results carry no prose, so the spinner must keep running across them
// and stop only when real prose arrives.
func TestQuietSpinnerSurvivesSilentEvents(t *testing.T) {
	stubTerminalDetection(t)
	terminal := &fakeTerminal{}
	labeled := &roleLabelWriter{output: terminal, prefix: "[implement] ", atLineStart: true}
	sp := newSpinner(labeled)

	atLineStart := true
	parser := newQuestionParser()
	var pendingRaw []harness.Event
	silent := []harness.Event{
		{Type: harness.EventSession, SessionID: "s1"},
		{Type: harness.EventToolUse, ToolName: "edit"},
		{Type: harness.EventRaw, Raw: `{"type":"user"}`}, // tool result
		{Type: harness.EventToolUse, ToolName: "bash"},
	}
	for _, event := range silent {
		if err := renderHarnessEvent(labeled, QuietHarnessOutput, event, questionParseResult{}, parser, &pendingRaw, sp, &atLineStart); err != nil {
			t.Fatalf("renderHarnessEvent: %v", err)
		}
	}

	sp.mu.Lock()
	active := sp.active
	sp.mu.Unlock()
	if !active {
		t.Fatal("spinner stopped during silent events, want it still running")
	}

	// Prose finally arrives and must stop the spinner.
	prose := harness.Event{Type: harness.EventAssistantText, Text: "Hello\n"}
	if err := renderHarnessEvent(labeled, QuietHarnessOutput, prose, parser.Feed(prose.Text), parser, &pendingRaw, sp, &atLineStart); err != nil {
		t.Fatalf("renderHarnessEvent: %v", err)
	}
	sp.mu.Lock()
	active = sp.active
	sp.mu.Unlock()
	if active {
		t.Fatal("spinner still running after prose arrived, want it stopped")
	}
	if got := terminal.String(); !strings.Contains(got, "[implement] Hello") {
		t.Fatalf("output = %q, want prose printed", got)
	}
}
