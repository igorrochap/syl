package orchestration

import "strings"

const (
	questionStartMarker = "QUESTION:\n"
	questionEndMarker   = "\nEND QUESTION"
)

type questionParseResult struct {
	VisibleText string
	Question    string
	Found       bool
}

// questionParser detects one QUESTION block while preserving output that
// precedes it. Once a block is found, the rest of that stream is ignored.
type questionParser struct {
	buffer string
	done   bool
}

func newQuestionParser() *questionParser {
	return &questionParser{}
}

func (p *questionParser) Feed(chunk string) questionParseResult {
	if p.done {
		return questionParseResult{}
	}
	p.buffer += chunk

	start := lineMarkerStart(p.buffer, questionStartMarker)
	if start == -1 {
		safeLength := len(p.buffer) - lineMarkerPrefixLength(p.buffer, questionStartMarker)
		visible := p.buffer[:safeLength]
		p.buffer = p.buffer[safeLength:]
		return questionParseResult{VisibleText: visible}
	}

	visible := p.buffer[:start]
	block := p.buffer[start+len(questionStartMarker):]
	end := strings.Index(block, questionEndMarker)
	if end == -1 {
		p.buffer = p.buffer[start:]
		return questionParseResult{VisibleText: visible}
	}

	p.done = true
	p.buffer = ""
	return questionParseResult{
		VisibleText: visible,
		Question:    strings.TrimSpace(block[:end]),
		Found:       true,
	}
}

func (p *questionParser) Flush() string {
	if p.done {
		return ""
	}
	visible := p.buffer
	p.buffer = ""
	return visible
}

func (p *questionParser) Pending() bool {
	return !p.done && p.buffer != ""
}

func suffixPrefixLength(value, marker string) int {
	max := len(value)
	if max >= len(marker) {
		max = len(marker) - 1
	}
	for length := max; length > 0; length-- {
		if strings.HasSuffix(value, marker[:length]) {
			return length
		}
	}
	return 0
}
