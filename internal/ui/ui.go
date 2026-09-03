// Package ui renders syl's user-facing output.
package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/term"
	"github.com/mattn/go-isatty"
	"github.com/muesli/termenv"
)

// Caps describes the output capabilities available to a Renderer.
type Caps struct {
	Color   bool
	Width   int
	Unicode bool
}

// Banner is a heading followed by aligned label and value rows.
type Banner struct {
	Title string
	Rows  []Field
}

// Field is a label and value in a Banner.
type Field struct {
	Label string
	Value string
}

// BannerRow is an alternate name for Field for callers that prefer row terminology.
type BannerRow = Field

// Step is a section or progress heading. Number is optional and omitted when zero;
// Total adds a progress total when it is also greater than zero.
type Step struct {
	Number int
	Total  int
	Label  string
}

// Prompt is the visible state of one interactive question.
type Prompt struct {
	Step         Step
	Options      []PromptOption
	Input        string
	DefaultValue string
	Hint         string
	Message      string
}

// PromptOption is one selectable option in a Prompt.
type PromptOption struct {
	Label    string
	Cursor   bool
	Checkbox bool
	Selected bool
}

// Verdict is a review outcome and its summary.
type Verdict struct {
	Status  string
	Summary string
}

// Finding is an item reported by a review.
type Finding struct {
	Kind     string
	Location string
	Issue    string
}

// Row is one key and value in a table.
type Row struct {
	Key   string
	Value string
}

// KeyValue is an alternate name for Row.
type KeyValue = Row

// Renderer appends consistently spaced, sized, colored, and glyph-aware output.
type Renderer struct {
	output io.Writer
	caps   Caps
	style  styleSet
}

type styleSet struct {
	title    lipgloss.Style
	heading  lipgloss.Style
	label    lipgloss.Style
	positive lipgloss.Style
	negative lipgloss.Style
	muted    lipgloss.Style
	code     lipgloss.Style
	bold     lipgloss.Style
	italic   lipgloss.Style
}

type outputLine struct {
	text  string
	style lipgloss.Style
}

type linePart struct {
	text       string
	whitespace bool
}

// New constructs a Renderer with explicitly supplied output capabilities.
func New(output io.Writer, caps Caps) *Renderer {
	if output == nil {
		output = io.Discard
	}
	if caps.Width < 0 {
		caps.Width = 0
	}

	profile := termenv.Ascii
	if caps.Color {
		profile = termenv.TrueColor
	}
	lipglossRenderer := lipgloss.NewRenderer(output, termenv.WithProfile(profile), termenv.WithUnsafe())
	lipglossRenderer.SetColorProfile(profile)

	return &Renderer{
		output: output,
		caps:   caps,
		style: styleSet{
			title:    lipglossRenderer.NewStyle().Bold(true).Foreground(lipgloss.Color("#7D56F4")),
			heading:  lipglossRenderer.NewStyle().Bold(true).Foreground(lipgloss.Color("#7D56F4")),
			label:    lipglossRenderer.NewStyle().Foreground(lipgloss.Color("#626262")),
			positive: lipglossRenderer.NewStyle().Bold(true).Foreground(lipgloss.Color("#04B575")),
			negative: lipglossRenderer.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF4672")),
			muted:    lipglossRenderer.NewStyle().Foreground(lipgloss.Color("#626262")),
			code:     lipglossRenderer.NewStyle().Foreground(lipgloss.Color("#04B575")),
			bold:     lipglossRenderer.NewStyle().Bold(true),
			italic:   lipglossRenderer.NewStyle().Italic(true),
		},
	}
}

// Banner writes a heading and aligned label/value rows.
func (r *Renderer) Banner(value Banner) error {
	maxLabelWidth := bannerLabelWidth(value.Rows)
	lines := []outputLine{{text: value.Title, style: r.style.title}}
	for _, row := range value.Rows {
		label := strings.TrimSpace(row.Label) + ":"
		padding := strings.Repeat(" ", maxLabelWidth-lipgloss.Width(label))
		lines = append(lines, outputLine{
			text:  "  " + label + padding + "  " + row.Value,
			style: r.style.label,
		})
	}
	return r.writeLines(lines)
}

// Text writes one or more ordinary user-facing lines with the renderer's label style.
func (r *Renderer) Text(value string) error {
	return r.writeLines([]outputLine{{text: value, style: r.style.label}})
}

// PromptLine writes an inline user-facing prompt without appending a newline.
func (r *Renderer) PromptLine(value string) error {
	return writeString(r.output, r.style.label.Render(value))
}

// Step writes a section or progress heading.
func (r *Renderer) Step(value Step) error {
	return r.writeLines([]outputLine{{
		text:  r.stepText(value),
		style: r.style.heading,
	}})
}

// Prompt writes one styled interactive question and its current input state.
func (r *Renderer) Prompt(value Prompt) error {
	lines := make([]outputLine, 0, len(value.Options)+4)
	if value.Step.Label != "" {
		lines = append(lines, outputLine{text: r.stepText(value.Step), style: r.style.heading})
	}
	if len(value.Options) > 0 {
		lines = append(lines, r.promptOptionLines(value.Options)...)
	} else {
		lines = append(lines,
			outputLine{text: "Type an override, or press Enter for " + value.DefaultValue + ":", style: r.style.muted},
			outputLine{text: "> " + value.Input, style: r.style.code},
		)
	}
	if value.Hint != "" {
		lines = append(lines, outputLine{text: value.Hint, style: r.style.muted})
	}
	if value.Message != "" {
		lines = append(lines, outputLine{text: value.Message, style: r.style.negative})
	}
	return r.writeLines(lines)
}

// Section writes a section heading without a step number.
func (r *Renderer) Section(label string) error {
	return r.Step(Step{Label: label})
}

// Verdict writes a review outcome and its summary.
func (r *Renderer) Verdict(value Verdict) error {
	status := strings.TrimSpace(value.Status)
	if status == "" {
		status = "unknown"
	}
	statusStyle := r.style.muted
	glyph := r.glyph("•", "-")
	if strings.EqualFold(status, "approve") {
		statusStyle = r.style.positive
		glyph = r.glyph("✓", "[ok]")
	}
	if strings.EqualFold(status, "revise") {
		statusStyle = r.style.negative
		glyph = r.glyph("△", "[!]")
	}

	lines := []outputLine{{
		text:  glyph + " Verdict: " + status,
		style: statusStyle,
	}}
	if strings.TrimSpace(value.Summary) != "" {
		lines = append(lines, outputLine{
			text:  "  Summary: " + value.Summary,
			style: r.style.label,
		})
	}
	return r.writeLines(lines)
}

// Findings writes a heading and a list of review findings.
func (r *Renderer) Findings(findings []Finding) error {
	lines := []outputLine{{text: "Findings", style: r.style.heading}}
	if len(findings) == 0 {
		lines = append(lines, outputLine{text: "  (none)", style: r.style.muted})
		return r.writeLines(lines)
	}

	for _, finding := range findings {
		kind := strings.TrimSpace(finding.Kind)
		if kind == "" {
			kind = "finding"
		}
		marker := r.glyph("•", "-")
		findingStyle := r.style.label
		if strings.EqualFold(kind, "blocking") {
			marker = r.glyph("⚠", "[!]")
			findingStyle = r.style.negative
		}
		lines = append(lines, outputLine{
			text:  marker + " [" + kind + "] " + finding.Location + r.separator() + finding.Issue,
			style: findingStyle,
		})
	}
	return r.writeLines(lines)
}

// List writes a plain bulleted list.
func (r *Renderer) List(items []string) error {
	if len(items) == 0 {
		return r.writeLines([]outputLine{{text: "(none)", style: r.style.muted}})
	}

	lines := make([]outputLine, 0, len(items))
	for _, item := range items {
		lines = append(lines, outputLine{
			text:  r.glyph("•", "-") + " " + item,
			style: r.style.label,
		})
	}
	return r.writeLines(lines)
}

// Table writes aligned key and value rows.
func (r *Renderer) Table(rows []Row) error {
	maxKeyWidth := tableKeyWidth(rows)
	lines := make([]outputLine, 0, len(rows))
	for _, row := range rows {
		padding := strings.Repeat(" ", maxKeyWidth-lipgloss.Width(row.Key))
		lines = append(lines, outputLine{
			text:  row.Key + padding + "  " + row.Value,
			style: r.style.label,
		})
	}
	return r.writeLines(lines)
}

// Error writes an error message with the renderer's error glyph and style.
func (r *Renderer) Error(err error) error {
	message := "unknown error"
	if err != nil {
		message = err.Error()
	}
	return r.writeLines([]outputLine{{
		text:  r.glyph("✗", "[error]") + " Error: " + message,
		style: r.style.negative,
	}})
}

func (r *Renderer) stepText(value Step) string {
	label := strings.TrimSpace(value.Label)
	if value.Number > 0 && value.Total > 0 {
		label = fmt.Sprintf("Step %d of %d: %s", value.Number, value.Total, label)
	} else if value.Number > 0 {
		label = fmt.Sprintf("Step %d: %s", value.Number, label)
	}
	return r.glyph("◆", "*") + " " + label
}

func (r *Renderer) promptOptionLines(options []PromptOption) []outputLine {
	lines := make([]outputLine, 0, len(options))
	for _, option := range options {
		cursor := "  "
		if option.Cursor {
			cursor = r.glyph("❯ ", "> ")
		}
		marker := ""
		if option.Checkbox {
			marker = r.glyph("☐ ", "[ ] ")
			if option.Selected {
				marker = r.glyph("☑ ", "[x] ")
			}
		}
		style := r.style.label
		if option.Cursor {
			style = r.style.heading
		}
		if option.Selected {
			style = r.style.positive
		}
		lines = append(lines, outputLine{text: cursor + marker + option.Label, style: style})
	}
	return lines
}

func (r *Renderer) writeLines(lines []outputLine) error {
	var builder strings.Builder
	for _, line := range lines {
		for _, wrapped := range wrapText(line.text, r.caps.Width) {
			builder.WriteString(line.style.Render(wrapped))
			builder.WriteByte('\n')
		}
	}
	return writeString(r.output, builder.String())
}

func (r *Renderer) glyph(unicode, ascii string) string {
	if r.caps.Unicode {
		return unicode
	}
	return ascii
}

func (r *Renderer) separator() string {
	if !r.caps.Unicode {
		return " - "
	}
	return " — "
}

func bannerLabelWidth(rows []Field) int {
	maxWidth := 0
	for _, row := range rows {
		width := lipgloss.Width(strings.TrimSpace(row.Label) + ":")
		if width > maxWidth {
			maxWidth = width
		}
	}
	return maxWidth
}

func tableKeyWidth(rows []Row) int {
	maxWidth := 0
	for _, row := range rows {
		width := lipgloss.Width(row.Key)
		if width > maxWidth {
			maxWidth = width
		}
	}
	return maxWidth
}

func wrapText(text string, width int) []string {
	physicalLines := strings.Split(text, "\n")
	wrapped := make([]string, 0, len(physicalLines))
	for _, line := range physicalLines {
		wrapped = append(wrapped, wrapLine(line, width)...)
	}
	return wrapped
}

func wrapLine(line string, width int) []string {
	if width <= 0 || lipgloss.Width(line) <= width {
		return []string{line}
	}
	if strings.TrimSpace(line) == "" {
		return []string{line}
	}

	content := strings.TrimLeftFunc(line, unicode.IsSpace)
	prefix := line[:len(line)-len(content)]
	parts := splitLineParts(content)
	continuationPrefix := alignedContinuationPrefix(prefix, parts)
	return wrapParts(parts, prefix, continuationPrefix, width)
}

func splitLineParts(line string) []linePart {
	if line == "" {
		return nil
	}

	parts := make([]linePart, 0, 3)
	partStart := 0
	r, size := utf8.DecodeRuneInString(line)
	partWhitespace := unicode.IsSpace(r)
	for index := size; index < len(line); {
		r, size = utf8.DecodeRuneInString(line[index:])
		whitespace := unicode.IsSpace(r)
		if whitespace != partWhitespace {
			parts = append(parts, linePart{text: line[partStart:index], whitespace: partWhitespace})
			partStart = index
			partWhitespace = whitespace
		}
		index += size
	}
	return append(parts, linePart{text: line[partStart:], whitespace: partWhitespace})
}

func alignedContinuationPrefix(prefix string, parts []linePart) string {
	column := lipgloss.Width(prefix)
	for index, part := range parts {
		if part.whitespace {
			if lipgloss.Width(part.text) >= 2 && index+1 < len(parts) {
				return strings.Repeat(" ", column+lipgloss.Width(part.text))
			}
		}
		column += lipgloss.Width(part.text)
	}
	return prefix
}

func wrapParts(parts []linePart, prefix, continuationPrefix string, width int) []string {
	if len(parts) == 0 {
		return []string{prefix}
	}

	lines := make([]string, 0, len(parts))
	currentPrefix := prefix
	current := parts[0].text
	for index := 1; index < len(parts); index += 2 {
		separator := parts[index].text
		if index+1 == len(parts) {
			current += separator
			break
		}

		word := parts[index+1].text
		candidate := current + separator + word
		availableWidth := width - lipgloss.Width(currentPrefix)
		if shouldStartNewLine(current, candidate, availableWidth) {
			lines = append(lines, currentPrefix+current)
			currentPrefix = continuationPrefix
			current = word
			continue
		}
		current = candidate
	}
	return append(lines, currentPrefix+current)
}

func shouldStartNewLine(current, candidate string, availableWidth int) bool {
	if current == "" || isGlyphToken(current) {
		return false
	}
	return lipgloss.Width(candidate) > availableWidth
}

func isGlyphToken(value string) bool {
	switch value {
	case "•", "-", "◆", "*", "✓", "[ok]", "△", "[!]", "⚠", "✗", "[error]":
		return true
	default:
		return false
	}
}

func writeString(output io.Writer, value string) error {
	written, err := io.WriteString(output, value)
	if err != nil {
		return fmt.Errorf("write rendered output: %w", err)
	}
	if written != len(value) {
		return io.ErrShortWrite
	}
	return nil
}

// DetectCaps derives output capabilities from the writer and environment.
func DetectCaps(output io.Writer) Caps {
	caps := Caps{Unicode: true}
	if os.Getenv("NO_COLOR") != "" {
		return caps
	}

	fileDescriptor, ok := terminalFileDescriptor(output)
	if !ok || (!isatty.IsTerminal(fileDescriptor) && !isatty.IsCygwinTerminal(fileDescriptor)) {
		return caps
	}

	caps.Color = true
	caps.Width = terminalWidth(fileDescriptor)
	return caps
}

type fileDescriptorWriter interface {
	Fd() uintptr
}

type unwrapWriter interface {
	Unwrap() io.Writer
}

func terminalFileDescriptor(output io.Writer) (uintptr, bool) {
	for attempts := 0; attempts < 8 && output != nil; attempts++ {
		if file, ok := output.(fileDescriptorWriter); ok {
			return file.Fd(), true
		}
		unwrapper, ok := output.(unwrapWriter)
		if !ok {
			return 0, false
		}
		output = unwrapper.Unwrap()
	}
	return 0, false
}

func terminalWidth(fileDescriptor uintptr) int {
	width, _, err := term.GetSize(fileDescriptor)
	if err != nil || width < 1 {
		return 0
	}
	return width
}
