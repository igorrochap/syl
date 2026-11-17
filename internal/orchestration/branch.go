package orchestration

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/igorrochap/syl/internal/tracker"
)

const (
	branchSuggestionPrefix = "Branch:"
	maxBranchNameLength    = 60
)

var (
	branchTypePattern = regexp.MustCompile(`^(feat|fix|refactor|chore|docs|test|perf|build|ci)$`)
	branchSlugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

func resolveBranchName(ticket tracker.Ticket) string {
	suggestion, ok := branchSuggestion(ticket.Body)
	if ok && validBranchName(suggestion) {
		return suggestion
	}
	return branchName(ticket)
}

func currentBranch(ctx context.Context, git GitRunner) string {
	branch, err := git.Run(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(branch)
}

func pullRequestBase(ctx context.Context, git GitRunner, ref string) string {
	ref = strings.TrimSpace(ref)
	if isRevisionExpression(ref) {
		return ""
	}
	branch, ok := pullRequestBranchName(ref)
	if !ok {
		return ""
	}
	if hasNamedBranch(ctx, git, branch) {
		return branch
	}
	return ""
}

func isRevisionExpression(value string) bool {
	return value == "HEAD" || value == "@" || strings.ContainsAny(value, "~^") || strings.Contains(value, "@{")
}

func pullRequestBranchName(ref string) (string, bool) {
	branch := strings.TrimSpace(ref)
	switch {
	case strings.HasPrefix(branch, "origin/"):
		branch = strings.TrimPrefix(branch, "origin/")
	case strings.HasPrefix(branch, "refs/heads/"):
		branch = strings.TrimPrefix(branch, "refs/heads/")
	case strings.HasPrefix(branch, "refs/remotes/origin/"):
		branch = strings.TrimPrefix(branch, "refs/remotes/origin/")
	case strings.HasPrefix(branch, "refs/"):
		return "", false
	}
	if branch == "" || branch == "HEAD" || isRevisionExpression(branch) {
		return "", false
	}
	return branch, true
}

func hasNamedBranch(ctx context.Context, git GitRunner, branch string) bool {
	localBranch := hasBranchReference(ctx, git, "refs/heads/"+branch)
	if localBranch {
		return true
	}
	return hasBranchReference(ctx, git, "refs/remotes/origin/"+branch)
}

func hasBranchReference(ctx context.Context, git GitRunner, ref string) bool {
	if git == nil {
		return false
	}
	_, err := git.Run(ctx, "show-ref", "--verify", "--quiet", ref)
	return err == nil
}

func branchSuggestion(body string) (string, bool) {
	for _, line := range strings.Split(body, "\n") {
		if suggestion, ok := strings.CutPrefix(strings.TrimSpace(line), branchSuggestionPrefix); ok {
			return strings.TrimSpace(suggestion), true
		}
	}
	return "", false
}

func validBranchName(name string) bool {
	branchType, slug, ok := strings.Cut(name, "/")
	return ok && len(name) <= maxBranchNameLength &&
		branchTypePattern.MatchString(branchType) && branchSlugPattern.MatchString(slug)
}

func branchName(ticket tracker.Ticket) string {
	title := strings.TrimSpace(ticket.Title)
	branchType := ""
	contextTitle := title
	for _, label := range ticket.Labels {
		label = strings.ToLower(strings.TrimSpace(label))
		if branchTypePattern.MatchString(label) {
			branchType = label
			break
		}
	}
	if before, after, ok := strings.Cut(title, ":"); ok {
		candidate := strings.ToLower(strings.TrimSpace(before))
		if branchType == "" && branchTypePattern.MatchString(candidate) {
			branchType = candidate
		}
		if branchType != "" || strings.Contains(candidate, "implement") || strings.Contains(candidate, "syl") {
			contextTitle = strings.TrimSpace(after)
		}
	}
	if branchType == "" {
		branchType = "feat"
	}
	context := slugContext(contextTitle)
	if context == "" {
		context = "ticket-" + strconv.Itoa(ticket.Number)
	}
	return branchType + "/" + context
}

func slugContext(value string) string {
	words := strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	stopWords := map[string]bool{"a": true, "an": true, "and": true, "for": true, "in": true, "of": true, "on": true, "the": true, "to": true, "with": true}
	selected := make([]string, 0, 5)
	for _, word := range words {
		if stopWords[word] {
			continue
		}
		selected = append(selected, word)
		if len(selected) == 5 {
			break
		}
	}
	return strings.Join(selected, "-")
}
