package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/tracker"
)

const bannerRoleLabelWidth = len("implementer:")

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
	worktreeLine := ""
	if worktreePath != "" {
		worktreeLine = fmt.Sprintf("  worktree: %s\n", worktreePath)
	}
	implementContextIndicator := contextIndicator(implementContext)
	reviewContextIndicator := contextIndicator(reviewContext)
	_, err = fmt.Fprintf(output,
		"syl implement #%d — %s\n"+
			"  %s %s · %s · effort %s%s\n"+
			"  %s %s · %s · effort %s%s\n"+
			"  max iterations: %d\n"+
			"  run artifacts: %s\n"+
			"%s",
		ticket.Number, ticket.Title,
		formatBannerRoleLabel("implementer:"),
		projectConfig.Roles.Implement.Harness, projectConfig.Roles.Implement.Model, projectConfig.Roles.Implement.Effort, implementContextIndicator,
		formatBannerRoleLabel("reviewer:"),
		projectConfig.Roles.Review.Harness, projectConfig.Roles.Review.Model, projectConfig.Roles.Review.Effort, reviewContextIndicator,
		projectConfig.Loop.MaxIterations, relativeArtifactDir, worktreeLine,
	)
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
	_, err := fmt.Fprintf(output, "%s\n  %s %s · %s · effort %s%s\n",
		heading,
		formatBannerRoleLabel("reviewer:"),
		projectConfig.Roles.Review.Harness, projectConfig.Roles.Review.Model, projectConfig.Roles.Review.Effort, contextIndicator(reviewContext),
	)
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
	_, err := fmt.Fprintf(output, "%s\n  %s %s · %s · effort %s\n",
		heading,
		formatBannerRoleLabel("planner:"),
		projectConfig.Roles.Plan.Harness, projectConfig.Roles.Plan.Model, projectConfig.Roles.Plan.Effort,
	)
	if err != nil {
		return fmt.Errorf("write plan banner: %w", err)
	}
	return nil
}

func formatBannerRoleLabel(label string) string {
	return fmt.Sprintf("%-*s", bannerRoleLabelWidth, label)
}
