package orchestration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/igorrochap/syl/internal/runrecord"
	"github.com/igorrochap/syl/internal/tracker"
	"github.com/igorrochap/syl/internal/verdict"
)

type publicationGateVerification struct {
	// Approved Runs reach publication only after a green Gate attempt.
	ran      bool
	feedback gateAttemptFeedback
}

type publicationVerification struct {
	gates                publicationGateVerification
	evidence             string
	evidenceImages       []tracker.PullRequestEvidenceImage
	evidenceImageNotices []string
	evidenceRecorded     bool
	evidenceUnavailable  bool
}

type publicationEvidence struct {
	summary      string
	images       []tracker.PullRequestEvidenceImage
	imageNotices []string
	recorded     bool
}

var (
	markdownImagePattern = regexp.MustCompile(`!\[([^\]]*)\](?:\([^)]*\)|\s*\[[^\]]*\])?`)
	htmlImagePattern     = regexp.MustCompile(`(?is)<img\b[^>]*>`)
	prWorthyImagePattern = regexp.MustCompile(`(?i)!\[([^\]]*)\]\(\s*(?:<([^>]+)>|([^\s)]+))(?:\s+(?:"[^"]*"|'[^']*'))?\s*\)\s*(?:[-–—:]\s*)?(?:\(\s*)?PR-worthy\b(?:\s*\))?`)
)

func publishApprovedImplement(
	ctx context.Context,
	options ImplementOptions,
	setup implementSetup,
	summary implementSummary,
	runState *runStateTracker,
) (runrecord.Publication, []string, []string) {
	var notices []string
	attachmentNotices := append([]string(nil), summary.verification.evidenceImageNotices...)
	notices = append(notices, attachmentNotices...)
	spans := newActivitySpanContext(options, runState, summary.iterations)
	hasChanges, commitNotice, commitErr := commitApprovedImplementationWithActivity(ctx, options, setup, spans)
	if commitNotice != "" {
		notices = append(notices, commitNotice)
	}
	if commitErr != nil {
		return failedPublication(runrecord.PublicationStepCommit, commitErr), notices, attachmentNotices
	}
	if !hasChanges {
		return runrecord.Publication{Status: runrecord.PublicationSkipped}, notices, attachmentNotices
	}
	if err := pushApprovedImplementationWithActivity(ctx, setup, spans); err != nil {
		return failedPublication(runrecord.PublicationStepPush, err), notices, attachmentNotices
	}
	publication, notices := openApprovedPullRequestWithActivity(ctx, options, setup, summary, spans, notices)
	if publication.Status != runrecord.PublicationPublished {
		return publication, notices, attachmentNotices
	}
	attachmentFailures := attachPullRequestEvidence(
		ctx,
		options.IssueTracker,
		publication.URL,
		summary.verification.evidenceImages,
	)
	attachmentNotices = append(attachmentNotices, attachmentFailures...)
	notices = append(notices, attachmentFailures...)
	return publication, notices, attachmentNotices
}

func commitApprovedImplementationWithActivity(
	ctx context.Context,
	options ImplementOptions,
	setup implementSetup,
	spans activitySpanContext,
) (bool, string, error) {
	var hasChanges bool
	var notice string
	err := spans.run(
		ctx, runrecord.Committing, "approved — committing",
		func() error {
			var stagingErr error
			hasChanges, stagingErr = stageApprovedImplementation(ctx, setup)
			if stagingErr != nil || !hasChanges {
				return stagingErr
			}
			message, messageErr := implementationCommitMessage(options, setup.branch)
			if messageErr != nil {
				return messageErr
			}
			var commitErr error
			notice, commitErr = commitApprovedImplementation(ctx, options, setup, message)
			if commitErr != nil {
				return fmt.Errorf("commit approved implementation: %w", commitErr)
			}
			return nil
		},
	)
	return hasChanges, notice, err
}

func pushApprovedImplementationWithActivity(
	ctx context.Context,
	setup implementSetup,
	spans activitySpanContext,
) error {
	return spans.run(
		ctx, runrecord.Pushing, "approved — pushing",
		func() error {
			if _, err := setup.git.Run(ctx, "push", "-u", "origin", setup.branch); err != nil {
				return fmt.Errorf("push implementation branch %q: %w", setup.branch, err)
			}
			return nil
		},
	)
}

func openApprovedPullRequestWithActivity(
	ctx context.Context,
	options ImplementOptions,
	setup implementSetup,
	summary implementSummary,
	spans activitySpanContext,
	notices []string,
) (runrecord.Publication, []string) {
	var publication runrecord.Publication
	openingErr := spans.run(
		ctx, runrecord.Publishing, "approved — opening PR",
		func() error {
			pullRequest, notice := commitgenPullRequest(ctx, options, setup, summary)
			if notice != "" {
				notices = append(notices, notice)
			}
			pullRequestURL, err := createDraftPullRequest(ctx, options, pullRequest)
			if err != nil {
				return err
			}
			publication = runrecord.Publication{Status: runrecord.PublicationPublished, URL: pullRequestURL}
			return nil
		},
	)
	if openingErr != nil {
		return failedPublication(runrecord.PublicationStepPullRequest, openingErr), notices
	}
	return publication, notices
}

func stageApprovedImplementation(ctx context.Context, setup implementSetup) (bool, error) {
	if _, err := setup.git.Run(ctx, "add", "-A"); err != nil {
		return false, fmt.Errorf("stage approved implementation: %w", err)
	}
	changedPaths, err := setup.git.Run(ctx, "diff", "--cached", "--name-only", setup.branchPoint)
	if err != nil {
		return false, fmt.Errorf("check approved implementation diff: %w", err)
	}
	return strings.TrimSpace(changedPaths) != "", nil
}

func createDraftPullRequest(
	ctx context.Context,
	options ImplementOptions,
	request tracker.PullRequest,
) (string, error) {
	publisher, ok := options.IssueTracker.(tracker.PullRequestCreator)
	if !ok {
		return "", fmt.Errorf("create pull request: Tracker cannot create pull requests")
	}
	pullRequestURL, err := publisher.CreatePullRequest(ctx, request)
	if err != nil {
		return "", fmt.Errorf("create draft pull request: %w", err)
	}
	pullRequestURL = strings.TrimSpace(pullRequestURL)
	if pullRequestURL == "" {
		return "", errors.New("create draft pull request: tracker returned an empty URL")
	}
	return pullRequestURL, nil
}

func attachPullRequestEvidence(
	ctx context.Context,
	issueTracker tracker.Tracker,
	pullRequestURL string,
	images []tracker.PullRequestEvidenceImage,
) []string {
	if len(images) == 0 {
		return nil
	}

	attacher, ok := issueTracker.(tracker.PullRequestEvidenceAttacher)
	if !ok {
		var notices []string
		attachmentError := errors.New("tracker does not support pull request Evidence attachments")
		for _, image := range images {
			notices = append(notices, formatPullRequestEvidenceNotice(image, attachmentError))
		}
		return notices
	}

	var notices []string
	for _, image := range images {
		if err := attacher.AttachPullRequestEvidence(ctx, pullRequestURL, image); err != nil {
			notices = append(notices, formatPullRequestEvidenceNotice(image, err))
		}
	}
	return notices
}

func formatPullRequestEvidenceNotice(image tracker.PullRequestEvidenceImage, reason error) string {
	return fmt.Sprintf("Could not attach PR-worthy Evidence image %q (%s): %v", image.Alt, image.Path, reason)
}

func commitApprovedImplementation(
	ctx context.Context,
	options ImplementOptions,
	setup implementSetup,
	fallbackMessage string,
) (string, error) {
	if options.Commitgen == nil || !options.Commitgen.Available() {
		if _, commitErr := setup.git.Run(ctx, "commit", "-m", fallbackMessage); commitErr != nil {
			return "commitgen unavailable; used fallback commit message", commitErr
		}
		return "commitgen unavailable; used fallback commit message", nil
	}

	snapshot, err := snapshotStagedPublication(ctx, setup.git)
	if err != nil {
		return "", err
	}

	args := []string{"--agent", "--context", fmt.Sprintf("Ticket #%d: %s", options.Ticket.Number, options.Ticket.Title)}
	args = commitgenLanguageArguments(args, options.ProjectConfig.Publish.Language)
	_, commandErr := options.Commitgen.Run(ctx, options.WorkRoot, args...)
	completeCommit, headMoved, verifyErr := commitgenCreatedOneCompleteCommit(ctx, setup.git, snapshot.head, snapshot.tree)
	if commandErr == nil && verifyErr == nil && completeCommit {
		return "", nil
	}

	reason := commitgenFallbackNotice(commandErr, verifyErr, headMoved)
	return fallbackPublicationCommit(ctx, setup, snapshot.head, snapshot.tree, fallbackMessage, reason)
}

type stagedPublicationSnapshot struct {
	head string
	tree string
}

func snapshotStagedPublication(ctx context.Context, git GitRunner) (stagedPublicationSnapshot, error) {
	head, err := git.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return stagedPublicationSnapshot{}, fmt.Errorf("record HEAD before commitgen: %w", err)
	}
	head = strings.TrimSpace(head)
	if head == "" {
		return stagedPublicationSnapshot{}, errors.New("record HEAD before commitgen: git returned an empty ref")
	}
	tree, err := git.Run(ctx, "write-tree")
	if err != nil {
		return stagedPublicationSnapshot{}, fmt.Errorf("record staged implementation tree before commitgen: %w", err)
	}
	tree = strings.TrimSpace(tree)
	if tree == "" {
		return stagedPublicationSnapshot{}, errors.New("record staged implementation tree before commitgen: git returned an empty tree")
	}
	return stagedPublicationSnapshot{head: head, tree: tree}, nil
}

func commitgenFallbackNotice(commandErr, verifyErr error, headMoved bool) string {
	switch {
	case commandErr != nil:
		return fmt.Sprintf("commitgen failed; used fallback commit message: %v", commandErr)
	case verifyErr != nil:
		return "commitgen commit could not be verified; used fallback commit message"
	case headMoved:
		return "commitgen did not create one complete commit; used fallback commit message"
	default:
		return "commitgen left HEAD unchanged; used fallback commit message"
	}
}

func commitgenCreatedOneCompleteCommit(
	ctx context.Context,
	git GitRunner,
	headBefore string,
	stagedTree string,
) (bool, bool, error) {
	headAfter, err := git.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return false, false, err
	}
	headAfter = strings.TrimSpace(headAfter)
	if headAfter == headBefore {
		return false, false, nil
	}
	parents, err := git.Run(ctx, "rev-list", "--parents", "-n", "1", "HEAD")
	if err != nil {
		return false, true, fmt.Errorf("inspect commit parents: %w", err)
	}
	tree, err := git.Run(ctx, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return false, true, err
	}
	parentFields := strings.Fields(parents)
	completeCommit := len(parentFields) == 2 && parentFields[0] == headAfter &&
		parentFields[1] == headBefore && strings.TrimSpace(tree) == stagedTree
	return completeCommit, true, nil
}

func fallbackPublicationCommit(
	ctx context.Context,
	setup implementSetup,
	headBefore string,
	stagedTree string,
	fallbackMessage string,
	reason string,
) (string, error) {
	if _, err := setup.git.Run(ctx, "reset", "--soft", headBefore); err != nil {
		return reason, fmt.Errorf("restore HEAD after commitgen failure: %w", err)
	}
	if _, err := setup.git.Run(ctx, "read-tree", stagedTree); err != nil {
		return reason, fmt.Errorf("restore staged implementation after commitgen failure: %w", err)
	}
	if _, err := setup.git.Run(ctx, "commit", "-m", fallbackMessage); err != nil {
		return reason, err
	}
	return reason, nil
}

func commitgenPullRequest(
	ctx context.Context,
	options ImplementOptions,
	setup implementSetup,
	summary implementSummary,
) (tracker.PullRequest, string) {
	fallback := tracker.PullRequest{
		Title: options.Ticket.Title,
		Body:  pullRequestBody(options.Ticket.Number, summary),
		Head:  setup.branch,
		Base:  setup.baseBranch,
	}
	if options.Commitgen == nil || !options.Commitgen.Available() {
		return fallback, "commitgen unavailable; used fallback pull request text"
	}

	base := strings.TrimSpace(setup.baseBranch)
	var err error
	if base == "" {
		resolver, ok := options.IssueTracker.(tracker.PullRequestBaseResolver)
		if !ok {
			return fallback, "forge default branch unavailable; used fallback pull request text"
		}
		base, err = resolver.DefaultPullRequestBase(ctx)
		if err != nil || strings.TrimSpace(base) == "" {
			return fallback, "forge default branch unavailable; used fallback pull request text"
		}
		base = strings.TrimSpace(base)
	}

	args := []string{"pr", "--agent", "--base", base, "--head", setup.branch}
	args = commitgenLanguageArguments(args, options.ProjectConfig.Publish.Language)
	output, err := options.Commitgen.Run(ctx, options.WorkRoot, args...)
	if err != nil {
		return pullRequestWithBase(fallback, base), fmt.Sprintf("commitgen pr failed; used fallback pull request text: %v", err)
	}
	title, description, ok := parseCommitgenPullRequest(output)
	if !ok {
		return pullRequestWithBase(fallback, base), "commitgen pr printed no usable title; used fallback pull request text"
	}

	description = ensureClosingReference(description, options.Ticket.Number)
	description = appendVerificationSection(description, summary.verification)
	return tracker.PullRequest{
		Title: title,
		Body:  appendNitChecklist(description, summary.nits),
		Head:  setup.branch,
		Base:  base,
	}, ""
}

func commitgenLanguageArguments(args []string, language string) []string {
	language = strings.TrimSpace(language)
	if language == "" {
		return args
	}
	return append(args, "--language", language)
}

func pullRequestWithBase(request tracker.PullRequest, base string) tracker.PullRequest {
	request.Base = base
	return request
}

func parseCommitgenPullRequest(output string) (string, string, bool) {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	for index, line := range lines {
		title := strings.TrimSpace(line)
		if title == "" {
			continue
		}
		for _, prefix := range []string{"Title:", "Titulo:", "Título:"} {
			if strings.HasPrefix(title, prefix) {
				title = strings.TrimSpace(strings.TrimPrefix(title, prefix))
				break
			}
		}
		if title == "" {
			return "", "", false
		}
		return title, strings.TrimSpace(strings.Join(lines[index+1:], "\n")), true
	}
	return "", "", false
}

func ensureClosingReference(description string, ticketNumber int) string {
	reference := fmt.Sprintf("Closes #%d", ticketNumber)
	first := closingReferenceIndex(description, reference, 0)
	if first < 0 {
		if description == "" {
			return reference
		}
		return description + "\n\n" + reference
	}
	suffix := description[first+len(reference):]
	var clean strings.Builder
	clean.WriteString(description[:first+len(reference)])
	position := 0
	for {
		duplicate := closingReferenceIndex(suffix, reference, position)
		if duplicate < 0 {
			clean.WriteString(suffix[position:])
			return strings.TrimSpace(clean.String())
		}
		clean.WriteString(suffix[position:duplicate])
		position = duplicate + len(reference)
	}
}

func closingReferenceIndex(description, reference string, start int) int {
	for start < len(description) {
		match := strings.Index(description[start:], reference)
		if match < 0 {
			return -1
		}
		index := start + match
		after := index + len(reference)
		if after == len(description) || description[after] < '0' || description[after] > '9' {
			return index
		}
		start = after
	}
	return -1
}

func appendNitChecklist(description string, nits []verdict.Finding) string {
	var body strings.Builder
	body.WriteString(strings.TrimSpace(description))
	for _, finding := range nits {
		if body.Len() > 0 {
			body.WriteString("\n\n")
		}
		fmt.Fprintf(&body, "- [ ] %s — %s", finding.Location, finding.Issue)
	}
	return body.String()
}

func appendVerificationSection(description string, verification publicationVerification) string {
	description = strings.TrimSpace(description)
	section := verificationSectionContents(verification)
	if description == "" {
		return "## Verification\n\n" + section
	}
	end, found := verificationSectionEnd(description)
	if !found {
		return description + "\n\n## Verification\n\n" + section
	}
	prefix := strings.TrimSpace(description[:end])
	suffix := strings.TrimSpace(description[end:])
	description = prefix + "\n\n" + section
	if suffix != "" {
		description += "\n\n" + suffix
	}
	return description
}

func verificationSectionContents(verification publicationVerification) string {
	var section strings.Builder
	writeGateVerification(&section, verification.gates)
	writeEvidenceVerification(&section, verification)
	return strings.TrimSpace(section.String())
}

func verificationSectionEnd(description string) (int, bool) {
	lines := strings.SplitAfter(description, "\n")
	offset := 0
	sectionStart := -1
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		level, heading := markdownHeadingLevel(trimmed)
		if isInitialVerificationHeading(level, heading, sectionStart) {
			sectionStart = offset + len(line)
		} else if isVerificationSectionBoundary(level, heading, sectionStart) {
			return offset, true
		}
		offset += len(line)
	}
	if sectionStart < 0 {
		return 0, false
	}
	return len(description), true
}

func isInitialVerificationHeading(level int, heading string, sectionStart int) bool {
	return sectionStart < 0 && level == 2 && heading != "" && strings.EqualFold(heading, "Verification")
}

func isVerificationSectionBoundary(level int, heading string, sectionStart int) bool {
	return sectionStart >= 0 && heading != "" && level <= 2
}

func markdownHeadingLevel(line string) (int, string) {
	trimmed := strings.TrimSpace(line)
	level := 0
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || (len(trimmed) > level && trimmed[level] != ' ') {
		return 0, ""
	}
	heading := strings.TrimSpace(trimmed[level:])
	heading = strings.TrimSpace(strings.TrimRight(heading, "#"))
	return level, heading
}

func writeGateVerification(section *strings.Builder, verification publicationGateVerification) {
	if !verification.ran {
		section.WriteString("Gates: none (no Gate ran).\n\n")
		return
	}
	results := verification.feedback.results
	if !verification.feedback.resultsAvailable || len(results) == 0 {
		section.WriteString("Gate: overall PASS (per-Gate results unavailable).\n\n")
		return
	}
	section.WriteString("Gates:\n")
	for _, result := range results {
		fmt.Fprintf(section, "- %s: %s\n", result.Name, result.Result)
	}
	section.WriteByte('\n')
}

func writeEvidenceVerification(section *strings.Builder, verification publicationVerification) {
	if verification.evidenceUnavailable {
		section.WriteString("Evidence: unavailable (evidence.md could not be read).\n")
		return
	}
	if !verification.evidenceRecorded {
		section.WriteString("Evidence: none (no evidence.md was recorded).\n")
		return
	}
	fmt.Fprintf(section, "Evidence summary:\n\n%s\n", verification.evidence)
}

func readPublicationEvidence(directory string) (publicationEvidence, error) {
	contents, err := os.ReadFile(filepath.Join(directory, "evidence.md"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return publicationEvidence{}, nil
		}
		return publicationEvidence{}, fmt.Errorf("read evidence.md: %w", err)
	}
	images, imageNotices := prWorthyEvidenceImages(directory, string(contents))
	return publicationEvidence{
		summary: summarizeEvidenceText(string(contents)), images: images,
		imageNotices: imageNotices, recorded: true,
	}, nil
}

func prWorthyEvidenceImages(directory, contents string) ([]tracker.PullRequestEvidenceImage, []string) {
	var (
		images  []tracker.PullRequestEvidenceImage
		notices []string
	)
	insideCodeBlock := false
	for _, line := range strings.Split(strings.ReplaceAll(contents, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if isCodeFenceDelimiter(trimmed) {
			insideCodeBlock = !insideCodeBlock
			continue
		}
		if insideCodeBlock {
			continue
		}
		for _, match := range prWorthyImagePattern.FindAllStringSubmatch(line, -1) {
			source := evidenceImageSource(match)
			alt := strings.TrimSpace(match[1])
			if alt == "" {
				alt = filepath.Base(source)
			}
			path, err := localEvidenceImagePath(directory, source)
			if err != nil {
				notices = append(notices, formatPullRequestEvidenceNotice(
					tracker.PullRequestEvidenceImage{Path: source, Alt: alt},
					err,
				))
				continue
			}
			images = append(images, tracker.PullRequestEvidenceImage{Path: path, Alt: alt})
		}
	}
	return images, notices
}

func isCodeFenceDelimiter(line string) bool {
	return strings.HasPrefix(line, "```")
}

func evidenceImageSource(match []string) string {
	if match[2] != "" {
		return match[2]
	}
	return match[3]
}

func localEvidenceImagePath(directory, source string) (string, error) {
	path, err := parseLocalEvidenceImagePath(source)
	if err != nil {
		return "", err
	}
	absoluteEvidenceDirectory, err := filepath.Abs(directory)
	if err != nil {
		return "", fmt.Errorf("resolve Evidence directory: %w", err)
	}
	absoluteImagePath, err := filepath.Abs(filepath.Join(absoluteEvidenceDirectory, path))
	if err != nil {
		return "", fmt.Errorf("resolve PR-worthy Evidence image path %q: %w", source, err)
	}
	insideEvidenceDirectory, err := isWithinEvidenceDirectory(absoluteEvidenceDirectory, absoluteImagePath)
	if err != nil {
		return "", fmt.Errorf("check PR-worthy Evidence image path %q: %w", source, err)
	}
	if !insideEvidenceDirectory {
		return "", fmt.Errorf("PR-worthy Evidence image path %q escapes the Evidence directory", source)
	}
	if err := requireEvidenceImageInDirectory(absoluteEvidenceDirectory, absoluteImagePath, source); err != nil {
		return "", err
	}
	if err := validateEvidenceImage(absoluteImagePath); err != nil {
		return "", err
	}
	return absoluteImagePath, nil
}

func parseLocalEvidenceImagePath(source string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(source))
	if err != nil {
		return "", fmt.Errorf("parse PR-worthy Evidence image path %q: %w", source, err)
	}
	isRemoteReference := parsed.Scheme != "" || parsed.Host != "" || parsed.RawQuery != ""
	if isRemoteReference {
		return "", fmt.Errorf("PR-worthy Evidence image path %q is not a local Evidence file", source)
	}
	if parsed.Fragment != "" {
		return "", fmt.Errorf("PR-worthy Evidence image path %q contains # and cannot be attached safely", source)
	}
	path := filepath.FromSlash(parsed.Path)
	if path == "" {
		return "", errors.New("PR-worthy Evidence image link has an empty path")
	}
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("PR-worthy Evidence image path %q must be relative to the Evidence directory", source)
	}
	return path, nil
}

func requireEvidenceImageInDirectory(evidenceDirectory, imagePath, source string) error {
	resolvedEvidenceDirectory, err := filepath.EvalSymlinks(evidenceDirectory)
	if err != nil {
		return fmt.Errorf("resolve Evidence directory: %w", err)
	}
	resolvedImagePath, err := filepath.EvalSymlinks(imagePath)
	if err != nil {
		return fmt.Errorf("resolve PR-worthy Evidence image %q: %w", source, err)
	}
	insideEvidenceDirectory, err := isWithinEvidenceDirectory(resolvedEvidenceDirectory, resolvedImagePath)
	if err != nil {
		return fmt.Errorf("check resolved PR-worthy Evidence image path %q: %w", source, err)
	}
	if !insideEvidenceDirectory {
		return fmt.Errorf("PR-worthy Evidence image path %q resolves outside the Evidence directory", source)
	}
	return nil
}

func isWithinEvidenceDirectory(directory, path string) (bool, error) {
	relativePath, err := filepath.Rel(directory, path)
	if err != nil {
		return false, err
	}
	pathEscapesDirectory := relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator))
	return !pathEscapesDirectory, nil
}

func validateEvidenceImage(path string) error {
	fileInfo, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect PR-worthy Evidence image %q: %w", path, err)
	}
	if !fileInfo.Mode().IsRegular() {
		return fmt.Errorf("PR-worthy Evidence image %q is not a regular file", path)
	}
	imageFile, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open PR-worthy Evidence image %q: %w", path, err)
	}
	header := make([]byte, 512)
	bytesRead, readErr := imageFile.Read(header)
	closeErr := imageFile.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return fmt.Errorf("read PR-worthy Evidence image %q: %w", path, readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close PR-worthy Evidence image %q: %w", path, closeErr)
	}
	contentType := http.DetectContentType(header[:bytesRead])
	isSVG := strings.EqualFold(filepath.Ext(path), ".svg") && strings.Contains(strings.ToLower(string(header[:bytesRead])), "<svg")
	isImage := strings.HasPrefix(contentType, "image/") || isSVG
	if !isImage {
		return fmt.Errorf("PR-worthy Evidence file %q is not an image (detected %s)", path, contentType)
	}
	return nil
}

func summarizeEvidenceText(contents string) string {
	contents = strings.ReplaceAll(contents, "\r\n", "\n")
	contents = rewriteEvidenceLinks(contents)
	contents = markdownImagePattern.ReplaceAllStringFunc(contents, markdownImageSummary)
	contents = htmlImagePattern.ReplaceAllString(contents, "[Image omitted]")
	exercises := parseEvidenceExercises(contents)
	var summary string
	if len(exercises) == 0 {
		summary = summarizeUnstructuredEvidence(contents)
	} else {
		lines := make([]string, 0, len(exercises))
		for _, exercise := range exercises {
			lines = append(lines, formatEvidenceExercise(exercise))
		}
		summary = strings.Join(lines, "\n")
	}
	return limitEvidenceSummary(summary)
}

type evidenceExercise struct {
	exercised string
	how       string
	observed  string
	outcome   string
}

type evidenceFieldKind uint8

const (
	evidenceFieldUnknown evidenceFieldKind = iota
	evidenceFieldExercised
	evidenceFieldHow
	evidenceFieldObserved
	evidenceFieldOutcome
)

var evidenceFieldNames = map[string]evidenceFieldKind{
	"what was exercised":   evidenceFieldExercised,
	"how it was exercised": evidenceFieldHow,
	"what was observed":    evidenceFieldObserved,
	"outcome":              evidenceFieldOutcome,
}

type evidenceExerciseParser struct {
	exercises             []evidenceExercise
	current               evidenceExercise
	currentField          evidenceFieldKind
	fence                 evidenceFenceState
	awaitingOutcomeReason bool
}

type evidenceTildeFence struct {
	length int
}

type evidenceFenceState struct {
	insideSummaryCode bool
	tildeFence        evidenceTildeFence
}

type evidenceFenceLine struct {
	summaryDelimiter  bool
	hiddenFromSummary bool
	insideTildeFence  bool
}

type evidenceReferenceDefinition struct {
	target   string
	local    bool
	lineFrom int
	lineTo   int
}

type evidenceLinkEdit struct {
	start int
	end   int
	text  string
}

func markdownImageSummary(match string) string {
	parts := markdownImagePattern.FindStringSubmatch(match)
	if len(parts) < 2 || strings.TrimSpace(parts[1]) == "" {
		return "[Image omitted]"
	}
	return "[Image omitted: " + strings.TrimSpace(parts[1]) + "]"
}

func rewriteEvidenceLinks(contents string) string {
	lines := strings.Split(contents, "\n")
	scanner := evidenceLinkScanner{
		definitions: make(map[string]evidenceReferenceDefinition, len(lines)/4),
	}
	lineOffset := 0
	for lineIndex, line := range lines {
		lineEnd := lineOffset + len(line)
		if lineIndex < len(lines)-1 {
			lineEnd++
		}
		scanner.scanLine(line, lineOffset, lineEnd)
		lineOffset = lineEnd
	}
	return applyEvidenceLinkEdits(contents, scanner.definitions, scanner.localDefinitions, scanner.links)
}

type evidenceLinkScanner struct {
	definitions      map[string]evidenceReferenceDefinition
	localDefinitions []evidenceReferenceDefinition
	links            []evidenceMarkdownLink
	fence            evidenceFenceState
	codeSpanLength   int
}

func (scanner *evidenceLinkScanner) scanLine(line string, lineStart, lineEnd int) {
	fenceLine := scanner.fence.consumeLine(line)
	if fenceLine.hiddenFromSummary {
		scanner.codeSpanLength = 0
		return
	}
	if fenceLine.insideTildeFence {
		scanner.links = append(scanner.links, scanEvidenceMarkdownLinks(line, lineStart, &scanner.codeSpanLength)...)
		scanner.codeSpanLength = 0
		return
	}
	if scanner.scanReferenceDefinition(line, lineStart, lineEnd) {
		return
	}
	scanner.links = append(scanner.links, scanEvidenceMarkdownLinks(line, lineStart, &scanner.codeSpanLength)...)
}

func (scanner *evidenceLinkScanner) scanReferenceDefinition(line string, lineStart, lineEnd int) bool {
	if scanner.codeSpanLength > 0 {
		return false
	}
	key, target, isDefinition := parseEvidenceReferenceDefinition(line)
	if !isDefinition {
		return false
	}
	recordEvidenceReferenceDefinition(scanner.definitions, &scanner.localDefinitions, key, target, lineStart, lineEnd)
	return true
}

func recordEvidenceReferenceDefinition(
	definitions map[string]evidenceReferenceDefinition,
	localDefinitions *[]evidenceReferenceDefinition,
	key, target string,
	lineFrom, lineTo int,
) {
	definition := evidenceReferenceDefinition{
		target: target, local: !hasHTTPOrHTTPSScheme(target),
		lineFrom: lineFrom, lineTo: lineTo,
	}
	if definition.local {
		*localDefinitions = append(*localDefinitions, definition)
	}
	if _, exists := definitions[key]; !exists {
		definitions[key] = definition
	}
}

func parseEvidenceReferenceDefinition(line string) (string, string, bool) {
	indent := len(line) - len(strings.TrimLeft(line, " "))
	if indent > 3 {
		return "", "", false
	}
	line = line[indent:]
	labelEnd, label, ok := evidenceBracketLabel(line, 0)
	if !ok || labelEnd >= len(line) || line[labelEnd] != ':' {
		return "", "", false
	}
	destination := strings.TrimSpace(line[labelEnd+1:])
	target, consumed, ok := parseEvidenceLinkDestination(destination)
	if !ok || label == "" || strings.TrimSpace(target) == "" {
		return "", "", false
	}
	title := strings.TrimSpace(destination[consumed:])
	if title != "" && !isEvidenceReferenceTitle(title) {
		return "", "", false
	}
	return normalizeEvidenceReferenceLabel(label), target, true
}

func isEvidenceReferenceTitle(title string) bool {
	return (strings.HasPrefix(title, "\"") && strings.HasSuffix(title, "\"")) ||
		(strings.HasPrefix(title, "'") && strings.HasSuffix(title, "'")) ||
		(strings.HasPrefix(title, "(") && strings.HasSuffix(title, ")"))
}

func scanEvidenceMarkdownLinks(line string, lineOffset int, codeSpanLength *int) []evidenceMarkdownLink {
	var links []evidenceMarkdownLink
	for position := 0; position < len(line); {
		if next, consumed := skipEvidenceCodeOrEscape(line, position, codeSpanLength); consumed {
			position = next
			continue
		}
		link, ok := parseEvidenceMarkdownLink(line, position)
		if !ok {
			position++
			continue
		}
		if isMarkdownImageLink(line, position) {
			position = link.end
			continue
		}
		linkEnd := link.end
		link.start = lineOffset + position
		link.end += lineOffset
		links = append(links, link)
		position = linkEnd
	}
	return links
}

func skipEvidenceCodeOrEscape(line string, position int, codeSpanLength *int) (int, bool) {
	if *codeSpanLength > 0 {
		end, ok := evidenceCodeSpanClosingEnd(line, position, *codeSpanLength)
		if !ok {
			return len(line), true
		}
		*codeSpanLength = 0
		return end, true
	}
	if line[position] == '\\' && position+1 < len(line) {
		return position + 2, true
	}
	if line[position] != '`' {
		return position, false
	}
	length := evidenceBacktickRun(line, position)
	if end, ok := evidenceCodeSpanClosingEnd(line, position+length, length); ok {
		return end, true
	}
	*codeSpanLength = length
	return len(line), true
}

func isMarkdownImageLink(line string, position int) bool {
	return position > 0 && line[position] == '[' && line[position-1] == '!'
}

func applyEvidenceLinkEdits(
	contents string,
	definitions map[string]evidenceReferenceDefinition,
	localDefinitions []evidenceReferenceDefinition,
	links []evidenceMarkdownLink,
) string {
	var edits []evidenceLinkEdit
	for _, link := range links {
		if link.isReference {
			definition, exists := definitions[link.reference]
			if !exists || !definition.local {
				continue
			}
			edits = append(edits, evidenceLinkEdit{
				start: link.start, end: link.end, text: evidenceLinkLabel(link.label, definition.target),
			})
			continue
		}
		if hasHTTPOrHTTPSScheme(link.target) {
			continue
		}
		edits = append(edits, evidenceLinkEdit{
			start: link.start, end: link.end, text: evidenceLinkLabel(link.label, link.target),
		})
	}
	for _, definition := range localDefinitions {
		edits = append(edits, evidenceLinkEdit{start: definition.lineFrom, end: definition.lineTo})
	}
	return applyEvidenceEdits(contents, edits)
}

func applyEvidenceEdits(contents string, edits []evidenceLinkEdit) string {
	sort.Slice(edits, func(left, right int) bool {
		return edits[left].start < edits[right].start
	})
	var result strings.Builder
	position := 0
	for _, edit := range edits {
		if edit.start < position {
			continue
		}
		result.WriteString(contents[position:edit.start])
		result.WriteString(edit.text)
		position = edit.end
	}
	result.WriteString(contents[position:])
	return result.String()
}

type evidenceMarkdownLink struct {
	label       string
	target      string
	reference   string
	start       int
	end         int
	isReference bool
}

func parseEvidenceMarkdownLink(line string, start int) (evidenceMarkdownLink, bool) {
	labelEnd, label, ok := evidenceBracketLabel(line, start)
	if !ok {
		return evidenceMarkdownLink{}, false
	}
	link := evidenceMarkdownLink{label: label, start: start}
	if labelEnd >= len(line) {
		return evidenceMarkdownLink{}, false
	}
	switch line[labelEnd] {
	case '(':
		target, consumed, ok := parseEvidenceLinkDestination(line[labelEnd+1:])
		if !ok {
			return evidenceMarkdownLink{}, false
		}
		closing, ok := evidenceInlineLinkEnd(line, labelEnd+1+consumed)
		if !ok {
			return evidenceMarkdownLink{}, false
		}
		link.target = target
		link.end = closing
	case '[':
		referenceEnd, reference, ok := evidenceBracketLabel(line, labelEnd)
		if !ok {
			return evidenceMarkdownLink{}, false
		}
		if reference == "" {
			reference = label
		}
		link.reference = normalizeEvidenceReferenceLabel(reference)
		link.end = referenceEnd
		link.isReference = true
	default:
		return evidenceMarkdownLink{}, false
	}
	return link, true
}

func evidenceBracketLabel(line string, start int) (int, string, bool) {
	if start >= len(line) || line[start] != '[' {
		return 0, "", false
	}
	depth := 1
	for position := start + 1; position < len(line); position++ {
		if line[position] == '\\' {
			position++
			continue
		}
		switch line[position] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return position + 1, line[start+1 : position], true
			}
		}
	}
	return 0, "", false
}

func parseEvidenceLinkDestination(text string) (string, int, bool) {
	leadingSpaces := len(text) - len(strings.TrimLeft(text, " \t"))
	text = text[leadingSpaces:]
	if len(text) > 0 && text[0] == '<' {
		return parseAngleEvidenceDestination(text, leadingSpaces)
	}
	return parseBareEvidenceDestination(text, leadingSpaces)
}

func parseAngleEvidenceDestination(text string, leadingSpaces int) (string, int, bool) {
	for position := 1; position < len(text); position++ {
		if text[position] == '\\' {
			position++
			continue
		}
		if text[position] == '>' {
			return text[1:position], leadingSpaces + position + 1, true
		}
	}
	return "", 0, false
}

func parseBareEvidenceDestination(text string, leadingSpaces int) (string, int, bool) {
	depth := 0
	for position := 0; position < len(text); position++ {
		if text[position] == '\\' {
			position++
			continue
		}
		switch text[position] {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				return text[:position], leadingSpaces + position, true
			}
			depth--
		case ' ', '\t':
			if depth == 0 {
				return text[:position], leadingSpaces + position, true
			}
		}
	}
	return text, leadingSpaces + len(text), true
}

func evidenceInlineLinkEnd(line string, start int) (int, bool) {
	var quote byte
	depth := 0
	for position := start; position < len(line); position++ {
		if line[position] == '\\' {
			position++
			continue
		}
		if quote != 0 {
			if line[position] == quote {
				quote = 0
			}
			continue
		}
		switch line[position] {
		case '"', '\'':
			quote = line[position]
		case '(':
			depth++
		case ')':
			if depth == 0 {
				return position + 1, true
			}
			depth--
		}
	}
	return 0, false
}

func evidenceBacktickRun(line string, start int) int {
	length := 1
	for start+length < len(line) && line[start+length] == '`' {
		length++
	}
	return length
}

func evidenceCodeSpanClosingEnd(line string, start, length int) (int, bool) {
	for position := start; position < len(line); position++ {
		if line[position] != '`' {
			continue
		}
		runLength := evidenceBacktickRun(line, position)
		if runLength == length {
			end := position + runLength
			return end, true
		}
		position += runLength - 1
	}
	return 0, false
}

func evidenceLinkLabel(label, target string) string {
	if strings.TrimSpace(label) != "" {
		return label
	}
	if strings.HasPrefix(target, "#") {
		return strings.TrimPrefix(target, "#")
	}
	parsed, err := url.Parse(target)
	if err == nil && parsed.Path != "" {
		target = parsed.Path
	}
	if target == "" {
		return ""
	}
	return path.Base(target)
}

func normalizeEvidenceReferenceLabel(label string) string {
	return strings.ToLower(strings.Join(strings.Fields(label), " "))
}

func hasHTTPOrHTTPSScheme(target string) bool {
	target = strings.ToLower(target)
	return strings.HasPrefix(target, "http:") || strings.HasPrefix(target, "https:")
}

func (fence *evidenceFenceState) consumeLine(line string) evidenceFenceLine {
	if isCodeFenceDelimiter(strings.TrimSpace(line)) {
		fence.insideSummaryCode = !fence.insideSummaryCode
		return evidenceFenceLine{summaryDelimiter: true, hiddenFromSummary: true}
	}
	if fence.insideSummaryCode {
		return evidenceFenceLine{hiddenFromSummary: true}
	}
	if !fence.tildeFence.consumeLine(line) {
		return evidenceFenceLine{}
	}
	return evidenceFenceLine{insideTildeFence: true}
}

func (fence *evidenceTildeFence) consumeLine(line string) bool {
	if fence.length > 0 {
		if fence.isClosingLine(line) {
			fence.length = 0
		}
		return true
	}
	delimiter, length, _, ok := markdownFenceMarker(line)
	if !ok || delimiter != '~' {
		return false
	}
	fence.length = length
	return true
}

func (fence evidenceTildeFence) isClosingLine(line string) bool {
	delimiter, length, remainder, ok := markdownFenceMarker(line)
	return ok && delimiter == '~' && length >= fence.length && strings.TrimSpace(remainder) == ""
}

func markdownFenceMarker(line string) (byte, int, string, bool) {
	indent := len(line) - len(strings.TrimLeft(line, " "))
	if indent > 3 || indent == len(line) {
		return 0, 0, "", false
	}
	delimiter := line[indent]
	if delimiter != '`' && delimiter != '~' {
		return 0, 0, "", false
	}
	length := 1
	for indent+length < len(line) && line[indent+length] == delimiter {
		length++
	}
	if length < 3 {
		return 0, 0, "", false
	}
	return delimiter, length, line[indent+length:], true
}

func parseEvidenceExercises(contents string) []evidenceExercise {
	parser := evidenceExerciseParser{}
	for _, line := range strings.Split(contents, "\n") {
		parser.addLine(line)
	}
	parser.flush()
	return parser.exercises
}

func (parser *evidenceExerciseParser) addLine(line string) {
	trimmed := strings.TrimSpace(line)
	fenceLine := parser.fence.consumeLine(line)
	if fenceLine.summaryDelimiter {
		parser.currentField = evidenceFieldUnknown
		parser.awaitingOutcomeReason = false
		return
	}
	if fenceLine.hiddenFromSummary {
		return
	}
	if parser.setFieldFromLine(trimmed) {
		return
	}
	if parser.consumeBlankOrHeading(trimmed) {
		return
	}
	if parser.appendOutcomeReason(trimmed) {
		return
	}
	appendEvidenceContinuation(&parser.current, parser.currentField, trimmed)
}

func (parser *evidenceExerciseParser) setFieldFromLine(line string) bool {
	field, value, found := evidenceField(line)
	if !found {
		return false
	}
	if field == evidenceFieldExercised && parser.current.exercised != "" {
		parser.flush()
	}
	setEvidenceField(&parser.current, field, value)
	parser.currentField = field
	parser.awaitingOutcomeReason = field == evidenceFieldOutcome && isNotExercisedOutcome(value)
	return true
}

func (parser *evidenceExerciseParser) consumeBlankOrHeading(line string) bool {
	if line == "" {
		return true
	}
	if !strings.HasPrefix(line, "#") {
		return false
	}
	if isEvidenceExerciseHeading(line) {
		parser.flush()
	}
	parser.currentField = evidenceFieldUnknown
	parser.awaitingOutcomeReason = false
	return true
}

func (parser *evidenceExerciseParser) appendOutcomeReason(line string) bool {
	if !parser.awaitingOutcomeReason {
		return false
	}
	value := strings.TrimSpace(strings.TrimLeft(line, "-* "))
	value = strings.TrimPrefix(strings.TrimPrefix(value, "Reason:"), "reason:")
	appendEvidenceContinuation(&parser.current, evidenceFieldOutcome, strings.TrimSpace(value))
	parser.awaitingOutcomeReason = false
	return true
}

func isEvidenceExerciseHeading(line string) bool {
	level, heading := markdownHeadingLevel(line)
	if level < 2 || heading == "" {
		return false
	}
	name, _, _ := strings.Cut(heading, ":")
	return strings.EqualFold(strings.TrimSpace(name), "Exercise")
}

func (parser *evidenceExerciseParser) flush() {
	if parser.current.hasDetails() {
		parser.exercises = append(parser.exercises, parser.current)
	}
	parser.current = evidenceExercise{}
	parser.currentField = evidenceFieldUnknown
	parser.awaitingOutcomeReason = false
}

func (exercise evidenceExercise) hasDetails() bool {
	return exercise.exercised != "" || exercise.how != "" || exercise.observed != "" || exercise.outcome != ""
}

func evidenceField(line string) (evidenceFieldKind, string, bool) {
	line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-* "))
	key, value, found := strings.Cut(line, ":")
	if !found {
		return evidenceFieldUnknown, "", false
	}
	key = strings.Trim(key, "*_ `")
	value = strings.Trim(strings.TrimSpace(value), "*_ `")
	field, found := evidenceFieldNames[strings.ToLower(key)]
	if !found {
		return evidenceFieldUnknown, "", false
	}
	return field, value, true
}

func setEvidenceField(exercise *evidenceExercise, field evidenceFieldKind, value string) {
	if target := exercise.fieldValue(field); target != nil {
		*target = value
	}
}

func appendEvidenceContinuation(exercise *evidenceExercise, field evidenceFieldKind, value string) {
	if target := exercise.fieldValue(field); target != nil {
		*target = joinEvidenceText(*target, value)
	}
}

func (exercise *evidenceExercise) fieldValue(field evidenceFieldKind) *string {
	switch field {
	case evidenceFieldExercised:
		return &exercise.exercised
	case evidenceFieldHow:
		return &exercise.how
	case evidenceFieldObserved:
		return &exercise.observed
	case evidenceFieldOutcome:
		return &exercise.outcome
	default:
		return nil
	}
}

func isNotExercisedOutcome(value string) bool {
	return strings.EqualFold(strings.Trim(strings.TrimSpace(value), ".!"), "not exercised")
}

func joinEvidenceText(existing, next string) string {
	if existing == "" {
		return next
	}
	return existing + " " + next
}

func formatEvidenceExercise(exercise evidenceExercise) string {
	fields := []struct {
		label string
		value string
	}{
		{label: "Outcome", value: exercise.outcome},
		{label: "Exercised", value: exercise.exercised},
		{label: "How", value: exercise.how},
		{label: "Observed", value: exercise.observed},
	}
	lines := make([]string, 0, len(fields))
	for _, field := range fields {
		if field.value == "" {
			continue
		}
		prefix := "  - "
		if len(lines) == 0 {
			prefix = "- "
		}
		lines = append(lines, prefix+field.label+": "+field.value)
	}
	return strings.Join(lines, "\n")
}

func summarizeUnstructuredEvidence(contents string) string {
	var lines []string
	var fence evidenceFenceState
	for _, line := range strings.Split(contents, "\n") {
		trimmed := strings.TrimSpace(line)
		if fence.consumeLine(line).hiddenFromSummary {
			continue
		}
		if !isUnstructuredEvidenceContentLine(trimmed) {
			continue
		}
		lines = append(lines, trimmed)
	}
	if len(lines) == 0 {
		return "evidence.md is empty."
	}
	return limitEvidenceSummary(strings.Join(lines, " "))
}

func isUnstructuredEvidenceContentLine(trimmed string) bool {
	return trimmed != "" && !strings.HasPrefix(trimmed, "#")
}

const maxPublicationEvidenceSummaryRunes = 2000

func limitEvidenceSummary(summary string) string {
	runes := []rune(summary)
	if len(runes) <= maxPublicationEvidenceSummaryRunes {
		return summary
	}
	return string(runes[:maxPublicationEvidenceSummaryRunes-1]) + "…"
}

func failedPublication(step runrecord.PublicationStep, err error) runrecord.Publication {
	return runrecord.Publication{Status: runrecord.PublicationFailed, Step: step, Error: err.Error()}
}

func implementationCommitMessage(options ImplementOptions, branch string) (string, error) {
	branchType, _, ok := strings.Cut(branch, "/")
	if !ok || branchType == "" {
		return "", fmt.Errorf("create implementation commit: branch %q has no conventional type", branch)
	}
	return fmt.Sprintf("%s: %s (#%d)", branchType, options.Ticket.Title, options.Ticket.Number), nil
}

func publicationCommands(
	step runrecord.PublicationStep,
	options ImplementOptions,
	setup implementSetup,
	summary implementSummary,
) []string {
	commands := []string{"git push -u origin " + shellArgument(setup.branch), pullRequestCommand(options, setup, summary)}
	if step == runrecord.PublicationStepCommit {
		message, err := implementationCommitMessage(options, setup.branch)
		if err != nil {
			message = fmt.Sprintf("implement: %s (#%d)", options.Ticket.Title, options.Ticket.Number)
		}
		commands = append([]string{"git add -A", "git commit -m " + shellQuote(message)}, commands...)
	}
	if step == runrecord.PublicationStepPullRequest {
		return commands[len(commands)-1:]
	}
	if step == runrecord.PublicationStepPush {
		return commands
	}
	if step == runrecord.PublicationStepCommit {
		return commands
	}
	return nil
}

func pullRequestCommand(options ImplementOptions, setup implementSetup, summary implementSummary) string {
	publisher, ok := options.IssueTracker.(tracker.PullRequestCreator)
	if !ok {
		return ""
	}
	request := tracker.PullRequest{
		Title: options.Ticket.Title, Body: pullRequestBody(options.Ticket.Number, summary),
		Head: setup.branch, Base: setup.baseBranch,
	}
	return shellCommand(publisher.CreatePullRequestCommand(request))
}

func shellCommand(arguments []string) string {
	quotedArguments := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		quotedArguments = append(quotedArguments, shellArgument(argument))
	}
	return strings.Join(quotedArguments, " ")
}

func formatPublicationFailure(publication runrecord.Publication, commands []string) string {
	var report strings.Builder
	fmt.Fprintf(&report, "publication failed during %s: %s\nManual commands to finish publication:\n", publication.Step, publication.Error)
	for _, command := range commands {
		fmt.Fprintf(&report, "  %s\n", command)
	}
	return strings.TrimRight(report.String(), "\n")
}

func shellArgument(value string) string {
	if value != "" && strings.Trim(value, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._/-") == "" {
		return value
	}
	return shellQuote(value)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func pullRequestBody(ticketNumber int, summary implementSummary) string {
	body := fmt.Sprintf("Closes #%d\n\n%s", ticketNumber, summary.final.Summary)
	body = ensureClosingReference(body, ticketNumber)
	body = appendVerificationSection(body, summary.verification)
	return appendNitChecklist(body, summary.nits)
}
