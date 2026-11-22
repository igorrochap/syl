package orchestration

import (
	"reflect"
	"strings"
	"testing"
)

func TestProtectedPathsInReviewPrompt(t *testing.T) {
	tests := []struct {
		name string
		diff string
		want []string
	}{
		{"gate", "diff --git a/scripts/quality.sh b/scripts/quality.sh\n", []string{"scripts/quality.sh"}},
		{"skill", "diff --git a/.agents/skills/code-review/SKILL.md b/.agents/skills/code-review/SKILL.md\n", []string{".agents/skills/code-review/SKILL.md"}},
		{"configured", "diff --git a/.golangci.yml b/.golangci.yml\n", []string{".golangci.yml"}},
		{"verify", "diff --git a/.syl/verify.md b/.syl/verify.md\n", []string{".syl/verify.md"}},
		{"unprotected", "diff --git a/scripts/quality.sh.extra b/scripts/quality.sh.extra\n", nil},
		{"rename", "diff --git a/.golangci.yml b/ordinary.yml\n", []string{".golangci.yml"}},
		{"quoted", "diff --git \"a/.agents/skills/a\\tb/SKILL.md\" \"b/.agents/skills/a\\tb/SKILL.md\"\n", []string{".agents/skills/a\tb/SKILL.md"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			paths := protectedPathsInDiff(test.diff, []string{".golangci.yml"})
			if len(paths) == 0 {
				paths = nil
			}
			if !reflect.DeepEqual(paths, test.want) {
				t.Fatalf("paths = %q, want %q", paths, test.want)
			}
			prompt := appendProtectedPaths(composeReviewPrompt("", nil, "base", reviewPromptContext{
				diffPath: "diff", evidence: noIterationReviewEvidence(),
			}), paths)
			if len(paths) == 0 {
				if strings.Contains(prompt, "Protected paths") {
					t.Fatal("unexpected protected-path section")
				}
				return
			}
			if !strings.Contains(prompt, "[blocking]") || !strings.Contains(prompt, "ticket did not request") {
				t.Fatal("missing blocking instruction")
			}
			for _, path := range paths {
				if !strings.Contains(prompt, "- "+path) {
					t.Fatalf("prompt omitted %q", path)
				}
			}
		})
	}
}
