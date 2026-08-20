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
	"github.com/igorrochap/syl/internal/usage"
	"github.com/spf13/cobra"
)

func (a *App) usageCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "usage [run]",
		Short: "show per-role token usage for a run",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runDir, err := resolveUsageRun(a.projectRoot, args)
			if err != nil {
				return err
			}
			artifactPath := filepath.Join(runDir, "usage.json")
			artifact, err := usage.ReadArtifact(artifactPath)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					artifact, recomputeErr := recomputeUsage(a.projectRoot, runDir)
					if recomputeErr != nil {
						return recomputeErr
					}
					if _, writeErr := fmt.Fprintln(cmd.OutOrStdout(), "recomputed from transcripts — usage.json not found"); writeErr != nil {
						return writeErr
					}
					return renderUsage(cmd.OutOrStdout(), artifact)
				}
				return err
			}
			return renderUsage(cmd.OutOrStdout(), artifact)
		},
	}
	return command
}

func recomputeUsage(projectRoot, runDir string) (usage.Artifact, error) {
	roles := make(map[string]usage.RoleMetadata)
	if projectConfig, err := config.Load(projectRoot); err == nil {
		roles["implement"] = usage.RoleMetadata{
			Harness: string(projectConfig.Roles.Implement.Harness),
			Model:   projectConfig.Roles.Implement.Model,
		}
		roles["review"] = usage.RoleMetadata{
			Harness: string(projectConfig.Roles.Review.Harness),
			Model:   projectConfig.Roles.Review.Model,
		}
	}
	return usage.RecomputeArtifact(runDir, projectRoot, "", roles)
}

func resolveUsageRun(projectRoot string, args []string) (string, error) {
	runsDir := filepath.Join(projectRoot, ".syl", "runs")
	if len(args) == 1 {
		name := strings.TrimSpace(args[0])
		if name == "" {
			return "", errors.New("usage run name cannot be empty")
		}
		candidate := name
		if !filepath.IsAbs(candidate) {
			if strings.ContainsRune(candidate, os.PathSeparator) || strings.HasPrefix(candidate, ".") {
				candidate = filepath.Join(projectRoot, candidate)
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
	lastIteration := -1
	for _, entry := range artifact.Entries {
		if entry.Iteration != lastIteration {
			if lastIteration != -1 {
				if _, err := io.WriteString(output, "\n"); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintf(output, "iteration %d\n", entry.Iteration); err != nil {
				return err
			}
			lastIteration = entry.Iteration
		}
		if _, err := fmt.Fprintf(output, "%s (%s, %s): ", entry.Role, entry.Harness, entry.Model); err != nil {
			return err
		}
		if !entry.Tracked || entry.Metrics == nil {
			reason := entry.Reason
			if reason == "" {
				reason = "usage was not tracked"
			}
			if reason == "usage unavailable" {
				if _, err := io.WriteString(output, "usage unavailable\n"); err != nil {
					return err
				}
				continue
			}
			if _, err := fmt.Fprintf(output, "not tracked: %s\n", reason); err != nil {
				return err
			}
			continue
		}
		metrics := entry.Metrics
		if entry.Harness == "codex" {
			cachedPercent := 0.0
			if metrics.InputTokens > 0 {
				cachedPercent = 100 * float64(metrics.CachedInputTokens) / float64(metrics.InputTokens)
			}
			if _, err := fmt.Fprintf(output,
				"input %s (%.0f%% cached) · output %s (%s reasoning)\n",
				formatTokenCount(metrics.InputTokens), math.Round(cachedPercent),
				formatTokenCount(metrics.OutputTokens), formatTokenCount(metrics.ReasoningOutputTokens),
			); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(output,
			"weighted_estimate=%.2f input_tokens=%d output_tokens=%d cache_write_tokens=%d cache_read_tokens=%d\n",
			metrics.WeightedEstimate, metrics.InputTokens, metrics.OutputTokens,
			metrics.CacheWriteTokens, metrics.CacheReadTokens,
		); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(output, "\nDisclaimer: %s\n", artifact.Disclaimer); err != nil {
		return err
	}
	return nil
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
