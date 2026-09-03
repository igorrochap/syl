// Package tui contains the interactive prompt engine used by syl init.
package tui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type PromptKind uint8

const (
	ChoicePrompt PromptKind = iota
	MultiPrompt
	TextPrompt
)

type PromptSpec struct {
	Key          string
	Label        string
	Kind         PromptKind
	Options      []string
	DefaultValue string
	Skip         func(map[string]Answer) bool
}

// PromptOption is the visible state of one option in a prompt.
type PromptOption struct {
	Label    string
	Cursor   bool
	Checkbox bool
	Selected bool
}

// PromptView is the visible state of the current prompt.
type PromptView struct {
	Step         int
	Total        int
	Label        string
	Options      []PromptOption
	Input        string
	DefaultValue string
	Hint         string
	Message      string
}

// ViewRenderer renders a prompt view for the terminal's output capabilities.
type ViewRenderer func(PromptView) string

type Answer struct {
	Value    string
	Selected []string
}

type promptModel struct {
	specs         []PromptSpec
	answers       map[string]Answer
	index         int
	cursor        int
	buffer        []rune
	selected      map[string]bool
	message       string
	err           error
	done          bool
	afterPrompts  func(map[string]Answer) (PromptSpec, bool, error)
	baseSpecCount int
	renderer      ViewRenderer
}

// Run renders a sequence of prompts. The finalizer may append one confirmation
// prompt after the initial sequence has been answered.
func Run(input io.Reader, output io.Writer, specs []PromptSpec, finalizer func(map[string]Answer) (PromptSpec, bool, error)) (map[string]Answer, error) {
	return run(input, output, specs, finalizer, nil)
}

// RunWithRenderer runs prompts using the supplied renderer for each view.
func RunWithRenderer(input io.Reader, output io.Writer, specs []PromptSpec, finalizer func(map[string]Answer) (PromptSpec, bool, error), renderer ViewRenderer) (map[string]Answer, error) {
	return run(input, output, specs, finalizer, renderer)
}

func run(input io.Reader, output io.Writer, specs []PromptSpec, finalizer func(map[string]Answer) (PromptSpec, bool, error), renderer ViewRenderer) (map[string]Answer, error) {
	model := newPromptModel(specs)
	model.afterPrompts = finalizer
	model.renderer = renderer
	options := []tea.ProgramOption{tea.WithInput(input), tea.WithOutput(output)}
	if !isInteractiveOutput(output) {
		options = append(options, tea.WithoutRenderer())
	}
	program := tea.NewProgram(model, options...)
	finalModel, err := program.Run()
	if err != nil {
		return nil, fmt.Errorf("run init prompts: %w", err)
	}
	result := finalModel.(*promptModel)
	if result.err != nil {
		return nil, result.err
	}
	return result.answers, nil
}

func isInteractiveOutput(output io.Writer) bool {
	file, ok := output.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func newPromptModel(specs []PromptSpec) *promptModel {
	model := &promptModel{
		specs:         specs,
		answers:       make(map[string]Answer, len(specs)),
		selected:      make(map[string]bool),
		baseSpecCount: len(specs),
	}
	model.resetCurrent()
	return model
}

func (m *promptModel) Init() tea.Cmd { return nil }

func (m *promptModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if m.done {
		return m, tea.Quit
	}
	key, ok := message.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	handled, quit := m.handleGlobalKey(key)
	if handled {
		if quit {
			return m, tea.Quit
		}
		return m, nil
	}
	if m.index >= len(m.specs) {
		m.done = true
		return m, tea.Quit
	}

	spec := m.specs[m.index]
	switch spec.Kind {
	case ChoicePrompt:
		m.updateChoice(spec, key)
	case MultiPrompt:
		m.updateMulti(spec, key)
	case TextPrompt:
		m.updateText(spec, key)
	}
	if m.done {
		return m, tea.Quit
	}
	return m, nil
}

func (m *promptModel) handleGlobalKey(key tea.KeyMsg) (bool, bool) {
	if key.Type == tea.KeyCtrlC {
		m.err = errors.New("init cancelled")
		m.done = true
		return true, true
	}
	if key.Type == tea.KeyEsc || key.Type == tea.KeyLeft {
		m.goBack()
		return true, false
	}
	return false, false
}

func (m *promptModel) updateChoice(spec PromptSpec, key tea.KeyMsg) {
	switch key.Type {
	case tea.KeyUp:
		m.moveCursor(-1, len(spec.Options))
		m.buffer = nil
	case tea.KeyDown, tea.KeyRight:
		m.moveCursor(1, len(spec.Options))
		m.buffer = nil
	case tea.KeyEnter, tea.KeyCtrlJ:
		index, ok := findOption(string(m.buffer), spec.Options)
		if len(m.buffer) == 0 {
			index, ok = m.cursor, true
		}
		if !ok {
			m.message = fmt.Sprintf("Unknown choice %q", string(m.buffer))
			return
		}
		m.advance(spec, Answer{Value: spec.Options[index]})
	case tea.KeyBackspace, tea.KeyCtrlH, tea.KeyDelete:
		m.removeLastRune()
	default:
		m.appendRunes(key)
	}
}

func (m *promptModel) updateMulti(spec PromptSpec, key tea.KeyMsg) {
	switch key.Type {
	case tea.KeyUp:
		m.moveCursor(-1, len(spec.Options))
	case tea.KeyDown, tea.KeyRight:
		m.moveCursor(1, len(spec.Options))
	case tea.KeySpace:
		if len(m.buffer) == 0 {
			m.selected[spec.Options[m.cursor]] = !m.selected[spec.Options[m.cursor]]
		} else {
			m.appendRunes(key)
		}
	case tea.KeyEnter, tea.KeyCtrlJ:
		if len(m.buffer) == 0 {
			m.advance(spec, Answer{Selected: selectedOptions(spec.Options, m.selected)})
			return
		}
		selected, ok := parseSelectedOptions(string(m.buffer), spec.Options)
		if !ok {
			m.message = fmt.Sprintf("Unknown optional skill in %q", string(m.buffer))
			return
		}
		m.advance(spec, Answer{Selected: selected})
	case tea.KeyBackspace, tea.KeyCtrlH, tea.KeyDelete:
		m.removeLastRune()
	default:
		m.appendRunes(key)
	}
}

func (m *promptModel) updateText(spec PromptSpec, key tea.KeyMsg) {
	switch key.Type {
	case tea.KeyEnter, tea.KeyCtrlJ:
		value := strings.TrimSpace(string(m.buffer))
		if value == "" {
			value = spec.DefaultValue
		}
		m.advance(spec, Answer{Value: value})
	case tea.KeyBackspace, tea.KeyCtrlH, tea.KeyDelete:
		m.removeLastRune()
	case tea.KeyCtrlU:
		m.buffer = nil
		m.message = ""
	default:
		m.appendRunes(key)
	}
}

func (m *promptModel) appendRunes(key tea.KeyMsg) {
	if key.Type != tea.KeyRunes && key.Type != tea.KeySpace {
		return
	}
	m.buffer = append(m.buffer, key.Runes...)
	m.message = ""
}

func (m *promptModel) removeLastRune() {
	if len(m.buffer) > 0 {
		m.buffer = m.buffer[:len(m.buffer)-1]
	}
	m.message = ""
}

func (m *promptModel) moveCursor(delta, count int) {
	if count == 0 {
		return
	}
	m.cursor = (m.cursor + delta + count) % count
	m.message = ""
}

func (m *promptModel) advance(spec PromptSpec, answer Answer) {
	m.answers[spec.Key] = answer
	m.buffer = nil
	m.message = ""
	if m.index >= m.baseSpecCount {
		m.done = true
		return
	}
	m.index = m.nextPromptIndex(m.index + 1)
	if m.index >= m.baseSpecCount {
		m.finishPrompts()
		return
	}
	m.resetCurrent()
}

func (m *promptModel) resetCurrent() {
	m.cursor = 0
	m.buffer = nil
	m.selected = make(map[string]bool)
	if m.index >= len(m.specs) {
		return
	}
	spec := m.specs[m.index]
	answer, answered := m.answers[spec.Key]
	if answered {
		m.restoreAnswer(spec, answer)
		return
	}
	if spec.Kind == ChoicePrompt {
		if index, ok := findOption(spec.DefaultValue, spec.Options); ok {
			m.cursor = index
		}
	}
}

func (m *promptModel) View() string {
	if m.done || m.index >= len(m.specs) {
		return ""
	}
	spec := m.specs[m.index]
	view := PromptView{
		Step:         m.currentStep(),
		Total:        m.totalSteps(),
		Label:        spec.Label,
		Options:      makePromptOptions(spec, m.cursor, m.selected),
		Input:        string(m.buffer),
		DefaultValue: spec.DefaultValue,
		Hint:         promptHint(spec, m.buffer, m.cursor),
		Message:      m.message,
	}
	if m.renderer != nil {
		return m.renderer(view)
	}
	return renderPlainPrompt(view)
}

func (m *promptModel) goBack() {
	if m.index >= m.baseSpecCount {
		delete(m.answers, m.specs[m.index].Key)
	}
	previous := m.previousPromptIndex(m.index)
	if previous < 0 {
		return
	}
	m.index = previous
	m.resetCurrent()
}

func (m *promptModel) finishPrompts() {
	if m.afterPrompts == nil {
		m.done = true
		return
	}
	spec, addPrompt, err := m.afterPrompts(m.answers)
	if err != nil {
		m.err = err
		m.done = true
		return
	}
	if !addPrompt {
		m.done = true
		return
	}
	if len(m.specs) == m.baseSpecCount {
		m.specs = append(m.specs, spec)
	} else {
		m.specs[m.baseSpecCount] = spec
	}
	m.index = m.baseSpecCount
	m.resetCurrent()
}

func (m *promptModel) nextPromptIndex(start int) int {
	for index := start; index < m.baseSpecCount; index++ {
		if m.isSkipped(index) {
			// Answers from an inactive branch must not leak into finalization.
			delete(m.answers, m.specs[index].Key)
			continue
		}
		return index
	}
	return m.baseSpecCount
}

func (m *promptModel) previousPromptIndex(start int) int {
	for index := start - 1; index >= 0; index-- {
		if !m.isSkipped(index) {
			return index
		}
	}
	return -1
}

func (m *promptModel) isSkipped(index int) bool {
	return index < m.baseSpecCount && m.specs[index].Skip != nil && m.specs[index].Skip(m.answers)
}

func (m *promptModel) currentStep() int {
	step := 0
	for index := 0; index <= m.index && index < len(m.specs); index++ {
		if index < m.baseSpecCount && m.isSkipped(index) {
			continue
		}
		step++
	}
	return step
}

func (m *promptModel) totalSteps() int {
	total := 0
	for index := 0; index < m.baseSpecCount; index++ {
		if !m.isSkipped(index) {
			total++
		}
	}
	if len(m.specs) > m.baseSpecCount {
		total++
	}
	return total
}

func (m *promptModel) restoreAnswer(spec PromptSpec, answer Answer) {
	switch spec.Kind {
	case ChoicePrompt:
		if index, ok := findOption(answer.Value, spec.Options); ok {
			m.cursor = index
		}
	case MultiPrompt:
		for _, option := range answer.Selected {
			m.selected[option] = true
		}
	case TextPrompt:
		m.buffer = []rune(answer.Value)
	}
}

func makePromptOptions(spec PromptSpec, cursor int, selected map[string]bool) []PromptOption {
	options := make([]PromptOption, 0, len(spec.Options))
	for index, option := range spec.Options {
		options = append(options, PromptOption{
			Label:    option,
			Cursor:   index == cursor,
			Checkbox: spec.Kind == MultiPrompt,
			Selected: selected[option],
		})
	}
	return options
}

func promptHint(spec PromptSpec, buffer []rune, cursor int) string {
	if len(buffer) > 0 {
		if spec.Kind == MultiPrompt {
			return "Typed selection: " + string(buffer)
		}
		return "Typed override: " + string(buffer)
	}
	if spec.Kind == MultiPrompt {
		return "Space toggles; Enter accepts; arrows navigate; Esc goes back."
	}
	if spec.Kind == ChoicePrompt {
		if len(spec.Options) == 0 || cursor >= len(spec.Options) {
			return "Enter accepts the default; arrows navigate; Esc goes back."
		}
		return fmt.Sprintf("Enter accepts %s; arrows navigate; Esc goes back.", spec.Options[cursor])
	}
	return "Enter accepts; Esc goes back."
}

func renderPlainPrompt(view PromptView) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Step %d of %d: %s\n", view.Step, view.Total, view.Label)
	for _, option := range view.Options {
		cursor := "  "
		if option.Cursor {
			cursor = "> "
		}
		marker := ""
		if option.Checkbox {
			marker = "[ ] "
			if option.Selected {
				marker = "[x] "
			}
		}
		fmt.Fprintf(&builder, "%s%s%s\n", cursor, marker, option.Label)
	}
	if len(view.Options) == 0 {
		fmt.Fprintf(&builder, "Type an override, or press Enter for %s:\n> %s\n", view.DefaultValue, view.Input)
	}
	if view.Hint != "" {
		fmt.Fprintf(&builder, "%s\n", view.Hint)
	}
	if view.Message != "" {
		fmt.Fprintf(&builder, "%s\n", view.Message)
	}
	return builder.String()
}

func findOption(value string, options []string) (int, bool) {
	value = strings.TrimSpace(value)
	if index, err := strconv.Atoi(value); err == nil && index >= 1 && index <= len(options) {
		return index - 1, true
	}
	for index, option := range options {
		if strings.EqualFold(value, option) {
			return index, true
		}
	}
	return 0, false
}

func parseSelectedOptions(value string, options []string) ([]string, bool) {
	if strings.EqualFold(strings.TrimSpace(value), "none") {
		return nil, true
	}
	selected := make(map[string]bool)
	for _, token := range strings.Split(value, ",") {
		index, ok := findOption(token, options)
		if !ok {
			return nil, false
		}
		selected[options[index]] = true
	}
	return selectedOptions(options, selected), true
}

func selectedOptions(options []string, selected map[string]bool) []string {
	result := make([]string, 0, len(selected))
	for _, option := range options {
		if selected[option] {
			result = append(result, option)
		}
	}
	return result
}
