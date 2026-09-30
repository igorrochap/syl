// Package pageview builds HTTP-independent values for the syl ui pages.
package pageview

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/igorrochap/syl/internal/readmodel"
	"github.com/igorrochap/syl/internal/runrecord"
)

// AbsoluteTimeStyle selects the absolute time shown by a page.
type AbsoluteTimeStyle int

const (
	// AbsoluteDateTime shows a local date and time.
	AbsoluteDateTime AbsoluteTimeStyle = iota
	// AbsoluteDate shows a UTC date without a time.
	AbsoluteDate
)

// TicketReferenceStyle selects whether a ticket reference is shown as written or by number.
type TicketReferenceStyle int

const (
	// TicketAsWritten preserves the nonblank reference.
	TicketAsWritten TicketReferenceStyle = iota
	// TicketByNumber trims the reference and normalizes positive numeric tickets.
	TicketByNumber
)

// RunKindStyle selects the label used when a Run has no recorded kind.
type RunKindStyle int

const (
	// RunKindAsRun labels a missing kind as "Run".
	RunKindAsRun RunKindStyle = iota
	// RunKindAsDash labels a missing kind with an em dash.
	RunKindAsDash
)

// IterationStyle selects how an iteration and its maximum are presented.
type IterationStyle int

const (
	// IterationFraction shows an iteration as "current / maximum".
	IterationFraction IterationStyle = iota
	// IterationOptionalMaximum omits a missing maximum.
	IterationOptionalMaximum
	// IterationRunSummary uses the Run header wording.
	IterationRunSummary
)

// FormatDuration returns a compact duration label.
func FormatDuration(duration time.Duration) string {
	seconds := int(duration / time.Second)
	if seconds < 1 {
		return "0s"
	}
	minutes, seconds := seconds/60, seconds%60
	hours, minutes := minutes/60, minutes%60
	if hours > 0 {
		return strconv.Itoa(hours) + "h " + strconv.Itoa(minutes) + "m"
	}
	if minutes > 0 {
		return strconv.Itoa(minutes) + "m"
	}
	return strconv.Itoa(seconds) + "s"
}

// FormatTokenCount returns a compact token count.
func FormatTokenCount(tokens int64) string {
	switch {
	case tokens >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(tokens)/1_000_000)
	case tokens >= 1_000:
		return fmt.Sprintf("%.1fk", float64(tokens)/1_000)
	default:
		return strconv.FormatInt(tokens, 10)
	}
}

// FormatRelativeStartTime returns a relative label, falling back to local date and time.
func FormatRelativeStartTime(startedAt, now time.Time) string {
	if startedAt.IsZero() {
		return "unknown"
	}
	ago := now.Sub(startedAt)
	if ago < time.Minute {
		return "just now"
	}
	if ago < time.Hour {
		return strconv.Itoa(int(ago/time.Minute)) + " min ago"
	}
	if ago < 24*time.Hour {
		return strconv.Itoa(int(ago/time.Hour)) + " h ago"
	}
	return FormatAbsoluteTime(startedAt, AbsoluteDateTime)
}

// FormatAbsoluteTime returns a date using the selected page format.
func FormatAbsoluteTime(value time.Time, style AbsoluteTimeStyle) string {
	if value.IsZero() {
		return "—"
	}
	if style == AbsoluteDate {
		return value.UTC().Format("2 Jan 2006")
	}
	return value.Local().Format("2 Jan 2006, 15:04")
}

// FormatTicketReference returns the page label for a ticket reference.
func FormatTicketReference(reference string, style TicketReferenceStyle) string {
	trimmed := strings.TrimSpace(reference)
	if trimmed == "" {
		return "—"
	}
	if style == TicketAsWritten {
		return reference
	}
	number := strings.TrimPrefix(trimmed, "#")
	parsed, err := strconv.Atoi(number)
	if err != nil || parsed < 1 {
		return trimmed
	}
	return "#" + strconv.Itoa(parsed)
}

// FormatRunKind returns the page label for a Run kind.
func FormatRunKind(kind runrecord.Kind, style RunKindStyle) string {
	if kind != "" {
		return string(kind)
	}
	if style == RunKindAsRun {
		return "Run"
	}
	return "—"
}

// FormatHarnessAndModel returns the harness and model label used by a page.
func FormatHarnessAndModel(harnessName, model string) string {
	if harnessName == "" {
		harnessName = "unknown"
	}
	if model == "" {
		return harnessName
	}
	return harnessName + " · " + model
}

// FormatIteration returns the label for an iteration and its maximum.
func FormatIteration(iterationNumber, maximum int, style IterationStyle) string {
	switch style {
	case IterationRunSummary:
		if maximum > 0 {
			return strconv.Itoa(iterationNumber) + " of " + strconv.Itoa(maximum) + " iterations"
		}
		if iterationNumber > 0 {
			return strconv.Itoa(iterationNumber) + " iterations"
		}
		return "—"
	case IterationOptionalMaximum:
		if maximum > 0 {
			return strconv.Itoa(iterationNumber) + " / " + strconv.Itoa(maximum)
		}
		if iterationNumber > 0 {
			return strconv.Itoa(iterationNumber)
		}
		return "—"
	default:
		if iterationNumber <= 0 && maximum <= 0 {
			return "—"
		}
		return strconv.Itoa(iterationNumber) + " / " + strconv.Itoa(maximum)
	}
}

// HealthClass returns the visual class for Project health.
func HealthClass(health readmodel.Health) string {
	switch health {
	case readmodel.HealthOK:
		return "pill-green"
	case readmodel.HealthInvalid:
		return "pill-red"
	default:
		return "pill-neutral"
	}
}

// RunStatus is the shared label and style mapping for a Run status.
type RunStatus struct {
	Label                string
	OverviewLabel        string
	PillClass            string
	ActivityClass        string
	RowClass             string
	ActivityApplicable   bool
	ShowRecordedActivity bool
	Dismissible          bool
}

// PresentRunStatus returns the one page mapping for an observed Run status.
func PresentRunStatus(status runrecord.ObservedStatus) RunStatus {
	switch status {
	case runrecord.ObservedRunning:
		return RunStatus{
			Label: "running", OverviewLabel: "running", PillClass: "running",
			ActivityClass: "running", ActivityApplicable: true, ShowRecordedActivity: true,
		}
	case runrecord.ObservedInterrupted:
		return RunStatus{
			Label: "Interrupted", OverviewLabel: "Interrupted", PillClass: "red",
			ActivityClass: "interrupted", RowClass: "interrupted", Dismissible: true,
		}
	case runrecord.ObservedApproved:
		return RunStatus{
			Label: "approved", OverviewLabel: "approved", PillClass: "green", ActivityClass: "running",
			ShowRecordedActivity: true,
		}
	case runrecord.ObservedExhausted:
		return RunStatus{
			Label: "exhausted", OverviewLabel: "exhausted", PillClass: "plum", ActivityClass: "running",
			ShowRecordedActivity: true,
		}
	case runrecord.ObservedFailed:
		return RunStatus{
			Label: "failed", OverviewLabel: "failed", PillClass: "red", ActivityClass: "running",
			ShowRecordedActivity: true,
		}
	case runrecord.ObservedCancelled:
		return RunStatus{
			Label: "cancelled", OverviewLabel: "cancelled", PillClass: "neutral", ActivityClass: "running",
			ShowRecordedActivity: true,
		}
	case runrecord.ObservedCompleted:
		return RunStatus{
			Label: "completed", OverviewLabel: "completed", PillClass: "completed", ActivityClass: "running",
			ShowRecordedActivity: true,
		}
	case runrecord.ObservedUnknown:
		return RunStatus{Label: "unknown", OverviewLabel: "Unknown", PillClass: "unknown", ActivityClass: "unknown"}
	default:
		return RunStatus{Label: "—", OverviewLabel: "Unknown", PillClass: "neutral", ActivityClass: "unknown"}
	}
}
