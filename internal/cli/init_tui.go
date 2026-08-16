package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/igorrochap/rig/internal/config"
)

type promptKind uint8

const (
	choicePrompt promptKind = iota
	multiPrompt
	textPrompt
)

type promptSpec struct {
	key          string
	label        string
	kind         promptKind
	options      []string
	defaultValue string
}

type promptAnswer struct {
	value    string
	selected []string
}

type promptModel struct {
	specs           []promptSpec
	answers         map[string]promptAnswer
	index           int
	cursor          int
	buffer          []rune
	selected        map[string]bool
	message         string
	err             error
	done            bool
	afterPrompts    func(map[string]promptAnswer) (promptSpec, bool, error)
	afterPromptsRun bool
}

func initPromptSpecs(manifest skillManifest) []promptSpec {
	optional := optionalSkillNames(manifest)
	specs := make([]promptSpec, 0, 12)
	if len(optional) > 0 {
		specs = append(specs, promptSpec{
			key:     "optional",
			label:   "Optional skills",
			kind:    multiPrompt,
			options: optional,
		})
	}
	specs = append(specs,
		promptSpec{key: "tracker.issues", label: "Issues tracker", kind: choicePrompt, options: []string{"github", "local"}, defaultValue: "github"},
		promptSpec{key: "tracker.reviews", label: "Review log", kind: choicePrompt, options: []string{"github", "local"}, defaultValue: "local"},
	)
	for _, role := range []struct {
		name    string
		harness string
		model   string
		effort  string
	}{
		{name: "plan", harness: "claude", model: "opus-5", effort: "high"},
		{name: "implement", harness: "codex", model: "gpt-5.6-luna", effort: "xhigh"},
		{name: "review", harness: "claude", model: "sonnet-5", effort: "medium"},
	} {
		specs = append(specs,
			promptSpec{key: role.name + ".harness", label: role.name + " harness", kind: choicePrompt, options: []string{"claude", "codex", "opencode"}, defaultValue: role.harness},
			promptSpec{key: role.name + ".model", label: role.name + " model", kind: textPrompt, defaultValue: role.model},
			promptSpec{key: role.name + ".effort", label: role.name + " effort", kind: choicePrompt, options: []string{"low", "medium", "high", "xhigh"}, defaultValue: role.effort},
		)
	}
	return specs
}

func runInitWizard(projectRoot string, input io.Reader, output io.Writer, manifest skillManifest) (initPlan, bool, error) {
	var plan initPlan
	finalizer := func(answers map[string]promptAnswer) (promptSpec, bool, error) {
		projectConfig, optional, err := configFromAnswers(answers)
		if err != nil {
			return promptSpec{}, false, err
		}
		plan, err = makeInitPlan(projectRoot, projectConfig, manifest, optional)
		if err != nil {
			return promptSpec{}, false, err
		}
		if len(plan.changes) == 0 || !plan.requiresConfirmation {
			return promptSpec{}, false, nil
		}
		return promptSpec{
			key:          "confirmation",
			label:        changesPrompt(plan),
			kind:         choicePrompt,
			options:      []string{"yes", "no"},
			defaultValue: "no",
		}, true, nil
	}
	answers, err := runPromptSequenceWithFinalizer(input, output, initPromptSpecs(manifest), finalizer)
	if err != nil {
		return initPlan{}, false, err
	}
	confirmed := !plan.requiresConfirmation || answers["confirmation"].value == "yes"
	return plan, confirmed, nil
}

func configFromAnswers(answers map[string]promptAnswer) (config.Config, []string, error) {
	value := func(key string) string { return answers[key].value }
	roles := config.RolesConfig{
		Plan: config.RoleConfig{
			Harness: config.Harness(value("plan.harness")),
			Model:   value("plan.model"),
			Effort:  config.Effort(value("plan.effort")),
		},
		Implement: config.RoleConfig{
			Harness: config.Harness(value("implement.harness")),
			Model:   value("implement.model"),
			Effort:  config.Effort(value("implement.effort")),
		},
		Review: config.RoleConfig{
			Harness: config.Harness(value("review.harness")),
			Model:   value("review.model"),
			Effort:  config.Effort(value("review.effort")),
		},
	}
	return config.Config{
		Tracker: config.TrackerConfig{
			Issues:  config.Tracker(value("tracker.issues")),
			Reviews: config.Tracker(value("tracker.reviews")),
		},
		Roles:         roles,
		Loop:          config.LoopConfig{MaxIterations: 3},
		Notifications: config.NotificationsConfig{Enabled: true},
	}, answers["optional"].selected, nil
}

func optionalSkillNames(manifest skillManifest) []string {
	optional := make([]string, 0)
	for _, skill := range manifest.Skills {
		if skill.Classification == "optional" {
			optional = append(optional, skill.Name)
		}
	}
	sort.Strings(optional)
	return optional
}

func runPromptSequenceWithFinalizer(input io.Reader, output io.Writer, specs []promptSpec, finalizer func(map[string]promptAnswer) (promptSpec, bool, error)) (map[string]promptAnswer, error) {
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

func changesPrompt(plan initPlan) string {
	var builder strings.Builder
	builder.WriteString("Changes:\n")
	for _, change := range plan.changes {
		fmt.Fprintf(&builder, "- %s\n", change)
	}
	builder.WriteString("\nApply these changes?")
	return builder.String()
}

func newPromptModel(specs []promptSpec) *promptModel {
	model := &promptModel{
		specs:    specs,
		answers:  make(map[string]promptAnswer, len(specs)),
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
	switch spec.kind {
	case choicePrompt:
		m.updateChoice(spec, key)
	case multiPrompt:
		m.updateMulti(spec, key)
	case textPrompt:
		m.updateText(spec, key)
	}
	if m.done {
		return m, tea.Quit
	}
	return m, nil
}

func (m *promptModel) updateChoice(spec promptSpec, key tea.KeyMsg) {
	switch key.Type {
	case tea.KeyUp, tea.KeyLeft:
		m.moveCursor(-1, len(spec.options))
		m.buffer = nil
	case tea.KeyDown, tea.KeyRight:
		m.moveCursor(1, len(spec.options))
		m.buffer = nil
	case tea.KeyEnter, tea.KeyCtrlJ:
		index, ok := findOption(string(m.buffer), spec.options)
		if len(m.buffer) == 0 {
			index, ok = m.cursor, true
		}
		if !ok {
			m.message = fmt.Sprintf("Unknown choice %q", string(m.buffer))
			return
		}
		m.advance(spec, promptAnswer{value: spec.options[index]})
	case tea.KeyBackspace, tea.KeyCtrlH, tea.KeyDelete:
		m.removeLastRune()
	default:
		m.appendRunes(key)
	}
}

func (m *promptModel) updateMulti(spec promptSpec, key tea.KeyMsg) {
	switch key.Type {
	case tea.KeyUp, tea.KeyLeft:
		m.moveCursor(-1, len(spec.options))
	case tea.KeyDown, tea.KeyRight:
		m.moveCursor(1, len(spec.options))
	case tea.KeySpace:
		if len(m.buffer) == 0 {
			m.selected[spec.options[m.cursor]] = !m.selected[spec.options[m.cursor]]
		} else {
			m.appendRunes(key)
		}
	case tea.KeyEnter, tea.KeyCtrlJ:
		if len(m.buffer) == 0 {
			m.advance(spec, promptAnswer{selected: selectedOptions(spec.options, m.selected)})
			return
		}
		selected, ok := parseSelectedOptions(string(m.buffer), spec.options)
		if !ok {
			m.message = fmt.Sprintf("Unknown optional skill in %q", string(m.buffer))
			return
		}
		m.advance(spec, promptAnswer{selected: selected})
	case tea.KeyBackspace, tea.KeyCtrlH, tea.KeyDelete:
		m.removeLastRune()
	default:
		m.appendRunes(key)
	}
}

func (m *promptModel) updateText(spec promptSpec, key tea.KeyMsg) {
	switch key.Type {
	case tea.KeyEnter, tea.KeyCtrlJ:
		value := strings.TrimSpace(string(m.buffer))
		if value == "" {
			value = spec.defaultValue
		}
		m.advance(spec, promptAnswer{value: value})
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

func (m *promptModel) advance(spec promptSpec, answer promptAnswer) {
	m.answers[spec.key] = answer
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
	if spec.kind != choicePrompt {
		return
	}
	if index, ok := findOption(spec.defaultValue, spec.options); ok {
		m.cursor = index
	}
}

func (m *promptModel) View() string {
	if m.done || m.index >= len(m.specs) {
		return ""
	}
	spec := m.specs[m.index]
	var builder strings.Builder
	fmt.Fprintf(&builder, "%s\n\n", spec.label)
	switch spec.kind {
	case choicePrompt:
		for index, option := range spec.options {
			marker := " "
			if index == m.cursor {
				marker = ">"
			}
			fmt.Fprintf(&builder, "%s %s\n", marker, option)
		}
		if len(m.buffer) > 0 {
			fmt.Fprintf(&builder, "\nTyped override: %s\n", string(m.buffer))
		} else {
			fmt.Fprintf(&builder, "\nEnter accepts %s; arrows navigate.\n", spec.options[m.cursor])
		}
	case multiPrompt:
		for index, option := range spec.options {
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
	case textPrompt:
		fmt.Fprintf(&builder, "Type an override, or press Enter for %s:\n> %s\n", spec.defaultValue, string(m.buffer))
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
