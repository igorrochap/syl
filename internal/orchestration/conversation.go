package orchestration

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/igorrochap/rig/internal/harness"
)

const questionProtocolInstruction = `If you are genuinely blocked on a decision that cannot be resolved from the ticket or the code, stop working and emit exactly this block:

QUESTION:
<the question, one or more lines>
END QUESTION

Ambiguity should have been resolved during planning, and trivial choices should be decided without asking. After emitting the block, stop working.`

const QuestionInputHelp = "When a harness asks a QUESTION, rig prints it and reads a multi-line answer from stdin until an empty line or EOF."

type harnessStreamStarter func(context.Context) (harness.Stream, error)

type conversationOptions struct {
	output    io.Writer
	mode      HarnessOutputMode
	questions *QuestionHandler
	sessionID string
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
	if h.notifier != nil {
		_ = h.notifier.Notify(ctx, fmt.Sprintf("rig is waiting for your answer on %s", h.target))
	}
	if h.output != nil {
		if _, err := fmt.Fprintf(h.output, "QUESTION:\n%s\nEND QUESTION\n", question); err != nil {
			return "", fmt.Errorf("print harness question: %w", err)
		}
	}
	answer, err := h.readAnswer()
	if err != nil {
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
		if len(line) > 0 {
			line = strings.TrimSuffix(line, "\n")
			line = strings.TrimSuffix(line, "\r")
			if line == "" {
				break
			}
			lines = append(lines, line)
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return "", fmt.Errorf("read answer to harness question: %w", err)
		}
	}

	answer := strings.TrimSpace(strings.Join(lines, "\n"))
	if answer == "" {
		return "", errors.New("answer to harness question is empty; enter an answer followed by an empty line or EOF")
	}
	return answer, nil
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
	sessionID := options.sessionID
	next := start

	for {
		stream, err := next(ctx)
		if err != nil {
			return harnessTranscript{}, err
		}

		result, err := consumeHarnessStream(stream, options.output, options.mode)
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
			return harnessTranscript{Transcript: transcript.String(), SessionIDs: sessionIDs}, nil
		}
		if options.questions == nil {
			return harnessTranscript{}, errors.New("harness asked a question but no terminal question handler is configured")
		}
		if sessionID == "" {
			return harnessTranscript{}, errors.New("harness asked a question before emitting a session id")
		}

		answer, err := options.questions.Handle(ctx, result.Question)
		if err != nil {
			return harnessTranscript{}, err
		}
		next = func(resumeContext context.Context) (harness.Stream, error) {
			return adapter.Resume(resumeContext, sessionID, answer)
		}
	}
}

func consumeHarnessStream(stream harness.Stream, output io.Writer, mode HarnessOutputMode) (harnessStreamResult, error) {
	var transcript strings.Builder
	var sessionIDs []string
	parser := newQuestionParser()
	var pendingRaw []harness.Event
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

		if err := renderHarnessEvent(output, mode, event, parsed, parser, &pendingRaw); err != nil {
			return harnessStreamResult{}, err
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
	flush := parser.Flush()
	if flush != "" {
		transcript.WriteString(flush)
		if mode != RawHarnessOutput {
			if err := writeParsedEvent(output, harness.Event{Type: harness.EventAssistantText, Text: flush}); err != nil {
				return harnessStreamResult{}, err
			}
		}
	}
	if err := stream.Wait(); err != nil {
		return harnessStreamResult{}, fmt.Errorf("read harness: %w", err)
	}
	return harnessStreamResult{Transcript: transcript.String(), SessionIDs: sessionIDs}, nil
}

func renderHarnessEvent(output io.Writer, mode HarnessOutputMode, event harness.Event, parsed questionParseResult, parser *questionParser, pendingRaw *[]harness.Event) error {
	if mode != RawHarnessOutput {
		visibleEvent := event
		visibleEvent.Text = parsed.VisibleText
		return writeParsedEvent(output, visibleEvent)
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
