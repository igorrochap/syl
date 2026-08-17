package orchestration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/mattn/go-isatty"

	"github.com/igorrochap/rig/internal/config"
	"github.com/igorrochap/rig/internal/harness"
	"github.com/igorrochap/rig/internal/tracker"
	"github.com/igorrochap/rig/internal/verdict"
)

const implementPrompt = `/implement

Implement the ticket below in the current project. Use the vendored implement skill and leave the working tree with the requested changes for review. Do not commit or push changes.

` + questionProtocolInstruction + `

Ticket: %s
Title: %s

Ticket body (including acceptance criteria):
%s`

const reviseImplementPrompt = `/fix-review

Address ONLY the reviewer's [blocking] findings listed below. Use the vendored fix-review skill. Leave the working tree with the changes for review and do not commit or push changes.

` + questionProtocolInstruction + `

Ticket: %s

Blocking findings:
%s`

var branchTypePattern = regexp.MustCompile(`^(feat|fix|refactor|chore|docs|test|perf|build|ci)$`)

type implementSetup struct {
	git         GitRunner
	branch      string
	branchPoint string
}

type runArtifacts struct {
	dir      string
	sessions []string
}

func newRunArtifacts(projectRoot string, issueNumber int, branch, branchPoint string) (runArtifacts, error) {
	dir := filepath.Join(projectRoot, ".rig", "runs", time.Now().UTC().Format("20060102T150405.000000000Z")+"-"+strconv.Itoa(issueNumber))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return runArtifacts{}, fmt.Errorf("create implement run artifacts: %w", err)
	}
	metadata := fmt.Sprintf("Branch: %s\nBranch point: %s\n", branch, branchPoint)
	if err := writeArtifact(filepath.Join(dir, "metadata.txt"), metadata); err != nil {
		return runArtifacts{}, err
	}
	return runArtifacts{dir: dir}, nil
}

type ImplementOptions struct {
	ProjectRoot   string
	ProjectConfig config.Config
	IssueTracker  tracker.Tracker
	Ticket        tracker.Ticket
	Implementer   harness.Adapter
	Reviewer      harness.Adapter
	Git           GitRunner
	Notifier      Notifier
	Input         io.Reader
	Output        io.Writer
}

func RunImplement(ctx context.Context, options ImplementOptions) error {
	projectConfig := options.ProjectConfig
	issueTracker := options.IssueTracker
	ticket := options.Ticket
	if options.Git == nil {
		return errors.New("implement: git runner is not configured")
	}
	if options.Implementer == nil {
		return fmt.Errorf("implement harness %q is not configured", projectConfig.Roles.Implement.Harness)
	}
	if options.Reviewer == nil {
		return fmt.Errorf("review harness %q is not configured", projectConfig.Roles.Review.Harness)
	}
	setup, err := prepareImplement(ctx, options.Git, issueTracker, ticket)
	if err != nil {
		return err
	}
	notifier := options.Notifier
	if !projectConfig.Notifications.Enabled {
		notifier = nil
	}
	questions := NewQuestionHandler(options.Input, options.Output, "#"+strconv.Itoa(ticket.Number), notifier)
	artifacts, err := newRunArtifacts(options.ProjectRoot, ticket.Number, setup.branch, setup.branchPoint)
	if err != nil {
		return err
	}
	iterations, final, nits, err := runImplementIterations(ctx, implementIterationsParams{
		git:           setup.git,
		implementer:   options.Implementer,
		reviewer:      options.Reviewer,
		projectConfig: projectConfig,
		ticket:        ticket,
		branchPoint:   setup.branchPoint,
		artifacts:     &artifacts,
		questions:     questions,
		output:        options.Output,
	})
	if err != nil {
		return err
	}

	diffStat, err := setup.git.Run(ctx, "diff", "--stat", setup.branchPoint)
	if err != nil {
		diffStat = fmt.Sprintf("unavailable: %v", err)
	}
	if err := artifacts.writeSummary(iterations, final, nits, diffStat); err != nil {
		return err
	}
	summary := formatImplementSummary(iterations, final, nits, diffStat)
	if _, err := io.WriteString(options.Output, summary); err != nil {
		return fmt.Errorf("write implement summary: %w", err)
	}
	if err := artifacts.writeSessions(); err != nil {
		return err
	}
	if notifier != nil {
		_ = notifier.Notify(ctx, fmt.Sprintf("implement #%d finished: %s", ticket.Number, final.Status))
	}
	if final.Status == verdict.Revise {
		return fmt.Errorf("implement loop reached max iterations (%d) with revise verdict", projectConfig.Loop.MaxIterations)
	}
	return nil
}

func prepareImplement(ctx context.Context, git GitRunner, issueTracker tracker.Tracker, ticket tracker.Ticket) (implementSetup, error) {
	status, err := git.Run(ctx, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return implementSetup{}, fmt.Errorf("check working tree: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		return implementSetup{}, errors.New("working tree is dirty; commit or stash changes first")
	}

	branchPoint, err := git.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return implementSetup{}, fmt.Errorf("record branch point: %w", err)
	}
	branchPoint = strings.TrimSpace(branchPoint)
	if branchPoint == "" {
		return implementSetup{}, errors.New("record branch point: git returned an empty ref")
	}
	branch := branchName(ticket)
	if _, err := git.Run(ctx, "switch", "-c", branch); err != nil {
		return implementSetup{}, fmt.Errorf("create implementation branch %q: %w", branch, err)
	}
	if err := issueTracker.UpdateStatus(ctx, ticket.Number, "doing"); err != nil {
		return implementSetup{}, fmt.Errorf("mark ticket #%d as doing: %w", ticket.Number, err)
	}
	return implementSetup{git: git, branch: branch, branchPoint: branchPoint}, nil
}

type implementIterationsParams struct {
	git           GitRunner
	implementer   harness.Adapter
	reviewer      harness.Adapter
	projectConfig config.Config
	ticket        tracker.Ticket
	branchPoint   string
	artifacts     *runArtifacts
	questions     *QuestionHandler
	output        io.Writer
}

func runImplementIterations(ctx context.Context, params implementIterationsParams) (int, verdict.Verdict, []verdict.Finding, error) {
	var blocking []verdict.Finding
	var nits []verdict.Finding
	var final verdict.Verdict
	iterations := 0
	for iteration := 1; iteration <= params.projectConfig.Loop.MaxIterations; iteration++ {
		iterations = iteration
		implementRequest := harness.Request{
			Model:  params.projectConfig.Roles.Implement.Model,
			Effort: params.projectConfig.Roles.Implement.Effort,
			Prompt: composeImplementPrompt(params.ticket, blocking, iteration),
		}
		implementResult, err := runImplementRole(ctx, params.implementer, implementRequest, newRoleLabelWriter(params.output, "implement", ansiColorImplement), params.artifacts.dir, iteration, params.questions)
		if err != nil {
			return 0, verdict.Verdict{}, nil, err
		}
		params.artifacts.recordSessions(iteration, "implement", implementResult.SessionIDs)
		if err := ensureHeadUnchanged(ctx, params.git, params.branchPoint); err != nil {
			return 0, verdict.Verdict{}, nil, err
		}

		reviewRequest := harness.Request{
			Model:  params.projectConfig.Roles.Review.Model,
			Effort: params.projectConfig.Roles.Review.Effort,
			Prompt: composeReviewPromptAgainstRef("#"+strconv.Itoa(params.ticket.Number), params.ticket, params.branchPoint),
		}
		reviewResult, err := RunReviewExecutionWithProgress(ctx, params.reviewer, reviewRequest, newRoleLabelWriter(params.output, "review", ansiColorReview), ParsedHarnessOutput, params.questions)
		if err != nil {
			return 0, verdict.Verdict{}, nil, err
		}
		if err := params.artifacts.recordReview(iteration, reviewResult); err != nil {
			return 0, verdict.Verdict{}, nil, err
		}
		if err := ensureHeadUnchanged(ctx, params.git, params.branchPoint); err != nil {
			return 0, verdict.Verdict{}, nil, err
		}
		params.artifacts.recordSessions(iteration, "review", reviewResult.SessionIDs)
		final = reviewResult.Verdict
		nits = append(nits, nitFindings(final)...)
		if final.Status == verdict.Approve {
			break
		}
		blocking = blockingFindings(final)
	}
	return iterations, final, nits, nil
}

func composeImplementPrompt(ticket tracker.Ticket, blocking []verdict.Finding, iteration int) string {
	if iteration == 1 {
		return fmt.Sprintf(implementPrompt, "#"+strconv.Itoa(ticket.Number), ticket.Title, ticket.Body)
	}
	return fmt.Sprintf(reviseImplementPrompt, "#"+strconv.Itoa(ticket.Number), formatBlockingFindings(blocking))
}

func runImplementRole(ctx context.Context, adapter harness.Adapter, request harness.Request, output io.Writer, artifactDir string, iteration int, questions *QuestionHandler) (harnessTranscript, error) {
	var feed bytes.Buffer
	result, err := runHarnessConversation(ctx, adapter, func(runContext context.Context) (harness.Stream, error) {
		return adapter.Run(runContext, request)
	}, conversationOptions{
		output:    io.MultiWriter(output, &feed),
		mode:      ParsedHarnessOutput,
		questions: questions,
	})
	if err != nil {
		return harnessTranscript{}, fmt.Errorf("run implement harness: %w", err)
	}
	if err := writeArtifact(filepath.Join(artifactDir, fmt.Sprintf("iteration-%02d-implement.feed", iteration)), feed.String()); err != nil {
		return harnessTranscript{}, err
	}
	if err := writeArtifact(filepath.Join(artifactDir, fmt.Sprintf("iteration-%02d-implement.transcript", iteration)), result.Transcript); err != nil {
		return harnessTranscript{}, err
	}
	return result, nil
}

type streamMarkerParser struct {
	buffer string
	marker string
	hidden bool
}

// streamMarkerParser follows questionParser's chunk-buffering pattern. The
// review contract puts the structured verdict at the end of each turn, so the
// live feed can treat everything from the canonical marker onward as opaque;
// verdict.Parse remains responsible for validating its shape.
func newStreamMarkerParser(marker string) *streamMarkerParser {
	return &streamMarkerParser{marker: marker}
}

func (p *streamMarkerParser) Feed(chunk string) string {
	if p.hidden {
		return ""
	}
	p.buffer += chunk
	start := lineMarkerStart(p.buffer, p.marker)
	if start == -1 {
		safeLength := len(p.buffer) - lineMarkerPrefixLength(p.buffer, p.marker)
		visible := p.buffer[:safeLength]
		p.buffer = p.buffer[safeLength:]
		return visible
	}

	p.hidden = true
	visible := p.buffer[:start]
	p.buffer = ""
	return visible
}

func (p *streamMarkerParser) Flush() string {
	if p.hidden {
		return ""
	}
	visible := p.buffer
	p.buffer = ""
	return visible
}

func (p *streamMarkerParser) Reset() {
	p.buffer = ""
	p.hidden = false
}

func lineMarkerStart(value, marker string) int {
	searchFrom := 0
	for searchFrom < len(value) {
		relative := strings.Index(value[searchFrom:], marker)
		if relative == -1 {
			return -1
		}
		start := searchFrom + relative
		if start == 0 || value[start-1] == '\n' {
			return start
		}
		searchFrom = start + 1
	}
	return -1
}

func lineMarkerPrefixLength(value, marker string) int {
	length := suffixPrefixLength(value, marker)
	if length == 0 {
		return 0
	}
	start := len(value) - length
	if start == 0 || value[start-1] == '\n' {
		return length
	}
	return 0
}

type reviewProgressWriter struct {
	output io.Writer
	parser *streamMarkerParser
}

func newReviewProgressWriter(output io.Writer) *reviewProgressWriter {
	return &reviewProgressWriter{output: output, parser: newStreamMarkerParser(verdict.BlockStartMarker)}
}

func (w *reviewProgressWriter) Write(p []byte) (int, error) {
	visible := w.parser.Feed(string(p))
	if visible == "" {
		return len(p), nil
	}
	if _, err := io.WriteString(w.output, visible); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *reviewProgressWriter) EndTurn() error {
	visible := w.parser.Flush()
	w.parser.Reset()
	if visible == "" {
		return nil
	}
	_, err := io.WriteString(w.output, visible)
	return err
}

const (
	ansiColorImplement = "\x1b[34m" // blue
	ansiColorReview    = "\x1b[33m" // yellow
	ansiColorReset     = "\x1b[0m"
)

// roleLabelWriter prefixes each line of live output with which role produced
// it, so implementer and reviewer text stay distinguishable while they
// interleave on the same terminal stream. The label is colorized only when
// output is a real terminal (and NO_COLOR isn't set); redirected output
// (files, pipes, tests) gets a plain bracketed label instead.
type roleLabelWriter struct {
	output      io.Writer
	prefix      string
	atLineStart bool
}

func newRoleLabelWriter(output io.Writer, role, ansiColor string) *roleLabelWriter {
	label := "[" + role + "] "
	if shouldColorizeOutput(output) {
		label = ansiColor + "[" + role + "]" + ansiColorReset + " "
	}
	return &roleLabelWriter{output: output, prefix: label, atLineStart: true}
}

func shouldColorizeOutput(output io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	file, ok := output.(interface{ Fd() uintptr })
	if !ok {
		return false
	}
	return isatty.IsTerminal(file.Fd())
}

func (w *roleLabelWriter) Write(p []byte) (int, error) {
	var labeled bytes.Buffer
	for _, b := range p {
		if w.atLineStart {
			labeled.WriteString(w.prefix)
			w.atLineStart = false
		}
		labeled.WriteByte(b)
		if b == '\n' {
			w.atLineStart = true
		}
	}
	if _, err := w.output.Write(labeled.Bytes()); err != nil {
		return 0, err
	}
	return len(p), nil
}

func ensureHeadUnchanged(ctx context.Context, git GitRunner, branchPoint string) error {
	head, err := git.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("verify implementation did not commit: %w", err)
	}
	if strings.TrimSpace(head) != branchPoint {
		return fmt.Errorf("implementation changed HEAD; agents must leave changes uncommitted")
	}
	return nil
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
		if branchType != "" || strings.Contains(candidate, "implement") || strings.Contains(candidate, "rig") {
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

func blockingFindings(review verdict.Verdict) []verdict.Finding {
	findings := make([]verdict.Finding, 0)
	for _, finding := range review.Findings {
		if finding.Kind == verdict.Blocking {
			findings = append(findings, finding)
		}
	}
	return findings
}

func nitFindings(review verdict.Verdict) []verdict.Finding {
	findings := make([]verdict.Finding, 0)
	for _, finding := range review.Findings {
		if finding.Kind == verdict.Nit {
			findings = append(findings, finding)
		}
	}
	return findings
}

func formatBlockingFindings(findings []verdict.Finding) string {
	if len(findings) == 0 {
		return "(none)"
	}
	var builder strings.Builder
	for _, finding := range findings {
		fmt.Fprintf(&builder, "- [%s] %s — %s\n", finding.Kind, finding.Location, finding.Issue)
	}
	return strings.TrimSuffix(builder.String(), "\n")
}

func formatImplementSummary(iterations int, final verdict.Verdict, nits []verdict.Finding, diffStat string) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Iterations: %d\nFinal verdict: %s\nSummary: %s\nNit findings:\n", iterations, final.Status, final.Summary)
	if len(nits) == 0 {
		builder.WriteString("- (none)\n")
	} else {
		for _, finding := range nits {
			fmt.Fprintf(&builder, "- [%s] %s — %s\n", finding.Kind, finding.Location, finding.Issue)
		}
	}
	fmt.Fprintf(&builder, "Diff stat:\n%s\n", strings.TrimSpace(diffStat))
	return builder.String()
}

func (r *runArtifacts) recordSessions(iteration int, role string, sessionIDs []string) {
	for _, sessionID := range sessionIDs {
		if strings.TrimSpace(sessionID) == "" {
			continue
		}
		r.sessions = append(r.sessions, fmt.Sprintf("iteration %d %s: %s", iteration, role, sessionID))
	}
}

func (r *runArtifacts) recordReview(iteration int, review ReviewExecution) error {
	if err := writeArtifact(filepath.Join(r.dir, fmt.Sprintf("iteration-%02d-review.feed", iteration)), review.Feed); err != nil {
		return err
	}
	if err := writeArtifact(filepath.Join(r.dir, fmt.Sprintf("iteration-%02d-review.transcript", iteration)), review.Transcript); err != nil {
		return err
	}
	return writeArtifact(filepath.Join(r.dir, fmt.Sprintf("iteration-%02d-verdict.txt", iteration)), formatVerdict(review.Verdict))
}

func (r *runArtifacts) writeSessions() error {
	sessions := append([]string(nil), r.sessions...)
	sort.Strings(sessions)
	return writeArtifact(filepath.Join(r.dir, "sessions.txt"), strings.Join(sessions, "\n")+"\n")
}

func (r *runArtifacts) writeSummary(iterations int, final verdict.Verdict, nits []verdict.Finding, diffStat string) error {
	return writeArtifact(filepath.Join(r.dir, "summary.txt"), formatImplementSummary(iterations, final, nits, diffStat))
}

func writeArtifact(path, contents string) error {
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		return fmt.Errorf("write run artifact %s: %w", path, err)
	}
	return nil
}
