package orchestration

import (
	"io"
	"strings"

	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/verdict"
)

type streamMarkerParser struct {
	buffer string
	marker string
	hidden bool
}

// streamMarkerParser follows questionParser's chunk-buffering pattern. The
// review contract puts the structured verdict at the end of each turn, so the
// live feed can treat everything from the canonical marker onward as opaque;
// verdict.Parse remains responsible for validating its shape.
func newStreamMarkerParser(marker string) *streamMarkerParser {
	return &streamMarkerParser{marker: marker}
}

func (p *streamMarkerParser) Feed(chunk string) string {
	if p.hidden {
		return ""
	}
	p.buffer += chunk
	start := lineMarkerStart(p.buffer, p.marker)
	if start == -1 {
		safeLength := len(p.buffer) - lineMarkerPrefixLength(p.buffer, p.marker)
		visible := p.buffer[:safeLength]
		p.buffer = p.buffer[safeLength:]
		return visible
	}

	p.hidden = true
	visible := p.buffer[:start]
	p.buffer = ""
	return visible
}

func (p *streamMarkerParser) Flush() string {
	if p.hidden {
		return ""
	}
	visible := p.buffer
	p.buffer = ""
	return visible
}

func (p *streamMarkerParser) Reset() {
	p.buffer = ""
	p.hidden = false
}

func lineMarkerStart(value, marker string) int {
	searchFrom := 0
	for searchFrom < len(value) {
		relative := strings.Index(value[searchFrom:], marker)
		if relative == -1 {
			return -1
		}
		start := searchFrom + relative
		if start == 0 || value[start-1] == '\n' {
			return start
		}
		searchFrom = start + 1
	}
	return -1
}

func lineMarkerPrefixLength(value, marker string) int {
	length := suffixPrefixLength(value, marker)
	if length == 0 {
		return 0
	}
	start := len(value) - length
	if start == 0 || value[start-1] == '\n' {
		return length
	}
	return 0
}

type reviewProgressWriter struct {
	output      io.Writer
	parser      *streamMarkerParser
	atLineStart bool
}

func newReviewProgressWriter(output io.Writer) *reviewProgressWriter {
	return &reviewProgressWriter{output: output, parser: newStreamMarkerParser(verdict.BlockStartMarker), atLineStart: true}
}

func (w *reviewProgressWriter) Unwrap() io.Writer {
	return w.output
}

func (w *reviewProgressWriter) AtLineStart() bool {
	if lineWriter, ok := w.output.(interface{ AtLineStart() bool }); ok {
		return lineWriter.AtLineStart()
	}
	return w.atLineStart
}

func (w *reviewProgressWriter) BeforeEvent() error {
	if renderer, ok := w.output.(harnessEventRenderer); ok {
		return renderer.BeforeEvent()
	}
	return nil
}

func (w *reviewProgressWriter) StartTurn() {
	renderer, ok := w.output.(harnessTurnRenderer)
	if !ok {
		return
	}
	renderer.StartTurn()
}

func (w *reviewProgressWriter) Assistant(text string) error {
	return w.writeVisible(w.parser.Feed(text))
}

func (w *reviewProgressWriter) Tool(name, gist string) error {
	if err := w.writeVisible(w.parser.Flush()); err != nil {
		return err
	}
	if renderer, ok := w.output.(harnessEventRenderer); ok {
		return renderer.Tool(name, gist)
	}
	return writeParsedEvent(w.output, harness.Event{Type: harness.EventToolUse, ToolName: name, ArgumentGist: gist})
}

type lineTrackingWriter struct {
	output      io.Writer
	atLineStart bool
}

func newLineTrackingWriter(output io.Writer) *lineTrackingWriter {
	return &lineTrackingWriter{output: output, atLineStart: true}
}

func ensureLineTrackingWriter(output io.Writer) io.Writer {
	if _, ok := output.(interface{ AtLineStart() bool }); ok {
		return output
	}
	return newLineTrackingWriter(output)
}

func (w *lineTrackingWriter) Unwrap() io.Writer {
	return w.output
}

func (w *lineTrackingWriter) AtLineStart() bool {
	return w.atLineStart
}

func (w *lineTrackingWriter) Write(p []byte) (int, error) {
	n, err := w.output.Write(p)
	if n > 0 {
		lastByte := p[n-1]
		w.atLineStart = lastByte == '\n' || lastByte == '\r'
	}
	return n, err
}

func (w *reviewProgressWriter) Write(p []byte) (int, error) {
	if err := w.Assistant(string(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *reviewProgressWriter) EndTurn() error {
	visible := w.parser.Flush()
	w.parser.Reset()
	if err := w.writeVisible(visible); err != nil {
		return err
	}
	if renderer, ok := w.output.(harnessEventRenderer); ok {
		return renderer.EndTurn()
	}
	return nil
}

func (w *reviewProgressWriter) writeVisible(visible string) error {
	if visible == "" {
		return nil
	}
	if renderer, ok := w.output.(harnessEventRenderer); ok {
		return renderer.Assistant(visible)
	}
	if _, err := io.WriteString(w.output, visible); err != nil {
		return err
	}
	w.atLineStart = bytesAtLineStart([]byte(visible))
	return nil
}
