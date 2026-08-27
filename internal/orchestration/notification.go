package orchestration

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

const (
	unknownNotificationBranch  = "unknown-branch"
	unknownNotificationProject = "unknown-project"
)

type contextualNotifier struct {
	notifier   Notifier
	originRoot string
	workGit    GitRunner
}

var _ Notifier = contextualNotifier{}

func withNotificationContext(notifier Notifier, originRoot string, workGit GitRunner) Notifier {
	if notifier == nil {
		return nil
	}
	return contextualNotifier{notifier: notifier, originRoot: originRoot, workGit: workGit}
}

func (n contextualNotifier) Notify(ctx context.Context, message string) error {
	project := notificationProjectName(n.originRoot)
	branch := notificationBranchName(ctx, n.workGit)
	message = fmt.Sprintf("[project: %s | branch: %s] %s", project, branch, message)
	return n.notifier.Notify(ctx, message)
}

func notificationProjectName(originRoot string) string {
	originRoot = strings.TrimSpace(originRoot)
	if originRoot == "" {
		return unknownNotificationProject
	}
	if absoluteRoot, err := filepath.Abs(originRoot); err == nil {
		originRoot = absoluteRoot
	}
	project := filepath.Base(filepath.Clean(originRoot))
	if project == "" || project == "." || project == string(filepath.Separator) {
		return unknownNotificationProject
	}
	return project
}

func notificationBranchName(ctx context.Context, workGit GitRunner) string {
	if workGit == nil {
		return unknownNotificationBranch
	}
	branch, err := workGit.Run(ctx, "branch", "--show-current")
	if err != nil {
		return unknownNotificationBranch
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return unknownNotificationBranch
	}
	return branch
}
