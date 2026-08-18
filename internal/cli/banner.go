package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/tracker"
)

func writeImplementBanner(output io.Writer, projectRoot string, projectConfig config.Config, ticket tracker.Ticket, artifactDir string) error {
	relativeArtifactDir, err := filepath.Rel(projectRoot, artifactDir)
	if err != nil {
		return fmt.Errorf("make run artifacts path relative to project root: %w", err)
	}
	_, err = fmt.Fprintf(output,
		"syl implement #%d — %s\n"+
			"  implementer: %s · %s · effort %s\n"+
			"  reviewer:    %s · %s · effort %s\n"+
			"  max iterations: %d\n"+
			"  run artifacts: %s\n",
		ticket.Number, ticket.Title,
		projectConfig.Roles.Implement.Harness, projectConfig.Roles.Implement.Model, projectConfig.Roles.Implement.Effort,
		projectConfig.Roles.Review.Harness, projectConfig.Roles.Review.Model, projectConfig.Roles.Review.Effort,
		projectConfig.Loop.MaxIterations, relativeArtifactDir,
	)
	if err != nil {
		return fmt.Errorf("write implement banner: %w", err)
	}
	return nil
}

func writeReviewBanner(output io.Writer, projectConfig config.Config, ticketRef string, ticket *tracker.Ticket) error {
	heading := "syl review — working tree"
	if ticket != nil {
		heading = fmt.Sprintf("syl review %s — %s", ticketRef, ticket.Title)
	}
	_, err := fmt.Fprintf(output, "%s\n  reviewer: %s · %s · effort %s\n",
		heading,
		projectConfig.Roles.Review.Harness, projectConfig.Roles.Review.Model, projectConfig.Roles.Review.Effort,
	)
	if err != nil {
		return fmt.Errorf("write review banner: %w", err)
	}
	return nil
}

func writePlanBanner(output io.Writer, projectConfig config.Config, target string) error {
	heading := "syl plan"
	if target = strings.TrimSpace(target); target != "" {
		heading += " " + target
	}
	_, err := fmt.Fprintf(output, "%s\n  planner:    %s · %s · effort %s\n",
		heading,
		projectConfig.Roles.Plan.Harness, projectConfig.Roles.Plan.Model, projectConfig.Roles.Plan.Effort,
	)
	if err != nil {
		return fmt.Errorf("write plan banner: %w", err)
	}
	return nil
}
