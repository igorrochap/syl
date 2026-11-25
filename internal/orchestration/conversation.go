package orchestration

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/ui"
)

const QuestionInputHelp = "When a harness asks a QUESTION, syl prints it as a block and reads a single-line answer from stdin. A trailing backslash continues onto the next line; an empty answer re-prompts; EOF without an answer is an error."

type harnessStreamStarter func(context.Context) (harness.Stream, error)

type conversationOptions struct {
	request    harness.Request
	output     io.Writer
	artifact   io.Writer
	mode       HarnessOutputMode
	questions  *QuestionHandler
	sessionID  string
	role       string
	childSpans subagentEventSink
}

type harnessEventRenderer interface {
	BeforeEvent() error
	Assistant(string) error
	Tool(string, string) error
	EndTurn() error
}

type harnessTurnRenderer interface {
	StartTurn()
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
	answers       *bufio.Reader
	output        io.Writer
	notifier      Notifier
	target        string
	stateObserver questionStateObserver
	span          *activitySpan
}

type questionPresentation interface {
	pause(*activitySpan) error
	resume(*activitySpan) error
	printQuestion(*QuestionHandler, string, string) error
	printAnswerPrompt(*QuestionHandler) error
	printEmptyAnswerHint(*QuestionHandler) error
	recordAnswer(*QuestionHandler, string)
	printResume(*QuestionHandler, string) error
}

type compactQuestionPresentation struct{}

type framedQuestionPresentation struct{}

func questionPresentationFor(mode HarnessOutputMode) questionPresentation {
	if mode == ProgressHarnessOutput {
		return compactQuestionPresentation{}
	}
	return framedQuestionPresentation{}
}

type harnessStreamResult struct {
	Transcript string
	SessionIDs []string
	Question   string
	Blocked    bool
}

// NewQuestionHandler creates a question handler bound to the Run lifecycle observer.
func NewQuestionHandler(
	input io.Reader,
	output io.Writer,
	target string,
	notifier Notifier,
	observer questionStateObserver,
) *QuestionHandler {
	var answers *bufio.Reader
	if input != nil {
		answers = bufio.NewReader(input)
	}
	return &QuestionHandler{
		answers:       answers,
		output:        output,
		notifier:      notifier,
		target:        questionTarget(target),
		stateObserver: observer,
	}
}

func (h *QuestionHandler) Handle(ctx context.Context, question string) (string, error) {
	return h.handle(ctx, "harness", question, framedQuestionPresentation{})
}

func (h *QuestionHandler) setActivitySpan(span *activitySpan) {
	if h != nil {
		h.span = span
	}
}

func (h *QuestionHandler) handle(ctx context.Context, role, question string, presentation questionPresentation) (answer string, returnErr error) {
	role = questionRole(role)
	session, err := h.beginQuestion(question, presentation)
	if session != nil {
		defer func() { returnErr = errors.Join(returnErr, session.close(ctx, returnErr)) }()
	}
	if err != nil {
		return "", err
	}
	if h.stateObserver != nil {
		h.stateObserver.questionAsked(question)
	}
	if h.notifier != nil {
		_ = h.notifier.Notify(ctx, fmt.Sprintf("syl is waiting for your answer on %s", h.target))
	}
	if err := presentation.printQuestion(h, role, question); err != nil {
		return "", err
	}
	answer, err = h.readAnswer(presentation)
	if err != nil {
		return "", err
	}
	presentation.recordAnswer(h, answer)
	if err := session.answer(answer); err != nil {
		return "", err
	}
	if h.stateObserver != nil {
		h.stateObserver.questionAnswered()
	}
	if err := presentation.printResume(h, role); err != nil {
		return "", err
	}
	return answer, nil
}

func (h *QuestionHandler) beginQuestion(question string, presentation questionPresentation) (*questionSpanSession, error) {
	session := &questionSpanSession{span: h.span, presentation: presentation}
	if h.span == nil {
		return session, nil
	}
	child, err := h.span.startAwaitingAnswer(question)
	if err != nil {
		return nil, err
	}
	session.child = child
	session.paused = true
	return session, presentation.pause(h.span)
}

type questionSpanSession struct {
	span         *activitySpan
	child        *childActivitySpan
	presentation questionPresentation
	paused       bool
}

func (session *questionSpanSession) answer(answer string) error {
	if session.child == nil {
		return nil
	}
	return session.span.finishAwaitingAnswer(session.child, answer, activitySpanCompleted)
}

func (session *questionSpanSession) close(ctx context.Context, operationErr error) error {
	var closeErr error
	if session.child != nil && !session.child.ended {
		outcome := activitySpanOutcomeFor(ctx, operationErr, activitySpanCompleted)
		closeErr = session.span.finishAwaitingAnswer(session.child, "", outcome)
	}
	if !session.paused {
		return closeErr
	}
	return errors.Join(closeErr, session.presentation.resume(session.span))
}

func (h *QuestionHandler) readAnswer(presentation questionPresentation) (string, error) {
	if h.answers == nil {
		return "", errors.New("cannot answer harness question: terminal input is unavailable")
	}
	return h.readAnswerLines(presentation)
}

func (h *QuestionHandler) readAnswerLines(presentation questionPresentation) (string, error) {
	var lines []string
	for {
		line, continuation, eof, err := h.readAnswerLine()
		if err != nil {
			return "", err
		}
		lines = append(lines, line)

		if continuation {
			if eof {
				return "", errors.New("cannot answer harness question: terminal input is unavailable")
			}
			if err := presentation.printAnswerPrompt(h); err != nil {
				return "", err
			}
			continue
		}

		answer := strings.TrimSpace(strings.Join(lines, "\n"))
		if answer != "" {
			return answer, nil
		}
		if eof {
			return "", errors.New("cannot answer harness question: terminal input is unavailable")
		}
		lines = nil
		if err := presentation.printEmptyAnswerHint(h); err != nil {
			return "", err
		}
	}
}

func (h *QuestionHandler) readAnswerLine() (line string, continuation, eof bool, err error) {
	line, err = h.answers.ReadString('\n')
	eof = errors.Is(err, io.EOF)
	if err != nil && !eof {
		return "", false, false, fmt.Errorf("read answer to harness question: %w", err)
	}
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	continuation = strings.HasSuffix(line, `\`)
	if continuation {
		line = strings.TrimSuffix(line, `\`)
	}
	return line, continuation, eof, nil
}

func (h *QuestionHandler) writeOutput(value, action string) error {
	if h.output == nil {
		return nil
	}
	if _, err := io.WriteString(h.output, value); err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	return nil
}

func (h *QuestionHandler) writeFramedOutput(value, action string) error {
	if h.output == nil {
		return nil
	}
	if err := h.writeOutput(value, action); err != nil {
		return err
	}
	h.recordSpanOutput(value)
	return nil
}

func (h *QuestionHandler) recordSpanOutput(value string) {
	if h.span == nil {
		return
	}
	h.span.recordOutput(value)
}

func awaitingAnswerOutput(question string) string {
	var output strings.Builder
	output.WriteString("    awaiting answer\n")
	for _, line := range strings.Split(question, "\n") {
		fmt.Fprintf(&output, "    ┃ %s\n", line)
	}
	output.WriteString("    answer> ")
	return output.String()
}

func (compactQuestionPresentation) pause(span *activitySpan) error {
	return span.pauseForQuestion()
}

func (compactQuestionPresentation) resume(span *activitySpan) error {
	return span.resumeAfterQuestion()
}

func (compactQuestionPresentation) printQuestion(handler *QuestionHandler, _, question string) error {
	return handler.writeOutput(awaitingAnswerOutput(question), "print harness question")
}

func (compactQuestionPresentation) printAnswerPrompt(handler *QuestionHandler) error {
	return handler.writeOutput("\n    answer> ", "print harness answer prompt")
}

func (compactQuestionPresentation) printEmptyAnswerHint(handler *QuestionHandler) error {
	value := "\n    answer cannot be empty; please try again\n    answer> "
	return handler.writeOutput(value, "print harness answer hint")
}

func (compactQuestionPresentation) recordAnswer(*QuestionHandler, string) {}

func (compactQuestionPresentation) printResume(*QuestionHandler, string) error {
	return nil
}

func (framedQuestionPresentation) pause(span *activitySpan) error {
	span.pauseForFramedQuestion()
	return nil
}

func (framedQuestionPresentation) resume(span *activitySpan) error {
	span.resumeAfterFramedQuestion()
	return nil
}

func (framedQuestionPresentation) printQuestion(handler *QuestionHandler, role, question string) error {
	value := fmt.Sprintf("\n---\n%s question on %s\n%s\nanswer> ", role, handler.target, question)
	return handler.writeFramedOutput(value, "print harness question")
}

func (framedQuestionPresentation) printAnswerPrompt(handler *QuestionHandler) error {
	return handler.writeFramedOutput("\nanswer> ", "print harness answer prompt")
}

func (framedQuestionPresentation) printEmptyAnswerHint(handler *QuestionHandler) error {
	value := "\nanswer cannot be empty; please try again\nanswer> "
	return handler.writeFramedOutput(value, "print harness answer hint")
}

func (framedQuestionPresentation) recordAnswer(handler *QuestionHandler, answer string) {
	handler.recordSpanOutput(answer + "\n")
}

func (framedQuestionPresentation) printResume(handler *QuestionHandler, role string) error {
	value := fmt.Sprintf("\nanswer sent — resuming %s…\n--- %s output resumes ---\n", roleNoun(role), role)
	return handler.writeFramedOutput(value, "print harness resume confirmation")
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
	var failureTail strings.Builder
	var sessionIDs []string
	failureTailOutput := newPlainHarnessOutput(&failureTail, ParsedHarnessOutput)
	sessionID, _ := normalizeSessionID(options.sessionID)
	next := start
	questionPresentation := questionPresentationFor(options.mode)

	// Returning stops every harness this conversation started, so a
	// conversation that fails mid-stream cannot keep its harness running
	// alongside the caller's retry or fallback.
	conversationContext, stopConversation := context.WithCancel(ctx)
	defer stopConversation()

	for {
		stream, err := next(conversationContext)
		if err != nil {
			return currentConversationTranscript(&transcript, &failureTail, sessionIDs, sessionID), err
		}
		knownSessionID, known := knownStreamSession(stream)
		if known {
			sessionIDs = appendSessionID(sessionIDs, knownSessionID)
			sessionID = firstSessionID(sessionID, knownSessionID)
		}

		result, err := consumeHarnessStream(stream, streamEventOptions{
			output: options.output, artifact: options.artifact, failureTail: failureTailOutput,
			mode: options.mode, captureSessionEvents: !known, childSpans: options.childSpans,
		})
		appendTranscript(&transcript, result.Transcript)
		sessionIDs = append(sessionIDs, result.SessionIDs...)
		sessionID = firstSessionID(sessionID, result.SessionIDs...)
		if err != nil {
			go releaseAbandonedStream(stream)
			return currentConversationTranscript(&transcript, &failureTail, sessionIDs, sessionID), err
		}
		if !result.Blocked {
			return harnessTranscript{
				Transcript: transcript.String(), FailureTail: failureTail.String(),
				SessionIDs: appendSessionID(sessionIDs, sessionID),
			}, nil
		}
		if err := questionAnswerable(options.questions, sessionID); err != nil {
			return currentConversationTranscript(&transcript, &failureTail, sessionIDs, sessionID), err
		}

		answer, err := options.questions.handle(ctx, options.role, result.Question, questionPresentation)
		if err != nil {
			return currentConversationTranscript(&transcript, &failureTail, sessionIDs, sessionID), err
		}
		next = func(resumeContext context.Context) (harness.Stream, error) {
			resumeRequest := options.request
			resumeRequest.Prompt = answer
			return adapter.Resume(resumeContext, sessionID, resumeRequest)
		}
	}
}

func currentConversationTranscript(
	transcript *strings.Builder,
	failureTail *strings.Builder,
	sessionIDs []string,
	sessionID string,
) harnessTranscript {
	return harnessTranscript{
		Transcript: transcript.String(), FailureTail: failureTail.String(),
		SessionIDs: appendSessionID(sessionIDs, sessionID),
	}
}

// knownStreamSession reports the session a stream announced before emitting
// events, in which case its session events need not be captured.
func knownStreamSession(stream harness.Stream) (string, bool) {
	knownSession, ok := stream.(harness.SessionStream)
	if !ok {
		return "", false
	}
	return normalizeSessionID(knownSession.SessionID())
}

// firstSessionID keeps the conversation's current session once one is known,
// otherwise it adopts the first candidate.
func firstSessionID(current string, candidates ...string) string {
	if current != "" {
		return current
	}
	for _, candidate := range candidates {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

func appendTranscript(transcript *strings.Builder, text string) {
	if text == "" {
		return
	}
	if transcript.Len() > 0 && !strings.HasSuffix(transcript.String(), "\n") {
		transcript.WriteString("\n")
	}
	transcript.WriteString(text)
}

func questionAnswerable(questions *QuestionHandler, sessionID string) error {
	if questions == nil {
		return errors.New("harness asked a question but no terminal question handler is configured")
	}
	if sessionID == "" {
		return errors.New("harness asked a question before emitting a session id")
	}
	return nil
}

func consumeHarnessStream(stream harness.Stream, options streamEventOptions) (harnessStreamResult, error) {
	startHarnessTurn(options.output)
	var transcript strings.Builder
	var sessionIDs []string
	var pendingRaw []harness.Event
	var artifactPendingRaw []harness.Event
	options.parser = newQuestionParser()
	options.pendingRaw = &pendingRaw
	options.artifactPendingRaw = &artifactPendingRaw
	options.artifactMode = artifactHarnessOutputMode(options.mode)
	state := harnessStreamState{
		transcript:           &transcript,
		sessionIDs:           &sessionIDs,
		captureSessionEvents: options.captureSessionEvents,
		assistantTextSeen:    false,
	}
	events := stream.Events()
	if blocked, err := consumeHarnessEvents(events, stream, &state, options); err != nil {
		return currentHarnessStreamResult(&state, sessionIDs), err
	} else if blocked {
		return harnessStreamResult{
			Transcript: state.transcript.String(), SessionIDs: sessionIDs,
			Question: state.question, Blocked: true,
		}, nil
	}

	if err := flushHarnessStream(&state, options); err != nil {
		return currentHarnessStreamResult(&state, sessionIDs), err
	}
	if err := finishHarnessOutput(options.output); err != nil {
		return currentHarnessStreamResult(&state, sessionIDs), err
	}
	if err := finishHarnessOutput(options.artifact); err != nil {
		return currentHarnessStreamResult(&state, sessionIDs), err
	}
	if err := finishHarnessOutput(options.failureTail); err != nil {
		return currentHarnessStreamResult(&state, sessionIDs), err
	}
	if err := stream.Wait(); err != nil {
		return currentHarnessStreamResult(&state, sessionIDs), fmt.Errorf("read harness: %w", err)
	}
	return currentHarnessStreamResult(&state, sessionIDs), nil
}

func currentHarnessStreamResult(state *harnessStreamState, sessionIDs []string) harnessStreamResult {
	return harnessStreamResult{Transcript: state.transcript.String(), SessionIDs: sessionIDs}
}

func consumeHarnessEvents(events <-chan harness.Event, stream harness.Stream, state *harnessStreamState, options streamEventOptions) (bool, error) {
	for event := range events {
		blocked, err := state.process(event, options)
		if err != nil {
			err = errors.Join(err, finishHarnessOutput(options.failureTail))
			return false, err
		}
		if !blocked {
			continue
		}
		if err := drainHarnessStream(events, stream); err != nil {
			return false, fmt.Errorf("finish harness turn after QUESTION: %w", err)
		}
		if err := finishHarnessOutput(options.output); err != nil {
			return false, err
		}
		if err := finishHarnessOutput(options.artifact); err != nil {
			return false, err
		}
		if err := finishHarnessOutput(options.failureTail); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func flushHarnessStream(state *harnessStreamState, options streamEventOptions) error {
	if err := flushRawEvents(options.output, *options.pendingRaw); err != nil {
		return err
	}
	if err := flushRawEvents(options.artifact, *options.artifactPendingRaw); err != nil {
		return err
	}
	flush := options.parser.Flush()
	if flush == "" {
		return nil
	}
	state.transcript.WriteString(flush)
	if options.mode != RawHarnessOutput {
		if err := writeParsedEvent(options.output, harness.Event{Type: harness.EventAssistantText, Text: flush}); err != nil {
			return err
		}
	}
	if options.artifact != nil && options.artifactMode != RawHarnessOutput {
		if err := writeParsedEvent(options.artifact, harness.Event{Type: harness.EventAssistantText, Text: flush}); err != nil {
			return err
		}
	}
	return renderFailureTail(options.failureTail, harness.Event{Type: harness.EventAssistantText, Text: flush}, questionParseResult{VisibleText: flush})
}

type streamEventOptions struct {
	output               io.Writer
	artifact             io.Writer
	failureTail          io.Writer
	mode                 HarnessOutputMode
	artifactMode         HarnessOutputMode
	parser               *questionParser
	pendingRaw           *[]harness.Event
	artifactPendingRaw   *[]harness.Event
	childSpans           subagentEventSink
	captureSessionEvents bool
}

func artifactHarnessOutputMode(mode HarnessOutputMode) HarnessOutputMode {
	if mode == ProgressHarnessOutput {
		return ParsedHarnessOutput
	}
	return mode
}

type harnessStreamState struct {
	transcript           *strings.Builder
	sessionIDs           *[]string
	captureSessionEvents bool
	assistantTextSeen    bool
	question             string
}

func (s *harnessStreamState) process(event harness.Event, options streamEventOptions) (bool, error) {
	s.captureSessionID(event)
	useEventText := eventCarriesTranscriptText(event, s.assistantTextSeen)
	parsed := questionParseResult{}
	if useEventText {
		parsed = options.parser.Feed(event.Text)
	}
	if err := processSubagentEvent(event, options.childSpans); err != nil {
		return false, err
	}
	if err := s.renderEvent(event, parsed, options); err != nil {
		return false, err
	}
	s.recordEventText(event, parsed, useEventText)
	if parsed.Found {
		s.question = parsed.Question
		return true, nil
	}
	return false, harnessResultError(event)
}

func (s *harnessStreamState) captureSessionID(event harness.Event) {
	if s.captureSessionEvents && event.SessionID != "" {
		*s.sessionIDs = append(*s.sessionIDs, event.SessionID)
	}
}

func (s *harnessStreamState) renderEvent(event harness.Event, parsed questionParseResult, options streamEventOptions) error {
	if err := renderHarnessEvent(options.output, options.mode, event, parsed, options.parser, options.pendingRaw); err != nil {
		return err
	}
	if options.artifact == nil {
		return renderFailureTail(options.failureTail, event, parsed)
	}
	if err := renderHarnessEvent(options.artifact, options.artifactMode, event, parsed, options.parser, options.artifactPendingRaw); err != nil {
		return err
	}
	return renderFailureTail(options.failureTail, event, parsed)
}

func renderFailureTail(output io.Writer, event harness.Event, parsed questionParseResult) error {
	if output == nil {
		return nil
	}
	return renderHarnessEvent(output, ParsedHarnessOutput, event, parsed, nil, nil)
}

func (s *harnessStreamState) recordEventText(event harness.Event, parsed questionParseResult, useEventText bool) {
	if useEventText {
		s.transcript.WriteString(parsed.VisibleText)
	}
	if event.Type == harness.EventAssistantText {
		s.assistantTextSeen = true
	}
}

func processSubagentEvent(event harness.Event, spans subagentEventSink) error {
	if spans == nil {
		return nil
	}
	switch event.Type {
	case harness.EventSubagentStarted:
		return processSubagentStarted(event, spans)
	case harness.EventSubagentFinished:
		return processSubagentFinished(event, spans)
	case harness.EventToolUse:
		processSubagentTool(event, spans)
	}
	return nil
}

func processSubagentStarted(event harness.Event, spans subagentEventSink) error {
	id := strings.TrimSpace(event.SubagentID)
	label := strings.TrimSpace(event.SubagentLabel)
	if id == "" || label == "" {
		return errors.New("record subagent start: id and label are required")
	}
	if err := spans.SubagentStarted(id, label); err != nil {
		return fmt.Errorf("record subagent start: %w", err)
	}
	return nil
}

func processSubagentFinished(event harness.Event, spans subagentEventSink) error {
	id := strings.TrimSpace(event.SubagentID)
	if id == "" {
		return nil
	}
	if err := spans.SubagentFinished(id); err != nil {
		return fmt.Errorf("record subagent finish: %w", err)
	}
	return nil
}

func processSubagentTool(event harness.Event, spans subagentEventSink) {
	if id := strings.TrimSpace(event.SubagentID); id != "" {
		spans.SubagentTool(id, event.ToolName, event.ArgumentGist)
	}
}

func eventCarriesTranscriptText(event harness.Event, assistantTextSeen bool) bool {
	if event.Type == harness.EventAssistantText {
		return true
	}
	return event.Type == harness.EventResult && !assistantTextSeen
}

func harnessResultError(event harness.Event) error {
	if event.Type != harness.EventResult || !event.IsError {
		return nil
	}
	harnessError := event.Text
	if harnessError == "" {
		harnessError = "unknown harness error"
	}
	return fmt.Errorf("harness returned an error: %s", harnessError)
}

func renderHarnessEvent(output io.Writer, mode HarnessOutputMode, event harness.Event, parsed questionParseResult, parser *questionParser, pendingRaw *[]harness.Event) error {
	if mode == RawHarnessOutput {
		return renderRawHarnessEvent(output, event, parsed, parser, pendingRaw)
	}
	if renderer, ok := output.(harnessEventRenderer); ok && mode != ProgressHarnessOutput && pausesActivity(event, parsed) {
		if err := renderer.BeforeEvent(); err != nil {
			return err
		}
	}
	return renderParsedHarnessEvent(output, mode, event, parsed)
}

func pausesActivity(event harness.Event, parsed questionParseResult) bool {
	if event.Type == harness.EventAssistantText {
		return true
	}
	return event.Type == harness.EventResult && parsed.VisibleText != ""
}

func renderParsedHarnessEvent(output io.Writer, mode HarnessOutputMode, event harness.Event, parsed questionParseResult) error {
	if event.Type == harness.EventToolUse && event.SubagentID != "" {
		return nil
	}
	if mode == ProgressHarnessOutput {
		if event.Type != harness.EventToolUse {
			return nil
		}
		renderer, ok := output.(harnessEventRenderer)
		if !ok {
			return nil
		}
		return renderer.Tool(event.ToolName, event.ArgumentGist)
	}
	visibleEvent := event
	visibleEvent.Text = parsed.VisibleText
	return writeParsedEvent(output, visibleEvent)
}

func renderRawHarnessEvent(output io.Writer, event harness.Event, parsed questionParseResult, parser *questionParser, pendingRaw *[]harness.Event) error {
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

func flushRawEvents(output io.Writer, events []harness.Event) error {
	for _, event := range events {
		if err := writeRawEvent(output, event); err != nil {
			return err
		}
	}
	return nil
}

func isHarnessEventRenderer(output io.Writer) bool {
	_, ok := output.(harnessEventRenderer)
	return ok
}

func finishHarnessOutput(output io.Writer) error {
	if output == nil {
		return nil
	}
	renderer, ok := output.(harnessEventRenderer)
	if !ok {
		return nil
	}
	return renderer.EndTurn()
}

func startHarnessTurn(output io.Writer) {
	renderer, ok := output.(harnessTurnRenderer)
	if !ok {
		return
	}
	renderer.StartTurn()
}

func newLiveHarnessOutput(output io.Writer, mode HarnessOutputMode, role string, span *ui.ActivitySpanView) io.Writer {
	if mode == RawHarnessOutput {
		return output
	}
	caps := ui.DetectCaps(output)
	return ui.NewStream(output, caps, ui.StreamOptions{
		Gutter:    liveHarnessGutter(caps),
		ShowTools: mode == ParsedHarnessOutput,
		Span:      span,
		Role:      role,
	})
}

func harnessOutputMode(verbose, raw bool) HarnessOutputMode {
	if raw {
		return RawHarnessOutput
	}
	if verbose {
		return ParsedHarnessOutput
	}
	return ProgressHarnessOutput
}

func writeRoleSection(output io.Writer, role string) error {
	caps := ui.DetectCaps(output)
	if !caps.Color || caps.Width <= 0 {
		return nil
	}
	if err := ui.New(output, caps).Section(role); err != nil {
		return fmt.Errorf("write %s role section: %w", strings.ToLower(role), err)
	}
	return nil
}

func liveHarnessGutter(caps ui.Caps) string {
	if !caps.Color || caps.Width <= 0 {
		return ""
	}
	return "│ "
}

func newPlainHarnessOutput(output io.Writer, mode HarnessOutputMode) io.Writer {
	if mode == RawHarnessOutput {
		return output
	}
	return ui.NewStream(output, ui.Caps{Unicode: true}, ui.StreamOptions{ShowTools: true})
}

func drainHarnessStream(events <-chan harness.Event, stream harness.Stream) error {
	for range events {
	}
	return stream.Wait()
}

// releaseAbandonedStream reads what remains of a stopped stream so the adapter
// can finish and reap its harness process. The stream already failed, so its
// exit error carries nothing new.
func releaseAbandonedStream(stream harness.Stream) {
	_ = drainHarnessStream(stream.Events(), stream)
}
