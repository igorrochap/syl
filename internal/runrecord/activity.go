package runrecord

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const activitiesFile = "activities.jsonl"

// ReadActivities decodes the Activity log inside a Run directory.
func ReadActivities(runDirectory string, files FileSystem) ([]ActivityEvent, error) {
	if files == nil {
		files = osFileSystem{}
	}
	path := filepath.Join(runDirectory, activitiesFile)
	contents, err := files.ReadFile(path)
	if err != nil {
		return nil, &activityLogReadError{path: path, err: err}
	}
	return ParseActivities(contents)
}

type activityLogReadError struct {
	path string
	err  error
}

func (readError *activityLogReadError) Error() string {
	return fmt.Sprintf("read Activity log %s: %v", readError.path, readError.err)
}

func (readError *activityLogReadError) Unwrap() error {
	return readError.err
}

// ActivityEventType identifies one append-only Activity log event.
type ActivityEventType string

const (
	// ActivityStart marks the opening of an Activity span.
	ActivityStart ActivityEventType = "start"
	// ActivityEnd marks the closing of an Activity span.
	ActivityEnd ActivityEventType = "end"
)

// ActivitySpanStart records one timed Activity occurrence as it opens.
type ActivitySpanStart struct {
	Type         ActivityEventType `json:"type"`
	SpanID       string            `json:"span_id"`
	ParentSpanID string            `json:"parent_span_id"`
	Activity     Activity          `json:"activity"`
	Iteration    string            `json:"iteration"`
	Label        string            `json:"label"`
	StartedAt    time.Time         `json:"started_at"`
}

// ActivitySpanEnd records one Activity occurrence as it closes.
type ActivitySpanEnd struct {
	Type    ActivityEventType `json:"type"`
	SpanID  string            `json:"span_id"`
	EndedAt time.Time         `json:"ended_at"`
	Outcome string            `json:"outcome"`
	Answer  string            `json:"answer,omitempty"`
}

// ActivityEvent is one decoded Activity log line.
type ActivityEvent struct {
	Type  ActivityEventType
	Start *ActivitySpanStart
	End   *ActivitySpanEnd
}

// ParseActivities reads the JSON object on each line of activities.jsonl.
func ParseActivities(contents []byte) ([]ActivityEvent, error) {
	var events []ActivityEvent
	scanner := bufio.NewScanner(strings.NewReader(string(contents)))
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var header struct {
			Type ActivityEventType `json:"type"`
		}
		if err := json.Unmarshal([]byte(line), &header); err != nil {
			return nil, fmt.Errorf("decode %s line %d: %w", activitiesFile, lineNumber, err)
		}
		switch header.Type {
		case ActivityStart:
			var start ActivitySpanStart
			if err := json.Unmarshal([]byte(line), &start); err != nil {
				return nil, fmt.Errorf("decode %s line %d: %w", activitiesFile, lineNumber, err)
			}
			events = append(events, ActivityEvent{Type: ActivityStart, Start: &start})
		case ActivityEnd:
			var end ActivitySpanEnd
			if err := json.Unmarshal([]byte(line), &end); err != nil {
				return nil, fmt.Errorf("decode %s line %d: %w", activitiesFile, lineNumber, err)
			}
			events = append(events, ActivityEvent{Type: ActivityEnd, End: &end})
		default:
			return nil, fmt.Errorf("decode %s line %d: unknown event type %q", activitiesFile, lineNumber, header.Type)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", activitiesFile, err)
	}
	return events, nil
}

// AppendActivityStart writes a span start event immediately to the Activity log.
func (writer *Writer) AppendActivityStart(start ActivitySpanStart) error {
	start.Type = ActivityStart
	return writer.appendActivityEvent(start)
}

// AppendActivityEnd writes a span end event immediately to the Activity log.
func (writer *Writer) AppendActivityEnd(end ActivitySpanEnd) error {
	end.Type = ActivityEnd
	return writer.appendActivityEvent(end)
}

func (writer *Writer) appendActivityEvent(event any) error {
	contents, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode Activity log event: %w", err)
	}
	contents = append(contents, '\n')
	path := filepath.Join(writer.directory, activitiesFile)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open Activity log %s: %w", path, err)
	}
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		return fmt.Errorf("append Activity log %s: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync Activity log %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close Activity log %s: %w", path, err)
	}
	return nil
}
