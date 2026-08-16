// Package verdict parses and formats the reviewer's structured outcome.
package verdict

import (
	"fmt"
	"strings"
)

type Status string

const (
	Approve Status = "approve"
	Revise  Status = "revise"
)

type FindingKind string

const (
	Blocking FindingKind = "blocking"
	Nit      FindingKind = "nit"
)

type Finding struct {
	Kind     FindingKind
	Location string
	Issue    string
}

type Verdict struct {
	Status   Status
	Summary  string
	Findings []Finding
}

// Parse parses the last verdict block in a review response.
func Parse(text string) (Verdict, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "VERDICT:") {
			start = i
		}
	}
	if start == -1 {
		return Verdict{}, fmt.Errorf("verdict block is missing")
	}
	if start+2 >= len(lines) {
		return Verdict{}, fmt.Errorf("verdict block is incomplete")
	}

	status, err := parseStatus(strings.TrimSpace(strings.TrimPrefix(lines[start], "VERDICT:")))
	if err != nil {
		return Verdict{}, err
	}
	if !strings.HasPrefix(lines[start+1], "SUMMARY:") {
		return Verdict{}, fmt.Errorf("verdict block is missing SUMMARY")
	}
	summary := strings.TrimSpace(strings.TrimPrefix(lines[start+1], "SUMMARY:"))
	if summary == "" {
		return Verdict{}, fmt.Errorf("verdict SUMMARY is empty")
	}
	if strings.TrimSpace(lines[start+2]) != "FINDINGS:" {
		return Verdict{}, fmt.Errorf("verdict block is missing FINDINGS")
	}

	parsed := Verdict{Status: status, Summary: summary}
	for _, line := range lines[start+3:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		finding, err := parseFinding(line)
		if err != nil {
			return Verdict{}, err
		}
		parsed.Findings = append(parsed.Findings, finding)
	}
	return parsed, nil
}

func parseStatus(value string) (Status, error) {
	switch Status(value) {
	case Approve, Revise:
		return Status(value), nil
	default:
		return "", fmt.Errorf("invalid verdict status %q", value)
	}
}

func parseFinding(line string) (Finding, error) {
	const prefix = "- ["
	if !strings.HasPrefix(line, prefix) {
		return Finding{}, fmt.Errorf("invalid verdict finding %q", line)
	}
	endTag := strings.Index(line, "] ")
	if endTag < len(prefix) {
		return Finding{}, fmt.Errorf("invalid verdict finding %q", line)
	}
	kind := FindingKind(line[len(prefix):endTag])
	if kind != Blocking && kind != Nit {
		return Finding{}, fmt.Errorf("invalid verdict finding tag %q", kind)
	}
	payload := strings.TrimSpace(line[endTag+2:])
	location, issue, ok := strings.Cut(payload, " — ")
	if !ok || strings.TrimSpace(location) == "" || strings.TrimSpace(issue) == "" {
		return Finding{}, fmt.Errorf("invalid verdict finding %q", line)
	}
	return Finding{
		Kind:     kind,
		Location: strings.TrimSpace(location),
		Issue:    strings.TrimSpace(issue),
	}, nil
}
