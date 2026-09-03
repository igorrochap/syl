package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/tracker"
	"github.com/igorrochap/syl/internal/ui"
)

func writeImplementBanner(
	output io.Writer,
	originRoot string,
	projectConfig config.Config,
	ticket tracker.Ticket,
	artifactDir, worktreePath, implementContext, reviewContext string,
) error {
	relativeArtifactDir, err := filepath.Rel(originRoot, artifactDir)
	if err != nil {
		return fmt.Errorf("make run artifacts path relative to project root: %w", err)
	}
	implementContextIndicator := contextIndicator(implementContext)
	reviewContextIndicator := contextIndicator(reviewContext)
	rows := []ui.Field{
		{
			Label: "implementer",
			Value: fmt.Sprintf("%s · %s · effort %s%s",
				projectConfig.Roles.Implement.Harness, projectConfig.Roles.Implement.Model, projectConfig.Roles.Implement.Effort, implementContextIndicator),
		},
		{
			Label: "reviewer",
			Value: fmt.Sprintf("%s · %s · effort %s%s",
				projectConfig.Roles.Review.Harness, projectConfig.Roles.Review.Model, projectConfig.Roles.Review.Effort, reviewContextIndicator),
		},
		{Label: "max iterations", Value: fmt.Sprintf("%d", projectConfig.Loop.MaxIterations)},
		{Label: "run artifacts", Value: relativeArtifactDir},
	}
	if worktreePath != "" {
		rows = append(rows, ui.Field{Label: "worktree", Value: worktreePath})
	}
	err = ui.New(output, ui.DetectCaps(output)).Banner(ui.Banner{
		Title: fmt.Sprintf("syl implement #%d — %s", ticket.Number, ticket.Title),
		Rows:  rows,
	})
	if err != nil {
		return fmt.Errorf("write implement banner: %w", err)
	}
	return nil
}

func writeReviewBanner(output io.Writer, projectConfig config.Config, ticketRef string, ticket *tracker.Ticket, reviewContext string) error {
	heading := "syl review — working tree"
	if ticket != nil {
		heading = fmt.Sprintf("syl review %s — %s", ticketRef, ticket.Title)
	}
	err := ui.New(output, ui.DetectCaps(output)).Banner(ui.Banner{
		Title: heading,
		Rows: []ui.Field{{Label: "reviewer", Value: fmt.Sprintf("%s · %s · effort %s%s",
			projectConfig.Roles.Review.Harness, projectConfig.Roles.Review.Model, projectConfig.Roles.Review.Effort, contextIndicator(reviewContext))}},
	})
	if err != nil {
		return fmt.Errorf("write review banner: %w", err)
	}
	return nil
}

func contextIndicator(context string) string {
	if strings.TrimSpace(context) == "" {
		return ""
	}
	return " · context"
}

func writePlanBanner(output io.Writer, projectConfig config.Config, target string) error {
	heading := "syl plan"
	if target = strings.TrimSpace(target); target != "" {
		heading += " " + target
	}
	err := ui.New(output, ui.DetectCaps(output)).Banner(ui.Banner{
		Title: heading,
		Rows: []ui.Field{{
			Label: "planner",
			Value: fmt.Sprintf("%s · %s · effort %s",
				projectConfig.Roles.Plan.Harness, projectConfig.Roles.Plan.Model, projectConfig.Roles.Plan.Effort),
		}},
	})
	if err != nil {
		return fmt.Errorf("write plan banner: %w", err)
	}
	return nil
}
