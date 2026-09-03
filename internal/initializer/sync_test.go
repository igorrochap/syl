package initializer

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	"github.com/igorrochap/syl/internal/ui"
)

func TestSyncReportHasPlainAndStyledGoldens(t *testing.T) {
	report := syncReport{
		skills: []syncSkill{
			{manifest: manifestSkill{Name: "core-skill", Classification: "core"}, status: syncMissingCore},
			{manifest: manifestSkill{Name: "optional-skill", Classification: "optional"}, status: syncMissingOptional},
			{manifest: manifestSkill{Name: "moved-skill"}, status: syncVendoredMovedForward},
			{
				manifest:  manifestSkill{Name: "local-skill"},
				status:    syncLocallyModified,
				installed: skillSnapshot{Files: map[string][]byte{"SKILL.md": []byte("old\n")}},
				vendored:  skillSnapshot{Files: map[string][]byte{"SKILL.md": []byte("new\n")}},
			},
		},
		extra: []string{"project-skill"},
	}

	for _, test := range []struct {
		name  string
		color bool
	}{
		{name: "plain"},
		{name: "styled", color: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			renderer := ui.New(&output, ui.Caps{Color: test.color, Width: 120, Unicode: true})
			if err := writeSyncReport(renderer, &output, report); err != nil {
				t.Fatal(err)
			}
			assertInitializerGolden(t, "sync-"+test.name+".golden", output.Bytes())
		})
	}
}

func TestSyncDecisionPromptHasPlainAndStyledGoldens(t *testing.T) {
	skill := syncSkill{
		manifest:  manifestSkill{Name: "local-skill"},
		status:    syncLocallyModified,
		installed: skillSnapshot{Files: map[string][]byte{"SKILL.md": []byte("old\n")}},
		vendored:  skillSnapshot{Files: map[string][]byte{"SKILL.md": []byte("new\n")}},
	}

	for _, test := range syncGoldenModes() {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			renderer := ui.New(&output, ui.Caps{Color: test.color, Width: 120, Unicode: true})
			decision, err := promptSyncDecision(bufio.NewReader(strings.NewReader("keep\n")), renderer, &output, skill)
			if err != nil {
				t.Fatal(err)
			}
			if decision != syncKeep {
				t.Fatalf("decision = %q, want %q", decision, syncKeep)
			}
			assertInitializerGolden(t, "sync-decision-"+test.name+".golden", output.Bytes())
		})
	}
}

func TestSyncReportForNoInstalledSkillsHasPlainAndStyledGoldens(t *testing.T) {
	for _, test := range syncGoldenModes() {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			renderer := ui.New(&output, ui.Caps{Color: test.color, Width: 120, Unicode: true})
			if err := writeSyncReport(renderer, &output, syncReport{}); err != nil {
				t.Fatal(err)
			}
			assertInitializerGolden(t, "sync-empty-"+test.name+".golden", output.Bytes())
		})
	}
}

func TestSyncCompletionMessagesHavePlainAndStyledGoldens(t *testing.T) {
	for _, test := range syncGoldenModes() {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			renderer := ui.New(&output, ui.Caps{Color: test.color, Width: 120, Unicode: true})
			if err := renderer.Text("Skills are up to date."); err != nil {
				t.Fatal(err)
			}
			assertInitializerGolden(t, "sync-up-to-date-"+test.name+".golden", output.Bytes())
		})
	}

	for _, test := range syncGoldenModes() {
		t.Run("updated/"+test.name, func(t *testing.T) {
			var output bytes.Buffer
			renderer := ui.New(&output, ui.Caps{Color: test.color, Width: 120, Unicode: true})
			root := t.TempDir()
			report := syncReport{lock: skillsLock{Version: 1, Skills: make(map[string]string)}}
			skill := syncSkill{
				manifest: manifestSkill{Name: "updated-skill"},
				vendored: makeSkillSnapshot(map[string][]byte{"SKILL.md": []byte("vendored\n")}),
			}
			if err := applySkillUpdate(root, renderer, &report, &skill); err != nil {
				t.Fatal(err)
			}
			assertInitializerGolden(t, "sync-updated-"+test.name+".golden", output.Bytes())
		})
	}
}

func syncGoldenModes() []struct {
	name  string
	color bool
} {
	return []struct {
		name  string
		color bool
	}{
		{name: "plain"},
		{name: "styled", color: true},
	}
}
