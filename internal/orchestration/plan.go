package orchestration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/harness"
	"github.com/igorrochap/syl/internal/tracker"
)

// PlanOptions contains the boundaries and user choices for an interactive plan session.
type PlanOptions struct {
	WorkRoot     string
	Topic        string
	TrackerName  config.Tracker
	Role         config.RoleConfig
	IssueTracker tracker.Tracker
	Adapter      harness.Adapter
	Output       io.Writer
	Spec         bool
	Grill        bool
	WithDocs     bool
}

// RunPlan validates the referenced skills, attaches the planner, and reports new tickets.
func RunPlan(ctx context.Context, options PlanOptions) error {
	if options.WithDocs && !options.Grill {
		return errors.New("--with-docs requires --grill")
	}
	if strings.TrimSpace(options.Topic) == "" {
		return errors.New("plan requires a topic")
	}
	if options.IssueTracker == nil {
		return errors.New("plan tracker is not configured")
	}
	if options.Adapter == nil {
		return errors.New("plan harness is not configured")
	}
	if options.Output == nil {
		options.Output = io.Discard
	}

	for _, name := range planSkills(options) {
		path := filepath.Join(options.WorkRoot, ".agents", "skills", name, "SKILL.md")
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) || err == nil && info.IsDir() {
			return fmt.Errorf("required plan skill %q is not installed in .agents/skills", name)
		}
		if err != nil {
			return fmt.Errorf("inspect plan skill %q: %w", name, err)
		}
	}

	before, err := options.IssueTracker.List(ctx)
	if err != nil {
		return fmt.Errorf("snapshot tickets before planning: %w", err)
	}
	request := harness.Request{
		Model:  options.Role.Model,
		Effort: options.Role.Effort,
		Prompt: composePlanPrompt(options),
		MCP:    options.Role.MCP,
	}
	if err := options.Adapter.Attach(ctx, request); err != nil {
		return err
	}
	after, err := options.IssueTracker.List(ctx)
	if err != nil {
		return fmt.Errorf("list tickets after planning: %w", err)
	}
	return writeCreatedTickets(options.Output, createdTickets(before, after))
}

func planSkills(options PlanOptions) []string {
	result := make([]string, 0, 3)
	if options.Grill {
		name := "grill-me"
		if options.WithDocs {
			name = "grill-with-docs"
		}
		result = append(result, name)
	}
	if options.Spec {
		result = append(result, "to-spec")
	}
	return append(result, "to-tickets")
}

func createdTickets(before, after []tracker.Ticket) []tracker.Ticket {
	existing := make(map[int]struct{}, len(before))
	for _, ticket := range before {
		existing[ticket.Number] = struct{}{}
	}
	created := make([]tracker.Ticket, 0)
	for _, ticket := range after {
		if _, ok := existing[ticket.Number]; !ok {
			created = append(created, ticket)
		}
	}
	sort.Slice(created, func(i, j int) bool {
		return created[i].Number < created[j].Number
	})
	return created
}

func writeCreatedTickets(output io.Writer, created []tracker.Ticket) error {
	if len(created) == 0 {
		if _, err := fmt.Fprintln(output, "No tickets created."); err != nil {
			return fmt.Errorf("write created tickets report: %w", err)
		}
		return nil
	}

	numbers := make([]string, 0, len(created))
	for _, ticket := range created {
		numbers = append(numbers, "#"+strconv.Itoa(ticket.Number))
	}
	if _, err := fmt.Fprintf(output, "Created: %s\n", strings.Join(numbers, ", ")); err != nil {
		return fmt.Errorf("write created tickets report: %w", err)
	}
	if next, ok := nextCreatedTicket(created); ok {
		if _, err := fmt.Fprintf(output, "Next: syl implement %d\n", next); err != nil {
			return fmt.Errorf("write created tickets report: %w", err)
		}
	}
	return nil
}

func nextCreatedTicket(created []tracker.Ticket) (int, bool) {
	unknown := 0
	for _, ticket := range created {
		blocked, available := ticketBlocked(ticket.Body)
		if available && !blocked {
			return ticket.Number, true
		}
		if !available && unknown == 0 {
			unknown = ticket.Number
		}
	}
	return unknown, unknown != 0
}

func ticketBlocked(body string) (blocked, available bool) {
	lines := strings.Split(body, "\n")
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		var value string
		switch {
		case strings.HasPrefix(lower, "**blocked by:**"):
			value = strings.TrimSpace(trimmed[len("**Blocked by:**"):])
		case lower == "## blocked by":
			for _, following := range lines[index+1:] {
				value = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(following), "-"))
				if value != "" {
					break
				}
			}
		default:
			continue
		}
		if value == "" {
			return false, false
		}
		value = strings.ToLower(value)
		return !strings.HasPrefix(value, "none"), true
	}
	return false, false
}
