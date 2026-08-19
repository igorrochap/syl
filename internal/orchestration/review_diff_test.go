package orchestration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitadapter "github.com/igorrochap/syl/internal/adapters/git"
)

func TestPrepareReviewRecordsCompleteDiff(t *testing.T) {
	const (
		trackedDiff = "diff --git a/tracked.txt b/tracked.txt\n--- a/tracked.txt\n+++ b/tracked.txt\n@@ -1 +1 @@\n-old\n+new\n"
		newADiff    = "diff --git a/a.txt b/a.txt\nnew file mode 100644\n--- /dev/null\n+++ b/a.txt\n@@ -0,0 +1 @@\n+a\n"
		newZDiff    = "diff --git a/z.txt b/z.txt\nnew file mode 100644\n--- /dev/null\n+++ b/z.txt\n@@ -0,0 +1 @@\n+z\n"
		binaryDiff  = "diff --git a/image.png b/image.png\nnew file mode 100644\nBinary files /dev/null and b/image.png differ\n"
	)

	tests := []struct {
		name      string
		responses map[string]reviewDiffResponse
		want      string
		wantErr   string
		wantCalls []string
	}{
		{
			name: "tracked only",
			responses: map[string]reviewDiffResponse{
				"rev-parse HEAD":                          {output: "branch-point\n"},
				"diff branch-point":                       {output: trackedDiff},
				"ls-files --others --exclude-standard -z": {},
			},
			want: trackedDiff,
			wantCalls: []string{
				"rev-parse HEAD",
				"diff branch-point",
				"ls-files --others --exclude-standard -z",
			},
		},
		{
			name: "untracked only",
			responses: map[string]reviewDiffResponse{
				"rev-parse HEAD":                          {output: "branch-point\n"},
				"diff branch-point":                       {},
				"ls-files --others --exclude-standard -z": {output: "a.txt\x00"},
				"diff --no-index -- /dev/null a.txt":      {output: newADiff, err: exitCodeError(1)},
			},
			want: newADiff,
			wantCalls: []string{
				"rev-parse HEAD",
				"diff branch-point",
				"ls-files --others --exclude-standard -z",
				"diff --no-index -- /dev/null a.txt",
			},
		},
		{
			name: "mixed with deterministic untracked ordering",
			responses: map[string]reviewDiffResponse{
				"rev-parse HEAD":                          {output: "branch-point\n"},
				"diff branch-point":                       {output: trackedDiff},
				"ls-files --others --exclude-standard -z": {output: "z.txt\x00a.txt\x00"},
				"diff --no-index -- /dev/null a.txt":      {output: newADiff, err: exitCodeError(1)},
				"diff --no-index -- /dev/null z.txt":      {output: newZDiff, err: exitCodeError(1)},
			},
			want: trackedDiff + newADiff + newZDiff,
			wantCalls: []string{
				"rev-parse HEAD",
				"diff branch-point",
				"ls-files --others --exclude-standard -z",
				"diff --no-index -- /dev/null a.txt",
				"diff --no-index -- /dev/null z.txt",
			},
		},
		{
			name: "ignored files excluded",
			responses: map[string]reviewDiffResponse{
				"rev-parse HEAD":                          {output: "branch-point\n"},
				"diff branch-point":                       {},
				"ls-files --others --exclude-standard -z": {output: "a.txt\x00"},
				"diff --no-index -- /dev/null a.txt":      {output: newADiff, err: exitCodeError(1)},
			},
			want: newADiff,
			wantCalls: []string{
				"rev-parse HEAD",
				"diff branch-point",
				"ls-files --others --exclude-standard -z",
				"diff --no-index -- /dev/null a.txt",
			},
		},
		{
			name: "binary untracked file",
			responses: map[string]reviewDiffResponse{
				"rev-parse HEAD":                          {output: "branch-point\n"},
				"diff branch-point":                       {},
				"ls-files --others --exclude-standard -z": {output: "image.png\x00"},
				"diff --no-index -- /dev/null image.png":  {output: binaryDiff, err: exitCodeError(1)},
			},
			want: binaryDiff,
			wantCalls: []string{
				"rev-parse HEAD",
				"diff branch-point",
				"ls-files --others --exclude-standard -z",
				"diff --no-index -- /dev/null image.png",
			},
		},
		{
			name: "empty",
			responses: map[string]reviewDiffResponse{
				"rev-parse HEAD":                          {output: "branch-point\n"},
				"diff branch-point":                       {},
				"ls-files --others --exclude-standard -z": {},
			},
			wantErr: "pre-computed diff against branch-point is empty",
			wantCalls: []string{
				"rev-parse HEAD",
				"diff branch-point",
				"ls-files --others --exclude-standard -z",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			git := &reviewDiffGit{responses: tt.responses}
			preparation, err := prepareReview(context.Background(), t.TempDir(), "", git)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("prepareReview() error = %v, want containing %q", err, tt.wantErr)
				}
			} else {
				if err != nil {
					t.Fatalf("prepareReview() error = %v", err)
				}
				diff, readErr := os.ReadFile(preparation.diffPath)
				if readErr != nil {
					t.Fatalf("read review diff: %v", readErr)
				}
				if string(diff) != tt.want {
					t.Fatalf("review diff = %q, want %q", diff, tt.want)
				}
			}
			if got := strings.Join(git.calls, "\n"); got != strings.Join(tt.wantCalls, "\n") {
				t.Fatalf("git calls = %q, want %q", got, strings.Join(tt.wantCalls, "\n"))
			}
		})
	}
}

func TestPrepareReviewDiffLeavesRepositoryStateUnchanged(t *testing.T) {
	root := t.TempDir()
	git := gitadapter.ExecGitRunner{Dir: root}
	runReviewDiffGit(t, git, "init", "--quiet")
	if err := os.WriteFile(
		filepath.Join(root, ".gitignore"),
		[]byte(".syl/runs/\nignored.bin\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runReviewDiffGit(t, git, "add", ".")
	runReviewDiffGit(
		t,
		git,
		"-c", "user.name=syl test",
		"-c", "user.email=syl@example.test",
		"commit", "--quiet", "-m", "fixture",
	)

	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("new file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "image.bin"), []byte{0, 1, 2}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored.bin"), []byte{0, 1, 2}, 0o644); err != nil {
		t.Fatal(err)
	}

	beforeHead := runReviewDiffGit(t, git, "rev-parse", "HEAD")
	beforeStatus := runReviewDiffGit(t, git, "status", "--porcelain", "--untracked-files=all")
	preparation, err := prepareReview(context.Background(), root, "", git)
	if err != nil {
		t.Fatalf("prepareReview() error = %v", err)
	}
	diff, err := os.ReadFile(preparation.diffPath)
	if err != nil {
		t.Fatalf("read review diff: %v", err)
	}

	for _, want := range []string{
		"diff --git a/tracked.txt b/tracked.txt",
		"diff --git a/a.txt b/a.txt",
		"new file mode",
		"--- /dev/null",
		"+++ b/a.txt",
		"Binary files /dev/null and b/image.bin differ",
	} {
		if !strings.Contains(string(diff), want) {
			t.Errorf("review diff does not contain %q:\n%s", want, diff)
		}
	}
	if strings.Contains(string(diff), "ignored.bin") {
		t.Fatalf("review diff contains ignored file:\n%s", diff)
	}
	if strings.Index(string(diff), "a/a.txt") > strings.Index(string(diff), "a/image.bin") {
		t.Fatalf("untracked files are not sorted in review diff:\n%s", diff)
	}

	afterHead := runReviewDiffGit(t, git, "rev-parse", "HEAD")
	afterStatus := runReviewDiffGit(t, git, "status", "--porcelain", "--untracked-files=all")
	if afterHead != beforeHead {
		t.Errorf("HEAD changed from %q to %q", beforeHead, afterHead)
	}
	if afterStatus != beforeStatus {
		t.Errorf("working tree changed from %q to %q", beforeStatus, afterStatus)
	}
}

func runReviewDiffGit(t *testing.T, git gitadapter.ExecGitRunner, args ...string) string {
	t.Helper()
	output, err := git.Run(context.Background(), args...)
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return output
}

type reviewDiffResponse struct {
	output string
	err    error
}

type reviewDiffGit struct {
	responses map[string]reviewDiffResponse
	calls     []string
}

func (g *reviewDiffGit) Run(_ context.Context, args ...string) (string, error) {
	call := strings.Join(args, " ")
	g.calls = append(g.calls, call)
	response, ok := g.responses[call]
	if !ok {
		return "", fmt.Errorf("unexpected git command %q", call)
	}
	return response.output, response.err
}

type exitCodeError int

func (e exitCodeError) Error() string { return fmt.Sprintf("exit status %d", e) }

func (e exitCodeError) ExitCode() int { return int(e) }
