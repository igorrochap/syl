package cli

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/ui"
	"github.com/igorrochap/syl/internal/usage"
	"github.com/spf13/cobra"
)

func (a *App) usageCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "usage [run]",
		Short: "show per-role token usage for a run",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runDir, err := resolveUsageRun(a.originRoot, args)
			if err != nil {
				return err
			}
			artifactPath := filepath.Join(runDir, "usage.json")
			artifact, err := usage.ReadArtifact(artifactPath)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					artifact, recomputeErr := recomputeUsage(a.originRoot, a.workRoot, runDir)
					if recomputeErr != nil {
						return recomputeErr
					}
					renderer := ui.New(cmd.OutOrStdout(), ui.DetectCaps(cmd.OutOrStdout()))
					if writeErr := renderer.Text("recomputed from transcripts — usage.json not found"); writeErr != nil {
						return writeErr
					}
					return renderUsageWithRenderer(renderer, artifact)
				}
				return err
			}
			return renderUsage(cmd.OutOrStdout(), artifact)
		},
	}
	return command
}

func recomputeUsage(originRoot, workRoot, runDir string) (usage.Artifact, error) {
	roles := make(map[string]usage.RoleMetadata)
	if projectConfig, err := config.Load(originRoot); err == nil {
		roles["implement"] = usage.RoleMetadata{
			Harness: string(projectConfig.Roles.Implement.Harness),
			Model:   projectConfig.Roles.Implement.Model,
		}
		roles["review"] = usage.RoleMetadata{
			Harness: string(projectConfig.Roles.Review.Harness),
			Model:   projectConfig.Roles.Review.Model,
		}
	}
	return usage.RecomputeArtifact(runDir, workRoot, "", roles)
}

func resolveUsageRun(originRoot string, args []string) (string, error) {
	runsDir := filepath.Join(originRoot, ".syl", "runs")
	if len(args) == 1 {
		name := strings.TrimSpace(args[0])
		if name == "" {
			return "", errors.New("usage run name cannot be empty")
		}
		candidate := name
		if !filepath.IsAbs(candidate) {
			if strings.ContainsRune(candidate, os.PathSeparator) || strings.HasPrefix(candidate, ".") {
				candidate = filepath.Join(originRoot, candidate)
			} else {
				candidate = filepath.Join(runsDir, candidate)
			}
		}
		info, err := os.Stat(candidate)
		if err != nil {
			return "", fmt.Errorf("run directory %q not found: %w", candidate, err)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("run path %q is not a directory", candidate)
		}
		return candidate, nil
	}

	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("no run directories found in %s", runsDir)
		}
		return "", fmt.Errorf("read run directories %s: %w", runsDir, err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no run directories found in %s", runsDir)
	}
	sort.Strings(names)
	return filepath.Join(runsDir, names[len(names)-1]), nil
}

func renderUsage(output io.Writer, artifact usage.Artifact) error {
	renderer := ui.New(output, ui.DetectCaps(output))
	return renderUsageWithRenderer(renderer, artifact)
}

func renderUsageWithRenderer(renderer *ui.Renderer, artifact usage.Artifact) error {
	lastIteration := -1
	rows := make([]ui.Row, 0)
	for _, entry := range artifact.Entries {
		if lastIteration != -1 && entry.Iteration != lastIteration {
			if err := renderUsageIteration(renderer, lastIteration, rows, lastIteration != artifact.Entries[0].Iteration); err != nil {
				return err
			}
			rows = rows[:0]
		}
		lastIteration = entry.Iteration
		rows = append(rows, usageRow(entry))
	}
	if lastIteration != -1 {
		if err := renderUsageIteration(renderer, lastIteration, rows, lastIteration != artifact.Entries[0].Iteration); err != nil {
			return err
		}
	}
	if err := renderer.Text(""); err != nil {
		return err
	}
	if err := renderer.Text("Disclaimer: " + artifact.Disclaimer); err != nil {
		return err
	}
	return nil
}

func renderUsageIteration(renderer *ui.Renderer, iteration int, rows []ui.Row, separate bool) error {
	if separate {
		if err := renderer.Text(""); err != nil {
			return err
		}
	}
	if err := renderer.Text(fmt.Sprintf("iteration %d", iteration)); err != nil {
		return err
	}
	return renderer.Table(rows)
}

func usageRow(entry usage.Entry) ui.Row {
	return ui.Row{
		Key:   fmt.Sprintf("%s (%s, %s):", entry.Role, entry.Harness, entry.Model),
		Value: usageValue(entry),
	}
}

func usageValue(entry usage.Entry) string {
	if !entry.Tracked || entry.Metrics == nil {
		reason := entry.Reason
		if reason == "" {
			reason = "usage was not tracked"
		}
		if reason == "usage unavailable" {
			return reason
		}
		return "not tracked: " + reason
	}
	if entry.Harness == "codex" {
		return formatCodexUsage(*entry.Metrics)
	}
	return formatClaudeUsage(*entry.Metrics)
}

func formatCodexUsage(metrics usage.Metrics) string {
	cachedPercent := 0.0
	if metrics.InputTokens > 0 {
		cachedPercent = 100 * float64(metrics.CachedInputTokens) / float64(metrics.InputTokens)
	}
	return fmt.Sprintf(
		"input %s (%.0f%% cached) · output %s (%s reasoning)",
		formatTokenCount(metrics.InputTokens), math.Round(cachedPercent),
		formatTokenCount(metrics.OutputTokens), formatTokenCount(metrics.ReasoningOutputTokens),
	)
}

func formatClaudeUsage(metrics usage.Metrics) string {
	return fmt.Sprintf(
		"weighted_estimate=%.2f input_tokens=%d output_tokens=%d cache_write_tokens=%d cache_read_tokens=%d",
		metrics.WeightedEstimate, metrics.InputTokens, metrics.OutputTokens,
		metrics.CacheWriteTokens, metrics.CacheReadTokens,
	)
}

func formatTokenCount(tokens int64) string {
	switch {
	case tokens >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(tokens)/1_000_000)
	case tokens >= 1_000:
		return fmt.Sprintf("%.1fk", float64(tokens)/1_000)
	default:
		return fmt.Sprintf("%d", tokens)
	}
}
