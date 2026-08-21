// Package usage records and reads per-role token usage for syl runs.
package usage

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	claudetranscript "github.com/igorrochap/syl/internal/harness/claude/transcript"
)

const (
	// SchemaVersion identifies the usage.json schema written by syl.
	SchemaVersion = 1
	// Disclaimer explains why weighted_estimate must not be treated as a bill.
	Disclaimer = "weighted_estimate is an estimate of plan-quota pressure, not a billing statement."
)

var (
	errTranscriptParse      = claudetranscript.ErrParse
	errCodexRolloutParse    = errors.New("Codex rollout parse failure")
	errCodexRolloutMissing  = errors.New("Codex rollout is missing")
	errCodexRolloutMultiple = errors.New("Codex rollout has multiple matching files")
)

// Metrics contains the raw usage fields recorded in usage.json. Claude uses
// the cache and weighted-estimate fields; Codex uses the cached-input,
// cache-write, reasoning-output, and total-token fields.
type Metrics struct {
	InputTokens           int64   `json:"input_tokens"`
	OutputTokens          int64   `json:"output_tokens"`
	CacheWriteTokens      int64   `json:"cache_write_tokens"`
	CacheReadTokens       int64   `json:"cache_read_tokens"`
	WeightedEstimate      float64 `json:"weighted_estimate"`
	CachedInputTokens     int64   `json:"cached_input_tokens"`
	CacheWriteInputTokens int64   `json:"cache_write_input_tokens"`
	ReasoningOutputTokens int64   `json:"reasoning_output_tokens"`
	TotalTokens           int64   `json:"total_tokens"`
}

// Entry is one role invocation in one implement iteration.
type Entry struct {
	Iteration int      `json:"iteration"`
	Role      string   `json:"role"`
	Harness   string   `json:"harness"`
	Model     string   `json:"model"`
	Tracked   bool     `json:"tracked"`
	Metrics   *Metrics `json:"metrics"`
	Reason    string   `json:"reason,omitempty"`
}

// Artifact is the on-disk usage.json document.
type Artifact struct {
	SchemaVersion int     `json:"schema_version"`
	Entries       []Entry `json:"entries"`
	Disclaimer    string  `json:"disclaimer"`
}

// NewArtifact returns an empty artifact with the current schema metadata.
func NewArtifact() Artifact {
	return Artifact{
		SchemaVersion: SchemaVersion,
		Entries:       []Entry{},
		Disclaimer:    Disclaimer,
	}
}

// Upsert replaces the entry for a role in an iteration and keeps output stable.
func (a *Artifact) Upsert(entry Entry) {
	for index := range a.Entries {
		if a.Entries[index].Iteration == entry.Iteration && a.Entries[index].Role == entry.Role {
			a.Entries[index] = entry
			sortEntries(a.Entries)
			return
		}
	}
	a.Entries = append(a.Entries, entry)
	sortEntries(a.Entries)
}

func sortEntries(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Iteration != entries[j].Iteration {
			return entries[i].Iteration < entries[j].Iteration
		}
		return entries[i].Role < entries[j].Role
	})
}

// WriteArtifact serializes an artifact to path.
func WriteArtifact(path string, artifact Artifact) error {
	if artifact.SchemaVersion == 0 {
		artifact.SchemaVersion = SchemaVersion
	}
	if artifact.Disclaimer == "" {
		artifact.Disclaimer = Disclaimer
	}
	if artifact.Entries == nil {
		artifact.Entries = []Entry{}
	}
	sortEntries(artifact.Entries)
	contents, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return fmt.Errorf("encode usage artifact: %w", err)
	}
	contents = append(contents, '\n')
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		return fmt.Errorf("write usage artifact %s: %w", path, err)
	}
	return nil
}

// ReadArtifact parses and validates an artifact from path.
func ReadArtifact(path string) (Artifact, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return Artifact{}, err
	}
	var artifact Artifact
	if err := json.Unmarshal(contents, &artifact); err != nil {
		return Artifact{}, fmt.Errorf("decode usage artifact %s: %w", path, err)
	}
	if artifact.SchemaVersion == 0 {
		return Artifact{}, fmt.Errorf("decode usage artifact %s: missing schema_version", path)
	}
	if artifact.Disclaimer == "" {
		return Artifact{}, fmt.Errorf("decode usage artifact %s: missing disclaimer", path)
	}
	sortEntries(artifact.Entries)
	return artifact, nil
}

// Invocation describes the live window in which a role was run. The window
// is recorded at run time so resumed Claude sessions can be split correctly;
// Codex sessions use their rollout's cumulative total instead.
type Invocation struct {
	Iteration  int
	Role       string
	Harness    string
	Model      string
	SessionIDs []string
	StartedAt  time.Time
	EndedAt    time.Time
}

// CollectInvocation makes a non-failing usage result for one role invocation.
// Claude transcript and Codex rollout failures become untracked entries; they
// are never returned as errors to the implement loop.
func CollectInvocation(invocation Invocation, projectRoot, homeDir string) Entry {
	entry := Entry{
		Iteration: invocation.Iteration,
		Role:      invocation.Role,
		Harness:   invocation.Harness,
		Model:     invocation.Model,
		Tracked:   false,
	}
	switch invocation.Harness {
	case "claude":
		metrics, err := CollectClaude(projectRoot, homeDir, invocation.SessionIDs, invocation.StartedAt, invocation.EndedAt)
		if err != nil {
			entry.Reason = shortReason(err)
			return entry
		}
		entry.Tracked = true
		entry.Metrics = &metrics
		return entry
	case "codex":
		metrics, err := CollectCodex(homeDir, invocation.SessionIDs)
		if err != nil {
			entry.Reason = shortReason(err)
			return entry
		}
		entry.Tracked = true
		entry.Metrics = &metrics
		return entry
	default:
		entry.Reason = fmt.Sprintf("usage tracking is not implemented for the %s harness", invocation.Harness)
		return entry
	}
}

func shortReason(err error) string {
	if errors.Is(err, errCodexRolloutMissing) {
		return "Codex rollout is missing"
	}
	if errors.Is(err, errCodexRolloutParse) {
		return "Codex rollout could not be parsed"
	}
	if errors.Is(err, errCodexRolloutMultiple) {
		return "Codex rollout has multiple matching files"
	}
	if errors.Is(err, os.ErrNotExist) {
		return "Claude transcript is missing"
	}
	if errors.Is(err, errTranscriptParse) {
		return "Claude transcript could not be parsed"
	}
	return strings.TrimSpace(err.Error())
}

// CollectCodex reads the last cumulative token-count event from the Codex
// rollout for the invocation's session. Unlike Claude, Codex does not need a
// time window: a role invocation corresponds to one rollout file.
func CollectCodex(homeDir string, sessionIDs []string) (Metrics, error) {
	sessionID := ""
	for _, rawSessionID := range sessionIDs {
		if sessionID = strings.TrimSpace(rawSessionID); sessionID != "" {
			break
		}
	}
	if sessionID == "" {
		return Metrics{}, errors.New("Codex invocation did not produce a session id")
	}
	if strings.TrimSpace(homeDir) == "" {
		var err error
		homeDir, err = os.UserHomeDir()
		if err != nil {
			return Metrics{}, fmt.Errorf("determine Codex home directory: %w", err)
		}
	}

	rolloutPath, err := findCodexRollout(homeDir, sessionID)
	if err != nil {
		return Metrics{}, err
	}
	return collectCodexRollout(rolloutPath)
}

func findCodexRollout(homeDir, sessionID string) (string, error) {
	pattern := filepath.Join(homeDir, ".codex", "sessions", "*", "*", "*", "rollout-*-"+sessionID+".jsonl")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return "", fmt.Errorf("find Codex rollout for %s: %w", sessionID, err)
	}
	if len(paths) == 0 {
		return "", fmt.Errorf("find Codex rollout for %s: %w", sessionID, errCodexRolloutMissing)
	}
	sort.Strings(paths)
	if len(paths) > 1 {
		return "", fmt.Errorf("find Codex rollout for %s: %w", sessionID, errCodexRolloutMultiple)
	}
	return paths[0], nil
}

type codexRolloutLine struct {
	Payload struct {
		Type string `json:"type"`
		Info struct {
			TotalTokenUsage *codexTokenUsage `json:"total_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

type codexTokenUsage struct {
	InputTokens           *int64 `json:"input_tokens"`
	CachedInputTokens     *int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens *int64 `json:"cache_write_input_tokens"`
	OutputTokens          *int64 `json:"output_tokens"`
	ReasoningOutputTokens *int64 `json:"reasoning_output_tokens"`
	TotalTokens           *int64 `json:"total_tokens"`
}

func collectCodexRollout(path string) (Metrics, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Metrics{}, fmt.Errorf("read Codex rollout %s: %w", path, errCodexRolloutMissing)
		}
		return Metrics{}, fmt.Errorf("read Codex rollout %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var metrics Metrics
	found := false
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record codexRolloutLine
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return Metrics{}, codexRolloutParseError(path, lineNumber, err)
		}
		if record.Payload.Type != "token_count" {
			continue
		}
		parsed, err := parseCodexTokenUsage(record.Payload.Info.TotalTokenUsage)
		if err != nil {
			return Metrics{}, codexRolloutParseError(path, lineNumber, err)
		}
		metrics = parsed
		found = true
	}
	if err := scanner.Err(); err != nil {
		return Metrics{}, fmt.Errorf("read Codex rollout %s: %w", path, err)
	}
	if !found {
		return Metrics{}, codexRolloutParseError(path, 0, errors.New("rollout has no token_count event"))
	}
	return metrics, nil
}

func codexRolloutParseError(path string, lineNumber int, cause error) error {
	if lineNumber == 0 {
		return fmt.Errorf("%w: %s: %v", errCodexRolloutParse, path, cause)
	}
	return fmt.Errorf("%w: %s line %d: %v", errCodexRolloutParse, path, lineNumber, cause)
}

func parseCodexTokenUsage(fields *codexTokenUsage) (Metrics, error) {
	if fields == nil {
		return Metrics{}, errors.New("token_count event has no total_token_usage")
	}
	if fields.InputTokens == nil || fields.CachedInputTokens == nil || fields.CacheWriteInputTokens == nil ||
		fields.OutputTokens == nil || fields.ReasoningOutputTokens == nil || fields.TotalTokens == nil {
		return Metrics{}, errors.New("total_token_usage is missing a token field")
	}
	values := []*int64{
		fields.InputTokens, fields.CachedInputTokens, fields.CacheWriteInputTokens,
		fields.OutputTokens, fields.ReasoningOutputTokens, fields.TotalTokens,
	}
	for _, value := range values {
		if *value < 0 {
			return Metrics{}, errors.New("total_token_usage contains negative token counts")
		}
	}
	return Metrics{
		InputTokens:           *fields.InputTokens,
		CachedInputTokens:     *fields.CachedInputTokens,
		CacheWriteInputTokens: *fields.CacheWriteInputTokens,
		OutputTokens:          *fields.OutputTokens,
		ReasoningOutputTokens: *fields.ReasoningOutputTokens,
		TotalTokens:           *fields.TotalTokens,
	}, nil
}

// CollectClaude reads the parent session transcripts and all direct sub-agent
// transcripts for the supplied sessions, selecting records in the invocation
// window and retaining only the last usage snapshot for each message ID.
func CollectClaude(projectRoot, homeDir string, sessionIDs []string, start, end time.Time) (Metrics, error) {
	if len(sessionIDs) == 0 {
		return Metrics{}, errors.New("Claude invocation did not produce a session id")
	}
	if start.IsZero() || end.IsZero() {
		return Metrics{}, errors.New("Claude invocation has no usage time window")
	}
	if start.After(end) {
		return Metrics{}, errors.New("Claude invocation has an invalid usage time window")
	}
	return collectClaude(projectRoot, homeDir, sessionIDs, start, end)
}

// CollectClaudeAll reads all usage records from the supplied Claude sessions.
// It is used by the read-only usage fallback for runs that predate recorded
// invocation windows.
func CollectClaudeAll(projectRoot, homeDir string, sessionIDs []string) (Metrics, error) {
	if len(sessionIDs) == 0 {
		return Metrics{}, errors.New("Claude invocation did not produce a session id")
	}
	return collectClaude(projectRoot, homeDir, sessionIDs, time.Time{}, time.Time{})
}

func collectClaude(projectRoot, homeDir string, sessionIDs []string, start, end time.Time) (Metrics, error) {
	reader := claudetranscript.New(homeDir)
	snapshots := make(map[string]transcriptUsage)
	seenSessions := make(map[string]struct{})
	for _, rawSessionID := range sessionIDs {
		sessionID := strings.TrimSpace(rawSessionID)
		if sessionID == "" {
			continue
		}
		if _, seen := seenSessions[sessionID]; seen {
			continue
		}
		seenSessions[sessionID] = struct{}{}

		transcriptPath, err := reader.Find(projectRoot, sessionID)
		if err != nil {
			return Metrics{}, err
		}
		if err := collectTranscriptFile(reader, transcriptPath, start, end, snapshots); err != nil {
			return Metrics{}, err
		}
		subagentPaths, err := reader.FindSubagents(projectRoot, sessionID)
		if err != nil {
			return Metrics{}, err
		}
		for _, subagentPath := range subagentPaths {
			if err := collectTranscriptFile(reader, subagentPath, start, end, snapshots); err != nil {
				return Metrics{}, err
			}
		}
	}
	if len(snapshots) == 0 {
		return Metrics{}, errors.New("no Claude usage records found in the invocation window")
	}

	var metrics Metrics
	for _, snapshot := range snapshots {
		metrics.InputTokens += snapshot.InputTokens
		metrics.OutputTokens += snapshot.OutputTokens
		metrics.CacheWriteTokens += snapshot.CacheWriteTokens
		metrics.CacheReadTokens += snapshot.CacheReadTokens
	}
	metrics.WeightedEstimate = float64(metrics.InputTokens) +
		1.25*float64(metrics.CacheWriteTokens) +
		0.1*float64(metrics.CacheReadTokens) +
		float64(metrics.OutputTokens)
	return metrics, nil
}

type transcriptUsage struct {
	InputTokens      int64
	OutputTokens     int64
	CacheWriteTokens int64
	CacheReadTokens  int64
}

type transcriptLine struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Timestamp string          `json:"timestamp"`
	Role      string          `json:"role"`
	Usage     json.RawMessage `json:"usage"`
	Message   struct {
		ID        string          `json:"id"`
		Timestamp string          `json:"timestamp"`
		Role      string          `json:"role"`
		Usage     json.RawMessage `json:"usage"`
	} `json:"message"`
}

func collectTranscriptFile(
	reader claudetranscript.Reader,
	path string,
	start, end time.Time,
	snapshots map[string]transcriptUsage,
) error {
	entries, err := reader.Read(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		var record transcriptLine
		if err := json.Unmarshal(entry.Raw, &record); err != nil {
			return transcriptParseError(path, entry.Line, err)
		}
		usageRaw := record.Message.Usage
		if len(usageRaw) == 0 || string(usageRaw) == "null" {
			usageRaw = record.Usage
		}
		if len(usageRaw) == 0 || string(usageRaw) == "null" {
			continue
		}
		role := record.Message.Role
		if role == "" {
			role = record.Role
		}
		if role != "assistant" && record.Type != "assistant" {
			continue
		}
		messageID := record.Message.ID
		if messageID == "" {
			messageID = record.ID
		}
		if strings.TrimSpace(messageID) == "" {
			return transcriptParseError(path, entry.Line, errors.New("usage record has no message id"))
		}
		timestampValue := record.Timestamp
		if timestampValue == "" {
			timestampValue = record.Message.Timestamp
		}
		timestamp, err := parseTimestamp(timestampValue)
		if err != nil {
			return transcriptParseError(path, entry.Line, err)
		}
		if (!start.IsZero() && timestamp.Before(start)) || (!end.IsZero() && timestamp.After(end)) {
			continue
		}
		parsedUsage, err := parseUsage(usageRaw)
		if err != nil {
			return transcriptParseError(path, entry.Line, err)
		}
		// Assigning in transcript order intentionally keeps the final streaming
		// snapshot for a message ID and avoids multiplying content-block snapshots.
		snapshots[messageID] = parsedUsage
	}
	return nil
}

func transcriptParseError(path string, lineNumber int, cause error) error {
	return fmt.Errorf("%w: %s line %d: %v", errTranscriptParse, path, lineNumber, cause)
}

func parseTimestamp(value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, errors.New("usage record has no timestamp")
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid timestamp %q", value)
	}
	return parsed, nil
}

func parseUsage(raw json.RawMessage) (transcriptUsage, error) {
	var fields struct {
		InputTokens              *int64 `json:"input_tokens"`
		OutputTokens             *int64 `json:"output_tokens"`
		CacheWriteTokens         *int64 `json:"cache_write_tokens"`
		CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
		CacheReadTokens          *int64 `json:"cache_read_tokens"`
		CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return transcriptUsage{}, err
	}
	if fields.InputTokens == nil || fields.OutputTokens == nil {
		return transcriptUsage{}, errors.New("usage record is missing input_tokens or output_tokens")
	}
	cacheWrite := fields.CacheWriteTokens
	if cacheWrite == nil {
		cacheWrite = fields.CacheCreationInputTokens
	}
	cacheRead := fields.CacheReadTokens
	if cacheRead == nil {
		cacheRead = fields.CacheReadInputTokens
	}
	if *fields.InputTokens < 0 ||
		*fields.OutputTokens < 0 ||
		(cacheWrite != nil && *cacheWrite < 0) ||
		(cacheRead != nil && *cacheRead < 0) {
		return transcriptUsage{}, errors.New("usage record contains negative token counts")
	}
	result := transcriptUsage{InputTokens: *fields.InputTokens, OutputTokens: *fields.OutputTokens}
	if cacheWrite != nil {
		result.CacheWriteTokens = *cacheWrite
	}
	if cacheRead != nil {
		result.CacheReadTokens = *cacheRead
	}
	return result, nil
}
