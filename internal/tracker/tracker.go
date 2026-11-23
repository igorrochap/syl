// Package tracker defines the tracker boundary shared by local and remote
// ticket backends.
package tracker

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// ErrNoRemote identifies remote tracker failures caused by a missing Git
// repository or origin. Local tracker failures never use this error.
var ErrNoRemote = errors.New("no remote configured")

type noRemoteError string

func (e noRemoteError) Error() string { return string(e) }

func (e noRemoteError) Is(target error) bool { return target == ErrNoRemote }

func newNoRemoteError(message string) error { return noRemoteError(message) }

// Ticket is the tracker-neutral representation used by workflow commands.
type Ticket struct {
	Number int
	Title  string
	Body   string
	Status string
	State  string
	Labels []string
}

// Tracker provides the ticket operations used across the workflow.
type Tracker interface {
	Resolve(ctx context.Context, reference string) (Ticket, error)
	List(ctx context.Context) ([]Ticket, error)
	UpdateStatus(ctx context.Context, number int, status string) error
	AddComment(ctx context.Context, number int, note string) error
	Create(ctx context.Context, title, body string) (Ticket, error)
}

// PullRequest describes the values used to create a draft pull request.
type PullRequest struct {
	Title string
	Body  string
	Head  string
	Base  string
}

// PullRequestCreator is implemented by Trackers that can publish pull requests.
type PullRequestCreator interface {
	CreatePullRequest(ctx context.Context, request PullRequest) (string, error)
	CreatePullRequestCommand(request PullRequest) []string
}

// PullRequestEvidenceImage describes one local image recorded in a Run's Evidence.
type PullRequestEvidenceImage struct {
	Path string
	Alt  string
}

// PullRequestEvidenceAttacher adds a local Evidence image to an existing pull request.
type PullRequestEvidenceAttacher interface {
	AttachPullRequestEvidence(ctx context.Context, pullRequestURL string, image PullRequestEvidenceImage) error
}

func pullRequestNumberFromURL(pullRequestURL, resourceName, resourceSegment string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(pullRequestURL))
	validURL := err == nil && parsed.Scheme != "" && parsed.Host != ""
	if !validURL {
		return "", fmt.Errorf("invalid %s URL %q", resourceName, pullRequestURL)
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) < 2 || segments[len(segments)-2] != resourceSegment {
		return "", fmt.Errorf("%s URL %q has no number", resourceName, pullRequestURL)
	}
	number := segments[len(segments)-1]
	parsedNumber, err := strconv.Atoi(number)
	if err != nil || parsedNumber < 1 {
		return "", fmt.Errorf("%s URL %q has no number", resourceName, pullRequestURL)
	}
	return number, nil
}

func pullRequestEvidencePath(image PullRequestEvidenceImage, forgeName string) (string, error) {
	path := strings.TrimSpace(image.Path)
	if path == "" {
		return "", fmt.Errorf("cannot attach a %s Evidence image without a file path", forgeName)
	}
	return path, nil
}

// RequirePullRequestCreator rejects a Tracker that cannot publish approved implementations.
func RequirePullRequestCreator(issueTracker Tracker) error {
	if _, ok := issueTracker.(PullRequestCreator); ok {
		return nil
	}
	return errors.New("--pr-on-approve requires a GitHub or GitLab Tracker")
}

// PullRequestBaseResolver resolves the forge default branch used when a Run
// starts from a detached or raw revision.
type PullRequestBaseResolver interface {
	DefaultPullRequestBase(ctx context.Context) (string, error)
}
