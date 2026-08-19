package orchestration

import (
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
