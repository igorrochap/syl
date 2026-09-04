package initializer

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/tui"
	"github.com/igorrochap/syl/internal/ui"
)

var updateInitializerGolden = flag.Bool("update-initializer-golden", false, "rewrite initializer golden files")

func TestInitPromptSpecsOfferGitLabForIssues(t *testing.T) {
	spec := initPromptSpec(t, skillManifest{}, "tracker.issues")

	if !reflect.DeepEqual(spec.Options, []string{"github", "local", "gitlab"}) {
		t.Fatalf("issues tracker options = %#v, want github, local, gitlab", spec.Options)
	}
	if spec.DefaultValue != "github" {
		t.Fatalf("issues tracker default = %q, want github", spec.DefaultValue)
	}
}

func TestInitPromptSpecsOfferGitLabForReviews(t *testing.T) {
	spec := initPromptSpec(t, skillManifest{}, "tracker.reviews")

	if !reflect.DeepEqual(spec.Options, []string{"github", "local", "gitlab"}) {
		t.Fatalf("review log options = %#v, want github, local, gitlab", spec.Options)
	}
	if spec.DefaultValue != "local" {
		t.Fatalf("review log default = %q, want local", spec.DefaultValue)
	}
}

func TestInitPromptSpecsRoleChoiceSkipsRoleQuestionsForRecommendedDefaults(t *testing.T) {
	specs := initPromptSpecs(skillManifest{})
	if len(specs) != 12 {
		t.Fatalf("prompt count without optional skills = %d, want 12", len(specs))
	}
	roleSpec := initPromptSpec(t, skillManifest{}, "roles")
	if !reflect.DeepEqual(roleSpec.Options, []string{recommendedRoles, configuredRoles}) {
		t.Fatalf("role choice options = %#v, want recommended/configure", roleSpec.Options)
	}
	if roleSpec.DefaultValue != recommendedRoles {
		t.Fatalf("role choice default = %q, want %q", roleSpec.DefaultValue, recommendedRoles)
	}

	answers := map[string]tui.Answer{"roles": {Value: recommendedRoles}}
	for _, spec := range specs {
		if strings.HasPrefix(spec.Key, "plan.") || strings.HasPrefix(spec.Key, "implement.") || strings.HasPrefix(spec.Key, "review.") {
			if spec.Skip == nil || !spec.Skip(answers) {
				t.Fatalf("role prompt %q is not skipped for recommended defaults", spec.Key)
			}
		}
	}
}

func TestRecommendedRoleDefaultsWriteTheSameConfigAsAcceptingEachDefault(t *testing.T) {
	manifest, err := readSkillManifest()
	if err != nil {
		t.Fatalf("readSkillManifest() error = %v", err)
	}
	recommended := runInitForRoleMode(t, manifest, recommendedRoles)
	configured := runInitForRoleMode(t, manifest, configuredRoles)
	if !bytes.Equal(recommended, configured) {
		t.Fatalf("recommended config differs from configured defaults:\nrecommended:\n%s\nconfigured:\n%s", recommended, configured)
	}
}

func TestInitPromptRendererHasPlainAndStyledGoldens(t *testing.T) {
	view := tui.PromptView{
		Step: 2, Total: 5, Label: "Issues tracker",
		Options: []tui.PromptOption{
			{Label: "github", Cursor: true},
			{Label: "local"},
			{Label: "gitlab"},
		},
		Hint: "Enter accepts github; arrows navigate; Esc goes back.",
	}
	for _, test := range []struct {
		name  string
		color bool
	}{
		{name: "plain"},
		{name: "styled", color: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			actual := []byte(promptRenderer(ui.Caps{Color: test.color, Width: 48, Unicode: true})(view))
			assertInitializerGolden(t, test.name+"-wizard.golden", actual)
		})
	}
}

func TestRunWithGitLabTrackersWritesLoadableConfig(t *testing.T) {
	root := t.TempDir()
	manifest, err := readSkillManifest()
	if err != nil {
		t.Fatalf("readSkillManifest() error = %v", err)
	}
	answers := make([]string, 0, len(initPromptSpecs(manifest)))
	for _, spec := range initPromptSpecs(manifest) {
		answer := ""
		if spec.Key == "tracker.issues" || spec.Key == "tracker.reviews" {
			answer = "gitlab"
		}
		answers = append(answers, answer)
	}

	var output strings.Builder
	input := strings.NewReader(strings.Join(answers, "\n") + "\n")
	if err := Run(root, input, &output); err != nil {
		t.Fatalf("Run() error = %v; output = %q", err, output.String())
	}

	got, err := config.Load(root)
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	if got.Tracker.Issues != config.TrackerGitLab || got.Tracker.Reviews != config.TrackerGitLab {
		t.Fatalf("tracker = %#v, want gitlab/gitlab", got.Tracker)
	}
}

func initPromptSpec(t *testing.T, manifest skillManifest, key string) tui.PromptSpec {
	t.Helper()
	for _, spec := range initPromptSpecs(manifest) {
		if spec.Key == key {
			return spec
		}
	}
	t.Fatalf("prompt spec %q not found", key)
	return tui.PromptSpec{}
}

func runInitForRoleMode(t *testing.T, manifest skillManifest, mode string) []byte {
	t.Helper()
	answers := make([]string, 0, len(initPromptSpecs(manifest)))
	for _, spec := range initPromptSpecs(manifest) {
		if spec.Key == "roles" {
			answers = append(answers, mode)
			continue
		}
		answers = append(answers, "")
	}
	root := t.TempDir()
	var output strings.Builder
	if err := Run(root, strings.NewReader(strings.Join(answers, "\n")+"\n"), &output); err != nil {
		t.Fatalf("Run(%s) error = %v; output = %q", mode, err, output.String())
	}
	contents, err := os.ReadFile(config.Path(root))
	if err != nil {
		t.Fatalf("read config after %s init: %v", mode, err)
	}
	return contents
}

func assertInitializerGolden(t *testing.T, name string, actual []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *updateInitializerGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, actual, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, want) {
		t.Fatalf("golden %s mismatch:\n got: %q\nwant: %q", name, actual, want)
	}
}
