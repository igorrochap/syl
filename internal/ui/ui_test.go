package ui

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// Run `go test ./internal/ui -update` to regenerate the golden files.
var updateGolden = flag.Bool("update", false, "rewrite UI golden files")

func TestPrimitivesHavePlainAndStyledGoldens(t *testing.T) {
	primitives := []struct {
		name   string
		render func(*Renderer) error
	}{
		{
			name: "banner",
			render: func(renderer *Renderer) error {
				return renderer.Banner(Banner{
					Title: "syl implement #136 — Renderer",
					Rows: []Field{
						{Label: "implementer", Value: "codex"},
						{Label: "reviewer", Value: "claude"},
						{Label: "max iterations", Value: "3"},
					},
				})
			},
		},
		{
			name: "step",
			render: func(renderer *Renderer) error {
				return renderer.Step(Step{Number: 2, Label: "reviewing changes"})
			},
		},
		{
			name: "verdict",
			render: func(renderer *Renderer) error {
				return renderer.Verdict(Verdict{Status: "approve", Summary: "The renderer is ready."})
			},
		},
		{
			name: "review",
			render: func(renderer *Renderer) error {
				return renderer.ReviewVerdict(ReviewVerdict{
					Status:  "revise",
					Summary: "Keep this machine-readable summary on one unchanged line.",
					Findings: []Finding{
						{Kind: "blocking", Location: "internal/ui/review.go:29", Issue: "Keep this complete finding on one unchanged line."},
						{Kind: "nit", Location: "internal/ui/ui_test.go:1", Issue: "Keep this fixture focused."},
					},
				})
			},
		},
		{
			name: "implement",
			render: func(renderer *Renderer) error {
				return renderer.RunSummary(RunSummary{
					Iterations:   2,
					FinalVerdict: "revise",
					Summary:      "The implement loop needs one more focused correction.",
					NitFindings:  []Finding{{Kind: "nit", Location: "internal/ui/ui_test.go:1", Issue: "Add the command-level golden."}},
					WorktreePath: "/tmp/syl-worktree",
					DiffStat:     " internal/ui/review.go | 12 ++++--------\n 1 file changed, 4 insertions(+), 8 deletions(-)",
				})
			},
		},
		{
			name: "findings",
			render: func(renderer *Renderer) error {
				return renderer.Findings([]Finding{
					{Kind: "blocking", Location: "internal/ui/ui.go:1", Issue: "keep output append-only"},
					{Kind: "nit", Location: "internal/ui/ui_test.go:1", Issue: "add another fixture"},
				})
			},
		},
		{
			name: "list",
			render: func(renderer *Renderer) error {
				return renderer.List([]string{"plain output", "styled output", "golden files"})
			},
		},
		{
			name: "table",
			render: func(renderer *Renderer) error {
				return renderer.Table([]Row{
					{Key: "writer", Value: "buffer"},
					{Key: "width", Value: "48 columns"},
					{Key: "unicode", Value: "enabled"},
				})
			},
		},
		{
			name: "error",
			render: func(renderer *Renderer) error {
				return renderer.Error(errors.New("cannot read .syl/config.toml"))
			},
		},
	}

	for _, mode := range []struct {
		name  string
		color bool
	}{
		{name: "plain", color: false},
		{name: "styled", color: true},
	} {
		for _, primitive := range primitives {
			t.Run(mode.name+"/"+primitive.name, func(t *testing.T) {
				var output bytes.Buffer
				renderer := New(&output, Caps{Color: mode.color, Width: 48, Unicode: true})
				if err := primitive.render(renderer); err != nil {
					t.Fatal(err)
				}
				assertGolden(t, mode.name+"-"+primitive.name+".golden", output.Bytes())
			})
		}
	}
}

func TestASCIIOutputUsesFallbackGlyphs(t *testing.T) {
	var output bytes.Buffer
	renderer := New(&output, Caps{Width: 48})
	if err := renderer.Step(Step{Number: 1, Label: "start"}); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Verdict(Verdict{Status: "approve", Summary: "ready"}); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Findings([]Finding{{Kind: "blocking", Location: "main.go:1", Issue: "fix it"}}); err != nil {
		t.Fatal(err)
	}
	if err := renderer.List([]string{"one"}); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Error(errors.New("failed")); err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "ascii.golden", output.Bytes())
}

func TestWidthHandling(t *testing.T) {
	t.Run("wraps at a normal width", func(t *testing.T) {
		var output bytes.Buffer
		renderer := New(&output, Caps{Width: 12, Unicode: true})
		if err := renderer.List([]string{"alpha beta gamma"}); err != nil {
			t.Fatal(err)
		}
		if lines := strings.Count(output.String(), "\n"); lines != 2 {
			t.Fatalf("line count = %d, want 2; output = %q", lines, output.String())
		}
	})

	t.Run("overflows an unbreakable token", func(t *testing.T) {
		const longPath = "internal/ui/a-very-long-file-name.go"
		var output bytes.Buffer
		renderer := New(&output, Caps{Width: 4, Unicode: true})
		if err := renderer.List([]string{longPath}); err != nil {
			t.Fatal(err)
		}
		if got := output.String(); got != "• "+longPath+"\n" {
			t.Fatalf("output = %q, want the overflowing token on one line", got)
		}
	})

	t.Run("zero width does not wrap", func(t *testing.T) {
		const longValue = "alpha beta gamma delta epsilon"
		var output bytes.Buffer
		renderer := New(&output, Caps{Unicode: true})
		if err := renderer.List([]string{longValue}); err != nil {
			t.Fatal(err)
		}
		if got := output.String(); got != "• "+longValue+"\n" {
			t.Fatalf("output = %q, want unwrapped output", got)
		}
		assertGolden(t, "width-zero.golden", output.Bytes())
	})

	t.Run("preserves aligned columns for narrow banners and tables", func(t *testing.T) {
		t.Run("banner", func(t *testing.T) {
			var output bytes.Buffer
			renderer := New(&output, Caps{Width: 25, Unicode: true})
			if err := renderer.Banner(Banner{
				Title: "Details",
				Rows: []Field{
					{Label: "short", Value: "a long value"},
					{Label: "long label", Value: "ok"},
				},
			}); err != nil {
				t.Fatal(err)
			}
			want := "Details\n" +
				"  short:       a long\n" +
				"               value\n" +
				"  long label:  ok\n"
			if got := output.String(); got != want {
				t.Fatalf("banner output = %q, want %q", got, want)
			}
		})

		t.Run("table", func(t *testing.T) {
			var output bytes.Buffer
			renderer := New(&output, Caps{Width: 16, Unicode: true})
			if err := renderer.Table([]Row{
				{Key: "key", Value: "a long value"},
				{Key: "long key", Value: "ok"},
			}); err != nil {
				t.Fatal(err)
			}
			want := "key       a long\n" +
				"          value\n" +
				"long key  ok\n"
			if got := output.String(); got != want {
				t.Fatalf("table output = %q, want %q", got, want)
			}
		})
	})
}

func TestDetectCapsUsesEnvironmentAndTerminalWriter(t *testing.T) {
	master, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = master.Close()
		_ = terminal.Close()
	})

	tests := []struct {
		name      string
		noColor   string
		output    io.Writer
		wantColor bool
		wantWidth bool
	}{
		{name: "terminal with unset NO_COLOR", output: terminal, wantColor: true, wantWidth: true},
		{name: "terminal with empty NO_COLOR", noColor: "", output: terminal, wantColor: true, wantWidth: true},
		{name: "terminal with NO_COLOR", noColor: "1", output: terminal},
		{name: "buffer with unset NO_COLOR", output: &bytes.Buffer{}},
		{name: "buffer with NO_COLOR", noColor: "1", output: &bytes.Buffer{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", test.noColor)
			caps := DetectCaps(test.output)
			if caps.Color != test.wantColor {
				t.Fatalf("Color = %v, want %v", caps.Color, test.wantColor)
			}
			if test.wantWidth && caps.Width < 0 {
				t.Fatalf("Width = %d, want a non-negative terminal width", caps.Width)
			}
		})
	}
}

func TestRendererHandlesEmptyValuesAndWriteFailures(t *testing.T) {
	var output bytes.Buffer
	renderer := New(&output, Caps{Width: -1})
	if err := renderer.Banner(Banner{}); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Verdict(Verdict{}); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Findings(nil); err != nil {
		t.Fatal(err)
	}
	if err := renderer.List(nil); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Table(nil); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Error(nil); err != nil {
		t.Fatal(err)
	}

	writeErr := errors.New("writer failed")
	if err := New(errorWriter{err: writeErr}, Caps{}).Step(Step{Label: "step"}); !errors.Is(err, writeErr) {
		t.Fatalf("write error = %v, want %v", err, writeErr)
	}
	if err := New(shortWriter{}, Caps{}).Step(Step{Label: "step"}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write error = %v, want %v", err, io.ErrShortWrite)
	}
	if err := New(nil, Caps{}).Step(Step{Label: "discarded"}); err != nil {
		t.Fatal(err)
	}
}

func TestStreamWrapsAssistantProseWithRoleGutterAndFlushesFinalWord(t *testing.T) {
	var output bytes.Buffer
	stream := NewStream(&output, Caps{Color: true, Width: 12, Unicode: true}, StreamOptions{Gutter: "│ "})
	if err := stream.Assistant("alpha beta gamma"); err != nil {
		t.Fatal(err)
	}
	if err := stream.EndTurn(); err != nil {
		t.Fatal(err)
	}

	want := "\x1b[38;2;97;97;97m│ \x1b[0malpha beta\n\x1b[38;2;97;97;97m│ \x1b[0mgamma\n"
	if got := output.String(); got != want {
		t.Fatalf("stream output = %q, want %q", got, want)
	}
}

func TestStreamOmitsRoleGutterWithoutStyledTerminal(t *testing.T) {
	var output bytes.Buffer
	stream := NewStream(&output, Caps{Width: 12, Unicode: true}, StreamOptions{Gutter: "│ "})
	if err := stream.Assistant("alpha beta"); err != nil {
		t.Fatal(err)
	}
	if err := stream.EndTurn(); err != nil {
		t.Fatal(err)
	}

	if got := output.String(); got != "alpha beta\n" {
		t.Fatalf("stream output = %q, want plain output without role gutter", got)
	}
}

func TestStreamOverwideTokenIsEmittedWithoutLooping(t *testing.T) {
	var output bytes.Buffer
	stream := NewStream(&output, Caps{Width: 10}, StreamOptions{Gutter: "│ "})
	path := "/tmp/a-file-path-that-is-wider-than-the-terminal"
	if err := stream.Assistant(path); err != nil {
		t.Fatal(err)
	}
	if err := stream.EndTurn(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), path) {
		t.Fatalf("stream output = %q, want over-wide token %q", output.String(), path)
	}
}

func TestStreamDoesNotWriteCursorEscapesToNonTerminalOutput(t *testing.T) {
	var output bytes.Buffer
	stream := NewStream(&output, Caps{Color: false}, StreamOptions{Activity: true})
	if err := stream.Tool("Bash", "cat review.diff"); err != nil {
		t.Fatal(err)
	}
	if err := stream.EndTurn(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "\x1b[") {
		t.Fatalf("stream output = %q, want no cursor-control escape", output.String())
	}
}

func TestStreamStylesInlineMarkdownButLeavesBlockMarkdownLiteral(t *testing.T) {
	var output bytes.Buffer
	stream := NewStream(&output, Caps{Color: true}, StreamOptions{})
	text := "`code` **bold** *italic*\n- bullet\n### heading\n| **table** |\n```\n**fenced**\n```"
	if err := stream.Assistant(text); err != nil {
		t.Fatal(err)
	}
	if err := stream.EndTurn(); err != nil {
		t.Fatal(err)
	}

	got := output.String()
	if !strings.Contains(got, "\x1b[") {
		t.Fatalf("stream output = %q, want styled output", got)
	}
	for _, expected := range []string{"code", "bold", "italic", "- bullet", "### heading", "| **table** |", "**fenced**"} {
		if !strings.Contains(stripANSI(got), expected) {
			t.Fatalf("stream output = %q, want visible text %q", got, expected)
		}
	}
	if strings.Contains(stripANSI(got), "`code`") || strings.Contains(stripANSI(got), "**bold**") || strings.Contains(stripANSI(got), "*italic*") {
		t.Fatalf("stream output = %q, want inline markers rendered away", got)
	}
}

func TestStreamActivityAdvancesWithItsOwnTickerAndClearsOnNextEvent(t *testing.T) {
	previousTerminal := isTerminal
	isTerminal = func(fd uintptr) bool { return fd == 42 }
	t.Cleanup(func() { isTerminal = previousTerminal })

	clock := &testClock{now: time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC), tickCh: make(chan time.Time, 1)}
	terminal := &testTerminal{}
	stream := NewStream(terminal, Caps{Color: true, Width: 48}, StreamOptions{
		Activity: true, Clock: clock, TickerInterval: time.Second,
	})
	if err := stream.Tool("Bash", "cat review.diff"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(terminal.String(), "Bash") || !strings.Contains(terminal.String(), "0.0s") {
		t.Fatalf("activity = %q, want tool and initial elapsed time", terminal.String())
	}
	clock.now = clock.now.Add(2 * time.Second)
	clock.tick()
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(terminal.String(), "2.0s") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(terminal.String(), "2.0s") {
		t.Fatalf("activity = %q, want ticker-driven elapsed update", terminal.String())
	}
	if err := stream.BeforeEvent(); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(terminal.String(), "\r\x1b[K") {
		t.Fatalf("activity = %q, want clear escape after next event", terminal.String())
	}
}

func stripANSI(value string) string {
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

type testTerminal struct {
	mu sync.Mutex
	bytes.Buffer
}

func (t *testTerminal) Fd() uintptr { return 42 }

func (t *testTerminal) Write(value []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.Buffer.Write(value)
}

func (t *testTerminal) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.Buffer.String()
}

type testClock struct {
	now    time.Time
	tickCh chan time.Time
}

func (c *testClock) Now() time.Time { return c.now }

func (c *testClock) NewTicker(time.Duration) Ticker {
	if c.tickCh == nil {
		c.tickCh = make(chan time.Time)
	}
	return testTicker{channel: c.tickCh}
}

func (c *testClock) tick() {
	c.tickCh <- c.now
}

type testTicker struct{ channel <-chan time.Time }

func (t testTicker) C() <-chan time.Time { return t.channel }

func (testTicker) Stop() {}

type errorWriter struct {
	err error
}

func (w errorWriter) Write([]byte) (int, error) {
	return 0, w.err
}

type shortWriter struct{}

func (shortWriter) Write(value []byte) (int, error) {
	return len(value) - 1, nil
}

func assertGolden(t *testing.T, name string, actual []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.WriteFile(path, actual, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, want) {
		t.Fatalf("golden %s mismatch:\n got: %q\nwant: %q", name, actual, want)
	}
}
