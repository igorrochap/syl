package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ReviewVerdict is the structured verdict printed after a reviewer turn.
type ReviewVerdict struct {
	Status   string
	Summary  string
	Findings []Finding
}

// RunSummary is the final summary printed after an implement/review loop.
type RunSummary struct {
	Iterations   int
	FinalVerdict string
	Summary      string
	NitFindings  []Finding
	WorktreePath string
	DiffStat     string
}

// ReviewVerdict writes the machine-readable verdict shape while applying
// terminal styles to its semantic lines.
func (r *Renderer) ReviewVerdict(value ReviewVerdict) error {
	status := strings.TrimSpace(value.Status)
	if status == "" {
		status = "unknown"
	}
	statusStyle := r.style.muted
	if strings.EqualFold(status, "approve") {
		statusStyle = r.style.positive
	}
	if strings.EqualFold(status, "revise") {
		statusStyle = r.style.negative
	}
	lines := []outputLine{{text: "VERDICT: " + status, style: statusStyle}}
	lines = append(lines, outputLine{text: "SUMMARY: " + value.Summary, style: r.style.label})
	lines = append(lines, outputLine{text: "FINDINGS:", style: r.style.heading})
	lines = append(lines, reviewFindingLines(r, value.Findings)...)
	return r.writeReviewLines(lines)
}

// RunSummary writes the final implement loop summary through the renderer.
func (r *Renderer) RunSummary(value RunSummary) error {
	lines := []outputLine{
		{text: fmt.Sprintf("Iterations: %d", value.Iterations), style: r.style.heading},
		{text: "Final verdict: " + value.FinalVerdict, style: r.style.label},
		{text: "Summary: " + value.Summary, style: r.style.label},
		{text: "Nit findings:", style: r.style.heading},
	}
	lines = append(lines, reviewFindingLines(r, value.NitFindings)...)
	if len(value.NitFindings) == 0 {
		lines[len(lines)-1] = outputLine{text: "- (none)", style: r.style.muted}
	}
	if value.WorktreePath != "" {
		lines = append(lines,
			outputLine{text: "Worktree: " + value.WorktreePath, style: r.style.label},
			outputLine{text: "Remove worktree: git worktree remove --force " + value.WorktreePath, style: r.style.label},
		)
	}
	lines = append(lines, outputLine{text: "Diff stat:", style: r.style.heading})
	diffStat := strings.TrimRight(value.DiffStat, " \t\r\n")
	if diffStat == "" {
		lines = append(lines, outputLine{text: "", style: r.style.label})
	} else {
		for _, line := range strings.Split(diffStat, "\n") {
			lines = append(lines, outputLine{text: line, style: r.style.label})
		}
	}
	return r.writeReviewLines(lines)
}

func (r *Renderer) writeReviewLines(lines []outputLine) error {
	if !rendererOutputAtLineStart(r.output) {
		if err := writeString(r.output, "\n"); err != nil {
			return err
		}
	}
	return r.writeUnwrappedLines(lines)
}

func (r *Renderer) writeUnwrappedLines(lines []outputLine) error {
	var builder strings.Builder
	for _, line := range lines {
		builder.WriteString(line.style.Render(line.text))
		builder.WriteByte('\n')
	}
	return writeString(r.output, builder.String())
}

func rendererOutputAtLineStart(output interface{}) bool {
	if output == nil {
		return true
	}
	if lineWriter, ok := output.(interface{ AtLineStart() bool }); ok {
		return lineWriter.AtLineStart()
	}
	if byteWriter, ok := output.(interface{ Bytes() []byte }); ok {
		return bytesAtLineStart(byteWriter.Bytes())
	}
	if stringWriter, ok := output.(interface{ String() string }); ok {
		return bytesAtLineStart([]byte(stringWriter.String()))
	}
	return true
}

func bytesAtLineStart(value []byte) bool {
	if len(value) == 0 {
		return true
	}
	last := value[len(value)-1]
	return last == '\n' || last == '\r'
}

func reviewFindingLines(renderer *Renderer, findings []Finding) []outputLine {
	if len(findings) == 0 {
		return []outputLine{{text: "- (none)", style: renderer.style.muted}}
	}
	lines := make([]outputLine, 0, len(findings))
	for _, finding := range findings {
		kind := strings.TrimSpace(finding.Kind)
		if kind == "" {
			kind = "finding"
		}
		lines = append(lines, outputLine{
			text:  fmt.Sprintf("- [%s] %s — %s", kind, finding.Location, finding.Issue),
			style: findingStyle(renderer, kind),
		})
	}
	return lines
}

func findingStyle(renderer *Renderer, kind string) lipgloss.Style {
	if strings.EqualFold(kind, "blocking") {
		return renderer.style.negative
	}
	return renderer.style.label
}
