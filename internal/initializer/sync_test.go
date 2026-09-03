package initializer

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
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

func TestSyncRunsReportAndAppliesCoreSkills(t *testing.T) {
	root := t.TempDir()
	var output bytes.Buffer
	if err := Sync(root, SyncOptions{Output: &output, All: true}); err != nil {
		t.Fatal(err)
	}

	output.Reset()
	if err := Sync(root, SyncOptions{Output: &output}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Skills are up to date.") {
		t.Fatalf("sync output = %q, want up-to-date message", output.String())
	}
}

func TestSyncPromptsForLocallyModifiedSkill(t *testing.T) {
	root, skillName := locallyModifiedSyncProject(t)
	var output bytes.Buffer
	if err := Sync(root, SyncOptions{Input: strings.NewReader("keep\n"), Output: &output}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Decision for "+skillName) {
		t.Fatalf("sync output = %q, want decision prompt", output.String())
	}
}

func TestSyncRequiresADecisionForLocallyModifiedSkill(t *testing.T) {
	root, _ := locallyModifiedSyncProject(t)
	var output bytes.Buffer
	if err := Sync(root, SyncOptions{Input: strings.NewReader(""), Output: &output}); err == nil {
		t.Fatal("Sync() error = nil, want missing decision error")
	}
}

func TestSyncPropagatesRendererErrors(t *testing.T) {
	t.Run("report", func(t *testing.T) {
		writer := &syncFailAtWriter{failAt: 1}
		if err := Sync(t.TempDir(), SyncOptions{Output: writer, DryRun: true}); err == nil {
			t.Fatal("Sync() error = nil, want report write error")
		}
	})

	t.Run("up to date", func(t *testing.T) {
		root := fullySyncedProject(t)
		writer := &syncFailOnWriter{needle: "Skills are up to date."}
		if err := Sync(root, SyncOptions{Output: writer}); err == nil {
			t.Fatal("Sync() error = nil, want completion write error")
		}
	})
}

func TestReconcileSyncLockUpdatesAndRemovesEntries(t *testing.T) {
	root := t.TempDir()
	report := syncReport{
		lock: skillsLock{Version: 1, Skills: map[string]string{
			"changed": "old-hash",
			"missing": "missing-hash",
			"present": "present-hash",
		}},
		skills: []syncSkill{
			{manifest: manifestSkill{Name: "changed"}, status: syncUnchanged, present: true, vendored: skillSnapshot{Hash: "new-hash"}},
			{manifest: manifestSkill{Name: "missing"}, status: syncMissingCore},
			{manifest: manifestSkill{Name: "present"}, status: syncLocallyModified, present: true},
		},
	}

	if err := reconcileSyncLock(root, &report); err != nil {
		t.Fatal(err)
	}
	if err := reconcileSyncLock(root, &report); err != nil {
		t.Fatal(err)
	}
	lock, _, err := readSkillsLock(root)
	if err != nil {
		t.Fatal(err)
	}
	if lock.Skills["changed"] != "new-hash" {
		t.Fatalf("changed lock hash = %q, want new-hash", lock.Skills["changed"])
	}
	if _, ok := lock.Skills["missing"]; ok {
		t.Fatal("missing skill lock entry was not removed")
	}
	if lock.Skills["present"] != "present-hash" {
		t.Fatalf("present lock hash = %q, want present-hash", lock.Skills["present"])
	}
}

func TestApplySkillUpdatePropagatesErrors(t *testing.T) {
	skill := syncSkill{
		manifest: manifestSkill{Name: "updated-skill"},
		vendored: makeSkillSnapshot(map[string][]byte{"SKILL.md": []byte("vendored\n")}),
	}

	t.Run("update", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Symlink("target", filepath.Join(root, ".agents")); err != nil {
			t.Fatal(err)
		}
		report := syncReport{lock: skillsLock{Version: 1, Skills: make(map[string]string)}}
		renderer := ui.New(io.Discard, ui.Caps{Width: 120, Unicode: true})
		if err := applySkillUpdate(root, renderer, &report, &skill); err == nil {
			t.Fatal("applySkillUpdate() error = nil, want update error")
		}
	})

	t.Run("render", func(t *testing.T) {
		root := t.TempDir()
		writer := &syncFailAtWriter{failAt: 1}
		renderer := ui.New(writer, ui.Caps{Width: 120, Unicode: true})
		report := syncReport{lock: skillsLock{Version: 1, Skills: make(map[string]string)}}
		if err := applySkillUpdate(root, renderer, &report, &skill); err == nil {
			t.Fatal("applySkillUpdate() error = nil, want renderer error")
		}
	})
}

func TestWriteSyncReportPropagatesErrors(t *testing.T) {
	localSkill := syncSkill{
		manifest:  manifestSkill{Name: "local-skill"},
		status:    syncLocallyModified,
		installed: skillSnapshot{Files: map[string][]byte{"SKILL.md": []byte("old\n")}},
		vendored:  skillSnapshot{Files: map[string][]byte{"SKILL.md": []byte("new\n")}},
	}
	tests := []struct {
		name   string
		report syncReport
		failAt int
	}{
		{name: "title", failAt: 1},
		{name: "skill", report: syncReport{skills: []syncSkill{{manifest: manifestSkill{Name: "skill"}, status: syncMissingCore}}}, failAt: 2},
		{name: "diff", report: syncReport{skills: []syncSkill{localSkill}}, failAt: 3},
		{name: "warning", report: syncReport{skills: []syncSkill{localSkill}}, failAt: 4},
		{name: "extra", report: syncReport{extra: []string{"project-skill"}}, failAt: 2},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			writer := &syncFailAtWriter{failAt: test.failAt}
			renderer := ui.New(writer, ui.Caps{Width: 120, Unicode: true})
			if err := writeSyncReport(renderer, writer, test.report); err == nil {
				t.Fatalf("writeSyncReport() error = nil, want write failure at call %d", test.failAt)
			}
		})
	}
}

func TestPromptSyncDecisionHandlesInvalidAndDiffInputs(t *testing.T) {
	skill := syncSkill{
		manifest:  manifestSkill{Name: "local-skill"},
		status:    syncLocallyModified,
		installed: skillSnapshot{Files: map[string][]byte{"SKILL.md": []byte("old\n")}},
		vendored:  skillSnapshot{Files: map[string][]byte{"SKILL.md": []byte("new\n")}},
	}
	var output bytes.Buffer
	renderer := ui.New(&output, ui.Caps{Width: 120, Unicode: true})
	decision, err := promptSyncDecision(bufio.NewReader(strings.NewReader("invalid\ndiff\nkeep\n")), renderer, &output, skill)
	if err != nil {
		t.Fatal(err)
	}
	if decision != syncKeep {
		t.Fatalf("decision = %q, want %q", decision, syncKeep)
	}
	if !strings.Contains(output.String(), "Choose update") || !strings.Contains(output.String(), "content diff") {
		t.Fatalf("prompt output = %q, want invalid-choice message and diff", output.String())
	}
}

func TestPromptSyncDecisionPropagatesRendererErrors(t *testing.T) {
	tests := []struct {
		name   string
		skill  syncSkill
		failAt int
	}{
		{name: "warning", skill: syncSkill{manifest: manifestSkill{Name: "local"}, status: syncLocallyModified}, failAt: 1},
		{name: "prompt", skill: syncSkill{manifest: manifestSkill{Name: "missing"}, status: syncMissingCore}, failAt: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			writer := &syncFailAtWriter{failAt: test.failAt}
			renderer := ui.New(writer, ui.Caps{Width: 120, Unicode: true})
			if _, err := promptSyncDecision(bufio.NewReader(strings.NewReader("keep\n")), renderer, writer, test.skill); err == nil {
				t.Fatal("promptSyncDecision() error = nil, want renderer error")
			}
		})
	}
}

func TestReadSyncDecisionHandlesReadOutcomes(t *testing.T) {
	skill := syncSkill{
		manifest:  manifestSkill{Name: "local-skill"},
		installed: skillSnapshot{Files: map[string][]byte{"SKILL.md": []byte("old\n")}},
		vendored:  skillSnapshot{Files: map[string][]byte{"SKILL.md": []byte("new\n")}},
	}
	tests := []struct {
		name       string
		input      io.Reader
		output     io.Writer
		want       syncDecision
		handled    bool
		wantErr    bool
		wantOutput string
	}{
		{name: "diff", input: strings.NewReader("diff\n"), wantOutput: "content diff"},
		{name: "diff eof", input: strings.NewReader("diff"), wantErr: true, wantOutput: "content diff"},
		{name: "diff write error", input: strings.NewReader("diff\n"), output: &syncFailAtWriter{failAt: 1}, wantErr: true},
		{name: "update", input: strings.NewReader("update\n"), want: syncUpdate, handled: true},
		{name: "keep", input: strings.NewReader("keep\n"), want: syncKeep, handled: true},
		{name: "invalid", input: strings.NewReader("invalid\n")},
		{name: "eof", input: strings.NewReader(""), wantErr: true},
		{name: "read error", input: syncReadError{}, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := test.output
			if output == nil {
				output = &bytes.Buffer{}
			}
			decision, handled, err := readSyncDecision(bufio.NewReader(test.input), output, skill)
			if (err != nil) != test.wantErr {
				t.Fatalf("readSyncDecision() error = %v, want error %t", err, test.wantErr)
			}
			if decision != test.want || handled != test.handled {
				t.Fatalf("readSyncDecision() = (%q, %t), want (%q, %t)", decision, handled, test.want, test.handled)
			}
			if test.wantOutput != "" {
				if got := output.(*bytes.Buffer).String(); !strings.Contains(got, test.wantOutput) {
					t.Fatalf("diff output = %q, want %q", got, test.wantOutput)
				}
			}
		})
	}
}

func TestWriteSkillDiffPropagatesWriteErrors(t *testing.T) {
	skill := syncSkill{
		manifest:  manifestSkill{Name: "local-skill"},
		installed: skillSnapshot{Files: map[string][]byte{"SKILL.md": []byte("old\n")}},
		vendored:  skillSnapshot{Files: map[string][]byte{"SKILL.md": []byte("new\n")}},
	}
	if err := writeSkillDiff(&syncFailAtWriter{failAt: 1}, skill); err == nil {
		t.Fatal("writeSkillDiff() error = nil, want write error")
	}
	if err := writeSkillDiff(syncShortWriter{}, skill); err == nil {
		t.Fatal("writeSkillDiff() error = nil, want short write error")
	}
}

func fullySyncedProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := Sync(root, SyncOptions{Output: io.Discard, All: true}); err != nil {
		t.Fatal(err)
	}
	return root
}

func locallyModifiedSyncProject(t *testing.T) (string, string) {
	t.Helper()
	root := fullySyncedProject(t)
	manifest, err := readSkillManifest()
	if err != nil {
		t.Fatal(err)
	}
	var skillName string
	for _, skill := range manifest.Skills {
		if skill.Classification == "core" {
			skillName = skill.Name
			break
		}
	}
	if skillName == "" {
		t.Fatal("manifest has no core skill")
	}
	path := filepath.Join(root, ".agents", "skills", skillName, "SKILL.md")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(contents, []byte("\nlocal change\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, skillName
}

type syncFailAtWriter struct {
	failAt int
	writes int
	output bytes.Buffer
}

func (w *syncFailAtWriter) Write(value []byte) (int, error) {
	w.writes++
	if w.writes == w.failAt {
		return 0, errors.New("write failed")
	}
	return w.output.Write(value)
}

type syncFailOnWriter struct {
	needle string
	output bytes.Buffer
}

func (w *syncFailOnWriter) Write(value []byte) (int, error) {
	if bytes.Contains(value, []byte(w.needle)) {
		return 0, errors.New("write failed")
	}
	return w.output.Write(value)
}

type syncReadError struct{}

func (syncReadError) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

type syncShortWriter struct{}

func (syncShortWriter) Write(value []byte) (int, error) {
	return len(value) - 1, nil
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
