package initializer

import (
	"reflect"
	"strings"
	"testing"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/tui"
)

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
