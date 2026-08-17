// Package tui contains the interactive prompt engine used by rig init.
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
}

type Answer struct {
	Value    string
	Selected []string
}

type promptModel struct {
	specs           []PromptSpec
	answers         map[string]Answer
	index           int
	cursor          int
	buffer          []rune
	selected        map[string]bool
	message         string
	err             error
	done            bool
	afterPrompts    func(map[string]Answer) (PromptSpec, bool, error)
	afterPromptsRun bool
}

// Run renders a sequence of prompts. The finalizer may append one confirmation
// prompt after the initial sequence has been answered.
func Run(input io.Reader, output io.Writer, specs []PromptSpec, finalizer func(map[string]Answer) (PromptSpec, bool, error)) (map[string]Answer, error) {
	model := newPromptModel(specs)
	model.afterPrompts = finalizer
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
		specs:    specs,
		answers:  make(map[string]Answer, len(specs)),
		selected: make(map[string]bool),
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
	if key.Type == tea.KeyCtrlC || key.Type == tea.KeyEsc {
		m.err = errors.New("init cancelled")
		m.done = true
		return m, tea.Quit
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

func (m *promptModel) updateChoice(spec PromptSpec, key tea.KeyMsg) {
	switch key.Type {
	case tea.KeyUp, tea.KeyLeft:
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
	case tea.KeyUp, tea.KeyLeft:
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
	m.index++
	m.buffer = nil
	m.message = ""
	if m.index >= len(m.specs) {
		if m.afterPrompts != nil && !m.afterPromptsRun {
			m.afterPromptsRun = true
			spec, addPrompt, err := m.afterPrompts(m.answers)
			if err != nil {
				m.err = err
				m.done = true
				return
			}
			if addPrompt {
				m.specs = append(m.specs, spec)
				m.resetCurrent()
				return
			}
		}
		m.done = true
		return
	}
	m.resetCurrent()
}

func (m *promptModel) resetCurrent() {
	m.cursor = 0
	m.selected = make(map[string]bool)
	if m.index >= len(m.specs) {
		return
	}
	spec := m.specs[m.index]
	if spec.Kind != ChoicePrompt {
		return
	}
	if index, ok := findOption(spec.DefaultValue, spec.Options); ok {
		m.cursor = index
	}
}

func (m *promptModel) View() string {
	if m.done || m.index >= len(m.specs) {
		return ""
	}
	spec := m.specs[m.index]
	var builder strings.Builder
	fmt.Fprintf(&builder, "%s\n\n", spec.Label)
	switch spec.Kind {
	case ChoicePrompt:
		for index, option := range spec.Options {
			marker := " "
			if index == m.cursor {
				marker = ">"
			}
			fmt.Fprintf(&builder, "%s %s\n", marker, option)
		}
		if len(m.buffer) > 0 {
			fmt.Fprintf(&builder, "\nTyped override: %s\n", string(m.buffer))
		} else {
			fmt.Fprintf(&builder, "\nEnter accepts %s; arrows navigate.\n", spec.Options[m.cursor])
		}
	case MultiPrompt:
		for index, option := range spec.Options {
			cursor := " "
			if index == m.cursor {
				cursor = ">"
			}
			checked := " "
			if m.selected[option] {
				checked = "x"
			}
			fmt.Fprintf(&builder, "%s [%s] %s\n", cursor, checked, option)
		}
		if len(m.buffer) > 0 {
			fmt.Fprintf(&builder, "\nTyped selection: %s\n", string(m.buffer))
		} else {
			builder.WriteString("\nSpace toggles; Enter accepts; arrows navigate.\n")
		}
	case TextPrompt:
		fmt.Fprintf(&builder, "Type an override, or press Enter for %s:\n> %s\n", spec.DefaultValue, string(m.buffer))
	}
	if m.message != "" {
		fmt.Fprintf(&builder, "\n%s\n", m.message)
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
