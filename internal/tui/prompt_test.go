package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestPromptBackRestoresChoiceAndTextAnswers(t *testing.T) {
	model := newPromptModel([]PromptSpec{
		{Key: "first", Label: "First", Kind: ChoicePrompt, Options: []string{"one", "two"}, DefaultValue: "one"},
		{Key: "second", Label: "Second", Kind: TextPrompt, DefaultValue: "default"},
		{Key: "third", Label: "Third", Kind: ChoicePrompt, Options: []string{"yes", "no"}, DefaultValue: "yes"},
	})

	sendKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	sendKey(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("corrected")})
	sendKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	sendKey(model, tea.KeyMsg{Type: tea.KeyEsc})

	if model.index != 1 {
		t.Fatalf("index after going back = %d, want second question", model.index)
	}
	if got := string(model.buffer); got != "corrected" {
		t.Fatalf("restored text = %q, want corrected", got)
	}
	if !strings.Contains(model.View(), "> corrected") {
		t.Fatalf("view = %q, want restored text buffer", model.View())
	}

	sendKey(model, tea.KeyMsg{Type: tea.KeyLeft})
	if model.index != 0 || model.cursor != 0 {
		t.Fatalf("choice after left = index %d, cursor %d; want first question with one selected", model.index, model.cursor)
	}
}

func TestPromptBackRestoresMultiSelectAnswer(t *testing.T) {
	model := newPromptModel([]PromptSpec{
		{Key: "skills", Label: "Skills", Kind: MultiPrompt, Options: []string{"one", "two"}},
		{Key: "next", Label: "Next", Kind: TextPrompt, DefaultValue: "default"},
	})

	sendKey(model, tea.KeyMsg{Type: tea.KeySpace})
	sendKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	sendKey(model, tea.KeyMsg{Type: tea.KeyEsc})

	if model.index != 0 || !model.selected["one"] {
		t.Fatalf("multi-select state after going back = index %d, selected %#v; want one selected", model.index, model.selected)
	}
}

func TestPromptEscAtFirstQuestionDoesNothing(t *testing.T) {
	model := newPromptModel([]PromptSpec{{
		Key: "first", Label: "First", Kind: ChoicePrompt, Options: []string{"one"}, DefaultValue: "one",
	}})

	model.Update(tea.KeyMsg{Type: tea.KeyEsc})

	if model.done {
		t.Fatal("esc at the first question marked the wizard done")
	}
	if model.index != 0 {
		t.Fatalf("index after esc = %d, want 0", model.index)
	}
}

func TestPromptSkipsConditionalQuestionsAndRecomputesProgress(t *testing.T) {
	model := newPromptModel([]PromptSpec{
		{Key: "tracker", Label: "Tracker", Kind: ChoicePrompt, Options: []string{"github"}, DefaultValue: "github"},
		{Key: "roles", Label: "Role defaults", Kind: ChoicePrompt, Options: []string{"recommended", "configure"}, DefaultValue: "recommended"},
		{Key: "plan.harness", Label: "plan harness", Kind: ChoicePrompt, Options: []string{"claude"}, DefaultValue: "claude", Skip: func(answers map[string]Answer) bool {
			return answers["roles"].Value == "recommended"
		}},
	})
	model.afterPrompts = func(map[string]Answer) (PromptSpec, bool, error) {
		return PromptSpec{Key: "confirmation", Label: "Apply these changes?", Kind: ChoicePrompt, Options: []string{"yes", "no"}, DefaultValue: "no"}, true, nil
	}

	sendKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	sendKey(model, tea.KeyMsg{Type: tea.KeyEnter})

	if model.index != 3 {
		t.Fatalf("index after recommended choice = %d, want confirmation", model.index)
	}
	view := model.View()
	if !strings.Contains(view, "Step 3 of 3") || strings.Contains(view, "plan harness") {
		t.Fatalf("view = %q, want recomputed progress without skipped question", view)
	}
}

func TestPromptConfigurePathPresentsEveryConditionalQuestion(t *testing.T) {
	rolePrompt := func(key string) PromptSpec {
		return PromptSpec{Key: key, Label: key, Kind: TextPrompt, DefaultValue: key, Skip: func(answers map[string]Answer) bool {
			return answers["roles"].Value == "recommended"
		}}
	}
	model := newPromptModel([]PromptSpec{
		{Key: "roles", Label: "Role defaults", Kind: ChoicePrompt, Options: []string{"recommended", "configure"}, DefaultValue: "recommended"},
		rolePrompt("plan"), rolePrompt("implement"), rolePrompt("review"),
	})

	sendKey(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("configure")})
	sendKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	for _, key := range []string{"plan", "implement", "review"} {
		if model.specs[model.index].Key != key {
			t.Fatalf("current key = %q, want %q", model.specs[model.index].Key, key)
		}
		sendKey(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key + " answer")})
		sendKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	}

	if !model.done {
		t.Fatal("configure path did not finish after all role questions")
	}
}

func TestPromptChangingConditionalChoiceClearsStaleAnswer(t *testing.T) {
	rolePrompt := PromptSpec{
		Key: "plan.model", Label: "plan model", Kind: TextPrompt, DefaultValue: "default",
		Skip: func(answers map[string]Answer) bool {
			return answers["roles"].Value == "recommended"
		},
	}
	model := newPromptModel([]PromptSpec{
		{Key: "roles", Label: "Role defaults", Kind: ChoicePrompt, Options: []string{"recommended", "configure"}, DefaultValue: "recommended"},
		rolePrompt,
		{Key: "last", Label: "Last", Kind: TextPrompt, DefaultValue: "last"},
	})

	sendKey(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("configure")})
	sendKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	sendKey(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("custom")})
	sendKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	sendKey(model, tea.KeyMsg{Type: tea.KeyEsc})
	sendKey(model, tea.KeyMsg{Type: tea.KeyEsc})
	sendKey(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("recommended")})
	sendKey(model, tea.KeyMsg{Type: tea.KeyEnter})

	if _, answered := model.answers[rolePrompt.Key]; answered {
		t.Fatalf("stale answer for skipped prompt = %#v, want it cleared", model.answers[rolePrompt.Key])
	}
	if model.index != 2 {
		t.Fatalf("index after changing branch = %d, want last prompt", model.index)
	}
}

func TestPromptRecomputesFinalizerAfterChangingAnEarlierAnswer(t *testing.T) {
	model := newPromptModel([]PromptSpec{
		{Key: "tracker", Label: "Tracker", Kind: ChoicePrompt, Options: []string{"github", "local"}, DefaultValue: "github"},
		{Key: "roles", Label: "Role defaults", Kind: ChoicePrompt, Options: []string{"recommended"}, DefaultValue: "recommended"},
	})
	model.afterPrompts = func(answers map[string]Answer) (PromptSpec, bool, error) {
		return PromptSpec{Key: "confirmation", Label: "Apply " + answers["tracker"].Value, Kind: ChoicePrompt, Options: []string{"yes", "no"}, DefaultValue: "no"}, true, nil
	}

	sendKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	sendKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(model.View(), "Apply github") {
		t.Fatalf("initial confirmation = %q, want github", model.View())
	}

	sendKey(model, tea.KeyMsg{Type: tea.KeyEsc})
	sendKey(model, tea.KeyMsg{Type: tea.KeyEsc})
	sendKey(model, tea.KeyMsg{Type: tea.KeyDown})
	sendKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	sendKey(model, tea.KeyMsg{Type: tea.KeyEnter})

	if !strings.Contains(model.View(), "Apply local") {
		t.Fatalf("recomputed confirmation = %q, want local", model.View())
	}
}

func TestPromptCtrlCAborts(t *testing.T) {
	model := newPromptModel([]PromptSpec{{
		Key: "first", Label: "First", Kind: ChoicePrompt, Options: []string{"one"}, DefaultValue: "one",
	}})

	model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})

	if !model.done || model.err == nil || model.err.Error() != "init cancelled" {
		t.Fatalf("ctrl+c state = done %v, error %v; want init cancelled", model.done, model.err)
	}
}

func TestPromptProgressExcludesAbsentOptionalQuestion(t *testing.T) {
	model := newPromptModel([]PromptSpec{
		{Key: "tracker", Label: "Tracker", Kind: ChoicePrompt, Options: []string{"github"}, DefaultValue: "github"},
		{Key: "roles", Label: "Role defaults", Kind: ChoicePrompt, Options: []string{"recommended"}, DefaultValue: "recommended"},
	})

	if got := model.View(); !strings.Contains(got, "Step 1 of 2") {
		t.Fatalf("view = %q, want two asked questions", got)
	}
}

func sendKey(model *promptModel, key tea.KeyMsg) {
	model.Update(key)
}
