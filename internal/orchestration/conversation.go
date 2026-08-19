package orchestration

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/igorrochap/syl/internal/harness"
)

const QuestionInputHelp = "When a harness asks a QUESTION, syl prints it as a block and reads a single-line answer from stdin. A trailing backslash continues onto the next line; an empty answer re-prompts; EOF without an answer is an error."

type harnessStreamStarter func(context.Context) (harness.Stream, error)

type conversationOptions struct {
	request   harness.Request
	output    io.Writer
	artifact  io.Writer
	mode      HarnessOutputMode
	questions *QuestionHandler
	sessionID string
	role      string
}

func normalizeSessionID(value string) (string, bool) {
	value = strings.TrimSpace(value)
	return value, value != ""
}

func appendSessionID(sessionIDs []string, sessionID string) []string {
	sessionID, ok := normalizeSessionID(sessionID)
	if !ok {
		return sessionIDs
	}
	for _, recorded := range sessionIDs {
		if recorded == sessionID {
			return sessionIDs
		}
	}
	return append([]string{sessionID}, sessionIDs...)
}

type QuestionHandler struct {
	answers  *bufio.Reader
	output   io.Writer
	notifier Notifier
	target   string
}

type harnessStreamResult struct {
	Transcript string
	SessionIDs []string
	Question   string
	Blocked    bool
}

func NewQuestionHandler(input io.Reader, output io.Writer, target string, notifier Notifier) *QuestionHandler {
	var answers *bufio.Reader
	if input != nil {
		answers = bufio.NewReader(input)
	}
	return &QuestionHandler{
		answers:  answers,
		output:   output,
		notifier: notifier,
		target:   questionTarget(target),
	}
}

func (h *QuestionHandler) Handle(ctx context.Context, question string) (string, error) {
	return h.handle(ctx, "harness", question)
}

func (h *QuestionHandler) handle(ctx context.Context, role, question string) (string, error) {
	role = questionRole(role)
	if h.notifier != nil {
		_ = h.notifier.Notify(ctx, fmt.Sprintf("syl is waiting for your answer on %s", h.target))
	}
	if err := h.printQuestion(role, question); err != nil {
		return "", err
	}
	answer, err := h.readAnswer()
	if err != nil {
		return "", err
	}
	if err := h.printResume(role); err != nil {
		return "", err
	}
	return answer, nil
}

func (h *QuestionHandler) readAnswer() (string, error) {
	if h.answers == nil {
		return "", errors.New("cannot answer harness question: terminal input is unavailable")
	}

	var lines []string
	for {
		line, err := h.answers.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("read answer to harness question: %w", err)
		}
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")
		continuation := strings.HasSuffix(line, `\`)
		if continuation {
			line = strings.TrimSuffix(line, `\`)
		}
		lines = append(lines, line)

		if continuation {
			if errors.Is(err, io.EOF) {
				return "", errors.New("cannot answer harness question: terminal input is unavailable")
			}
			if err := h.printAnswerPrompt(); err != nil {
				return "", err
			}
			continue
		}

		answer := strings.TrimSpace(strings.Join(lines, "\n"))
		if answer != "" {
			return answer, nil
		}
		if errors.Is(err, io.EOF) {
			return "", errors.New("cannot answer harness question: terminal input is unavailable")
		}
		lines = nil
		if err := h.printEmptyAnswerHint(); err != nil {
			return "", err
		}
	}
}

func (h *QuestionHandler) printQuestion(role, question string) error {
	if h.output == nil {
		return nil
	}
	if _, err := fmt.Fprintf(h.output, "\n---\n[%s] question on %s\n%s\nanswer> ", role, h.target, question); err != nil {
		return fmt.Errorf("print harness question: %w", err)
	}
	return nil
}

func (h *QuestionHandler) printAnswerPrompt() error {
	if h.output == nil {
		return nil
	}
	if _, err := io.WriteString(h.output, "\nanswer> "); err != nil {
		return fmt.Errorf("print harness answer prompt: %w", err)
	}
	return nil
}

func (h *QuestionHandler) printEmptyAnswerHint() error {
	if h.output == nil {
		return nil
	}
	if _, err := io.WriteString(h.output, "\nanswer cannot be empty; please try again\nanswer> "); err != nil {
		return fmt.Errorf("print harness answer hint: %w", err)
	}
	return nil
}

func (h *QuestionHandler) printResume(role string) error {
	if h.output == nil {
		return nil
	}
	if _, err := fmt.Fprintf(h.output, "\nanswer sent — resuming %s…\n--- %s output resumes ---\n", roleNoun(role), role); err != nil {
		return fmt.Errorf("print harness resume confirmation: %w", err)
	}
	return nil
}

func questionRole(role string) string {
	role = strings.TrimSpace(role)
	if role == "" {
		return "harness"
	}
	return role
}

func roleNoun(role string) string {
	switch role = questionRole(role); role {
	case "implement":
		return "implementer"
	case "review":
		return "reviewer"
	default:
		return role
	}
}

func questionTarget(reference string) string {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return "review"
	}
	if strings.HasPrefix(reference, "#") {
		return reference
	}
	return "#" + reference
}

func runHarnessConversation(ctx context.Context, adapter harness.Adapter, start harnessStreamStarter, options conversationOptions) (harnessTranscript, error) {
	var transcript strings.Builder
	var sessionIDs []string
	sessionID, _ := normalizeSessionID(options.sessionID)
	next := start

	for {
		stream, err := next(ctx)
		if err != nil {
			return harnessTranscript{}, err
		}

		result, err := consumeHarnessStreamWithArtifact(stream, options.output, options.artifact, options.mode)
		if err != nil {
			return harnessTranscript{}, err
		}
		if result.Transcript != "" {
			if transcript.Len() > 0 && !strings.HasSuffix(transcript.String(), "\n") {
				transcript.WriteString("\n")
			}
			transcript.WriteString(result.Transcript)
		}
		sessionIDs = append(sessionIDs, result.SessionIDs...)
		if sessionID == "" && len(result.SessionIDs) > 0 {
			sessionID = result.SessionIDs[0]
		}
		if !result.Blocked {
			return harnessTranscript{Transcript: transcript.String(), SessionIDs: appendSessionID(sessionIDs, sessionID)}, nil
		}
		if options.questions == nil {
			return harnessTranscript{}, errors.New("harness asked a question but no terminal question handler is configured")
		}
		if sessionID == "" {
			return harnessTranscript{}, errors.New("harness asked a question before emitting a session id")
		}

		answer, err := options.questions.handle(ctx, options.role, result.Question)
		if err != nil {
			return harnessTranscript{}, err
		}
		next = func(resumeContext context.Context) (harness.Stream, error) {
			resumeRequest := options.request
			resumeRequest.Prompt = answer
			return adapter.Resume(resumeContext, sessionID, resumeRequest)
		}
	}
}

func consumeHarnessStream(stream harness.Stream, output io.Writer, mode HarnessOutputMode) (harnessStreamResult, error) {
	return consumeHarnessStreamWithArtifact(stream, output, nil, mode)
}

func consumeHarnessStreamWithArtifact(stream harness.Stream, output, artifact io.Writer, mode HarnessOutputMode) (harnessStreamResult, error) {
	var transcript strings.Builder
	var sessionIDs []string
	parser := newQuestionParser()
	var pendingRaw []harness.Event
	var artifactPendingRaw []harness.Event
	artifactMode := mode
	if mode == QuietHarnessOutput {
		artifactMode = ParsedHarnessOutput
	}
	var sp *spinner
	// atLineStart tracks whether the terminal cursor is at the start of a fresh
	// line, so the spinner can be handed a clean line to draw on and prose is
	// never clobbered by its cursor-control escapes.
	atLineStart := true
	if mode == QuietHarnessOutput {
		sp = newSpinner(output)
		sp.Start()
		defer sp.Stop()
	}
	// Some harnesses repeat the final assistant message in their result event.
	// Streamed assistant text is authoritative; result text is only a fallback
	// for harnesses that emitted no assistant text during this turn.
	assistantTextSeen := false
	events := stream.Events()
	for event := range events {
		if event.SessionID != "" {
			sessionIDs = append(sessionIDs, event.SessionID)
		}

		useEventText := event.Type == harness.EventAssistantText ||
			(event.Type == harness.EventResult && !assistantTextSeen)
		parsed := questionParseResult{}
		if useEventText {
			parsed = parser.Feed(event.Text)
		}

		if err := renderHarnessEvent(output, mode, event, parsed, parser, &pendingRaw, sp, &atLineStart); err != nil {
			return harnessStreamResult{}, err
		}
		if artifact != nil {
			if err := renderHarnessEvent(artifact, artifactMode, event, parsed, parser, &artifactPendingRaw, nil, nil); err != nil {
				return harnessStreamResult{}, err
			}
		}
		if useEventText {
			transcript.WriteString(parsed.VisibleText)
		}
		if event.Type == harness.EventAssistantText {
			assistantTextSeen = true
		}

		if parsed.Found {
			if err := drainHarnessStream(events, stream); err != nil {
				return harnessStreamResult{}, fmt.Errorf("finish harness turn after QUESTION: %w", err)
			}
			return harnessStreamResult{
				Transcript: transcript.String(),
				SessionIDs: sessionIDs,
				Question:   parsed.Question,
				Blocked:    true,
			}, nil
		}
		if event.Type == harness.EventResult && event.IsError {
			harnessError := event.Text
			if harnessError == "" {
				harnessError = "unknown harness error"
			}
			return harnessStreamResult{}, fmt.Errorf("harness returned an error: %s", harnessError)
		}
	}

	if err := flushRawEvents(output, pendingRaw); err != nil {
		return harnessStreamResult{}, err
	}
	if err := flushRawEvents(artifact, artifactPendingRaw); err != nil {
		return harnessStreamResult{}, err
	}
	flush := parser.Flush()
	if flush != "" {
		transcript.WriteString(flush)
		if mode != RawHarnessOutput {
			sp.Stop()
			if err := writeParsedEvent(output, harness.Event{Type: harness.EventAssistantText, Text: flush}); err != nil {
				return harnessStreamResult{}, err
			}
		}
		if artifact != nil && mode != RawHarnessOutput {
			if err := writeParsedEvent(artifact, harness.Event{Type: harness.EventAssistantText, Text: flush}); err != nil {
				return harnessStreamResult{}, err
			}
		}
	}
	if err := stream.Wait(); err != nil {
		return harnessStreamResult{}, fmt.Errorf("read harness: %w", err)
	}
	return harnessStreamResult{Transcript: transcript.String(), SessionIDs: sessionIDs}, nil
}

func renderHarnessEvent(output io.Writer, mode HarnessOutputMode, event harness.Event, parsed questionParseResult, parser *questionParser, pendingRaw *[]harness.Event, sp *spinner, atLineStart *bool) error {
	if mode != RawHarnessOutput {
		visibleEvent := event
		visibleEvent.Text = parsed.VisibleText
		if mode == QuietHarnessOutput {
			if event.Type == harness.EventToolUse {
				// A tool call marks the start of silent work; the spinner
				// stands in for it. End the prose line first so the animation
				// never overwrites it.
				return startSpinner(output, sp, atLineStart)
			}
			if !eventHasVisibleProse(visibleEvent) {
				// Other silent events (session ids, control frames like the
				// ping/content_block markers that stream between prose tokens,
				// empty deltas) carry nothing to show. Leave the spinner exactly
				// as it is: starting it here would split mid-line prose across
				// labeled lines, and stopping it would make it flicker.
				return nil
			}
		}
		sp.Stop()
		if err := writeParsedEvent(output, visibleEvent); err != nil {
			return err
		}
		if atLineStart != nil && visibleEvent.Text != "" {
			*atLineStart = strings.HasSuffix(visibleEvent.Text, "\n")
		}
		return nil
	}
	if parsed.Found {
		*pendingRaw = nil
		return nil
	}
	if parser.Pending() {
		*pendingRaw = append(*pendingRaw, event)
		return nil
	}
	if err := flushRawEvents(output, *pendingRaw); err != nil {
		return err
	}
	*pendingRaw = nil
	return writeRawEvent(output, event)
}

// eventHasVisibleProse reports whether an event carries assistant prose that
// quiet mode should print. Tool calls, session ids, and empty text are silent;
// the spinner covers them instead.
func eventHasVisibleProse(event harness.Event) bool {
	switch event.Type {
	case harness.EventAssistantText, harness.EventResult:
		return event.Text != ""
	default:
		return false
	}
}

// startSpinner (re)starts the spinner for a stretch of silent work, first
// ending the current line if prose left the cursor mid-line so the animation
// never overwrites it. It is a no-op when the spinner is disabled or nil.
func startSpinner(output io.Writer, sp *spinner, atLineStart *bool) error {
	if sp == nil || !sp.enabled {
		return nil
	}
	if atLineStart != nil && !*atLineStart {
		if _, err := io.WriteString(output, "\n"); err != nil {
			return fmt.Errorf("write harness output: %w", err)
		}
		*atLineStart = true
	}
	sp.Start()
	return nil
}

func flushRawEvents(output io.Writer, events []harness.Event) error {
	for _, event := range events {
		if err := writeRawEvent(output, event); err != nil {
			return err
		}
	}
	return nil
}

func drainHarnessStream(events <-chan harness.Event, stream harness.Stream) error {
	for range events {
	}
	return stream.Wait()
}
