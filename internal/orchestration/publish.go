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
	"path/filepath"
	"regexp"
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
) (runrecord.Publication, []string, []string) {
	var notices []string
	attachmentNotices := append([]string(nil), summary.verification.evidenceImageNotices...)
	notices = append(notices, attachmentNotices...)
	hasChanges, err := stageApprovedImplementation(ctx, setup)
	if err != nil {
		return failedPublication(runrecord.PublicationStepCommit, err), notices, attachmentNotices
	}
	if !hasChanges {
		return runrecord.Publication{Status: runrecord.PublicationSkipped}, notices, attachmentNotices
	}
	message, err := implementationCommitMessage(options, setup.branch)
	if err != nil {
		return failedPublication(runrecord.PublicationStepCommit, err), notices, attachmentNotices
	}
	notice, err := commitApprovedImplementation(ctx, options, setup, message)
	if notice != "" {
		notices = append(notices, notice)
	}
	if err != nil {
		return failedPublication(runrecord.PublicationStepCommit, fmt.Errorf("commit approved implementation: %w", err)), notices, attachmentNotices
	}
	if _, err := setup.git.Run(ctx, "push", "-u", "origin", setup.branch); err != nil {
		return failedPublication(runrecord.PublicationStepPush, fmt.Errorf("push implementation branch %q: %w", setup.branch, err)), notices, attachmentNotices
	}
	pullRequest, notice := commitgenPullRequest(ctx, options, setup, summary)
	if notice != "" {
		notices = append(notices, notice)
	}
	publication, notices := createDraftPullRequest(ctx, options, pullRequest, notices)
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
	notices []string,
) (runrecord.Publication, []string) {
	publisher, ok := options.IssueTracker.(tracker.PullRequestCreator)
	if !ok {
		return failedPublication(runrecord.PublicationStepPullRequest, fmt.Errorf("create pull request: Tracker cannot create pull requests")), notices
	}
	pullRequestURL, err := publisher.CreatePullRequest(ctx, request)
	if err != nil {
		return failedPublication(runrecord.PublicationStepPullRequest, fmt.Errorf("create draft pull request: %w", err)), notices
	}
	pullRequestURL = strings.TrimSpace(pullRequestURL)
	if pullRequestURL == "" {
		return failedPublication(runrecord.PublicationStepPullRequest, fmt.Errorf("create draft pull request: tracker returned an empty URL")), notices
	}
	return runrecord.Publication{Status: runrecord.PublicationPublished, URL: pullRequestURL}, notices
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
	contents = markdownImagePattern.ReplaceAllStringFunc(contents, markdownImageSummary)
	contents = htmlImagePattern.ReplaceAllString(contents, "[Image omitted]")
	exercises := parseEvidenceExercises(contents)
	if len(exercises) == 0 {
		return summarizeUnstructuredEvidence(contents)
	}
	lines := make([]string, 0, len(exercises))
	for _, exercise := range exercises {
		lines = append(lines, formatEvidenceExercise(exercise))
	}
	return limitEvidenceSummary(strings.Join(lines, "\n"))
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
	insideCodeBlock       bool
	awaitingOutcomeReason bool
}

func markdownImageSummary(match string) string {
	parts := markdownImagePattern.FindStringSubmatch(match)
	if len(parts) < 2 || strings.TrimSpace(parts[1]) == "" {
		return "[Image omitted]"
	}
	return "[Image omitted: " + strings.TrimSpace(parts[1]) + "]"
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
	if parser.consumeCodeFence(trimmed) {
		return
	}
	if parser.insideCodeBlock {
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

func (parser *evidenceExerciseParser) consumeCodeFence(line string) bool {
	if !isCodeFenceDelimiter(line) {
		return false
	}
	parser.insideCodeBlock = !parser.insideCodeBlock
	parser.currentField = evidenceFieldUnknown
	parser.awaitingOutcomeReason = false
	return true
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
	parts := make([]string, 0, 4)
	if exercise.outcome != "" {
		parts = append(parts, "Outcome: "+exercise.outcome)
	}
	if exercise.exercised != "" {
		parts = append(parts, "Exercised: "+exercise.exercised)
	}
	if exercise.how != "" {
		parts = append(parts, "How: "+exercise.how)
	}
	if exercise.observed != "" {
		parts = append(parts, "Observed: "+exercise.observed)
	}
	return "- " + strings.Join(parts, "; ")
}

func summarizeUnstructuredEvidence(contents string) string {
	var lines []string
	insideCodeBlock := false
	for _, line := range strings.Split(contents, "\n") {
		trimmed := strings.TrimSpace(line)
		if isCodeFenceDelimiter(trimmed) {
			insideCodeBlock = !insideCodeBlock
			continue
		}
		if !isUnstructuredEvidenceContentLine(trimmed, insideCodeBlock) {
			continue
		}
		lines = append(lines, trimmed)
	}
	if len(lines) == 0 {
		return "evidence.md is empty."
	}
	return limitEvidenceSummary(strings.Join(lines, " "))
}

func isCodeFenceDelimiter(trimmed string) bool {
	return strings.HasPrefix(trimmed, "```")
}

func isUnstructuredEvidenceContentLine(trimmed string, insideCodeBlock bool) bool {
	return !insideCodeBlock && trimmed != "" && !strings.HasPrefix(trimmed, "#")
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
