package usage

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/igorrochap/syl/internal/runrecord"
)

// RoleMetadata supplies the configuration fields that cannot be recovered
// from a pre-usage run's artifacts. Missing fields are rendered as unknown.
type RoleMetadata struct {
	Harness string
	Model   string
}

type sessionInvocation struct {
	iteration int
	role      string
	sessions  []string
}

type usageCluster struct {
	role        string
	invocations []sessionInvocation
}

// RecomputeArtifact rebuilds a usage artifact in memory from a run's legacy
// artifacts. It never writes anything to runDir. Missing or malformed usage
// inputs are represented by unavailable entries rather than returned as
// errors; only an invalid run directory is an error.
func RecomputeArtifact(runDir, projectRoot, homeDir string, roles map[string]RoleMetadata) (Artifact, error) {
	info, err := os.Stat(runDir)
	if err != nil {
		return Artifact{}, err
	}
	if !info.IsDir() {
		return Artifact{}, fmt.Errorf("run path %q is not a directory", runDir)
	}

	record, err := runrecord.NewReader(nil).Read(runDir)
	if err != nil {
		return Artifact{}, err
	}
	invocations := invocationsFromSessions(record.Sessions)
	invocations = mergeArtifactInvocations(record, invocations)
	clusters := clusterInvocations(invocations)

	artifact := NewArtifact()
	for _, cluster := range clusters {
		for _, entry := range recomputeCluster(record, projectRoot, homeDir, roles, cluster) {
			artifact.Upsert(entry)
		}
	}
	return artifact, nil
}

func invocationsFromSessions(sessions []runrecord.Session) []sessionInvocation {
	byInvocation := make(map[string]*sessionInvocation)
	for _, session := range sessions {
		key := invocationKey(session.Iteration, session.Role)
		current := byInvocation[key]
		if current == nil {
			grouped := sessionInvocation{iteration: session.Iteration, role: session.Role}
			byInvocation[key] = &grouped
			current = &grouped
		}
		appendSession(&current.sessions, session.SessionID)
	}

	result := make([]sessionInvocation, 0, len(byInvocation))
	for _, invocation := range byInvocation {
		result = append(result, *invocation)
	}
	sortInvocations(result)
	return result
}

func mergeArtifactInvocations(record runrecord.Record, invocations []sessionInvocation) []sessionInvocation {
	knownInvocations := make(map[string]struct{}, len(invocations))
	for _, invocation := range invocations {
		knownInvocations[invocationKey(invocation.iteration, invocation.role)] = struct{}{}
	}
	for _, artifact := range record.Artifacts {
		if isInvocationArtifact(artifact) && !artifact.Standalone {
			invocations = appendInvocationIfMissing(invocations, knownInvocations, artifact.Iteration, artifact.Role)
		}
	}
	if record.Metadata.Kind == runrecord.Review && hasStandaloneReviewOutput(record.Artifacts) {
		invocations = appendInvocationIfMissing(invocations, knownInvocations, 0, "review")
	}
	sortInvocations(invocations)
	return invocations
}

func isInvocationArtifact(artifact runrecord.Artifact) bool {
	switch artifact.Kind {
	case runrecord.ImplementFeed, runrecord.ReviewFeed, runrecord.ImplementTranscript, runrecord.ReviewTranscript:
		return true
	default:
		return false
	}
}

func hasStandaloneReviewOutput(artifacts []runrecord.Artifact) bool {
	for _, artifact := range artifacts {
		isReviewOutput := artifact.Kind == runrecord.ReviewFeed || artifact.Kind == runrecord.ReviewTranscript
		if artifact.Standalone && isReviewOutput {
			return true
		}
	}
	return false
}

func appendInvocationIfMissing(
	invocations []sessionInvocation,
	known map[string]struct{},
	iteration int,
	role string,
) []sessionInvocation {
	key := invocationKey(iteration, role)
	if _, exists := known[key]; exists {
		return invocations
	}
	known[key] = struct{}{}
	return append(invocations, sessionInvocation{iteration: iteration, role: role})
}

func invocationKey(iteration int, role string) string {
	return fmt.Sprintf("%d\x00%s", iteration, role)
}

func appendSession(sessions *[]string, sessionID string) {
	for _, existing := range *sessions {
		if existing == sessionID {
			return
		}
	}
	*sessions = append(*sessions, sessionID)
}

func sortInvocations(invocations []sessionInvocation) {
	sort.Slice(invocations, func(i, j int) bool {
		if invocations[i].iteration != invocations[j].iteration {
			return invocations[i].iteration < invocations[j].iteration
		}
		return invocations[i].role < invocations[j].role
	})
}

func clusterInvocations(invocations []sessionInvocation) []usageCluster {
	clusters := make([]usageCluster, 0, len(invocations))
	for _, invocation := range invocations {
		matching := -1
		for index := range clusters {
			if clusters[index].role == invocation.role && clusterSharesSession(clusters[index], invocation) {
				matching = index
				break
			}
		}
		if matching == -1 {
			clusters = append(clusters, usageCluster{role: invocation.role, invocations: []sessionInvocation{invocation}})
			continue
		}
		clusters[matching].invocations = append(clusters[matching].invocations, invocation)
		// A resumed session can connect two clusters transitively. Collapse any
		// newly connected clusters so one role is never silently duplicated.
		for index := len(clusters) - 1; index > matching; index-- {
			if clusters[index].role != clusters[matching].role || !clustersShareSessions(clusters[index], clusters[matching]) {
				continue
			}
			clusters[matching].invocations = append(clusters[matching].invocations, clusters[index].invocations...)
			clusters = append(clusters[:index], clusters[index+1:]...)
		}
	}
	for index := range clusters {
		sortInvocations(clusters[index].invocations)
	}
	sort.Slice(clusters, func(i, j int) bool {
		left, right := clusters[i].invocations[0], clusters[j].invocations[0]
		if left.iteration != right.iteration {
			return left.iteration < right.iteration
		}
		return clusters[i].role < clusters[j].role
	})
	return clusters
}

func clusterSharesSession(cluster usageCluster, invocation sessionInvocation) bool {
	for _, existing := range cluster.invocations {
		if invocationsShareSession(existing, invocation) {
			return true
		}
	}
	return false
}

func clustersShareSessions(left, right usageCluster) bool {
	for _, invocation := range left.invocations {
		if clusterSharesSession(right, invocation) {
			return true
		}
	}
	return false
}

func invocationsShareSession(left, right sessionInvocation) bool {
	for _, leftID := range left.sessions {
		for _, rightID := range right.sessions {
			if leftID == rightID {
				return true
			}
		}
	}
	return false
}

func recomputeCluster(record runrecord.Record, projectRoot, homeDir string, roles map[string]RoleMetadata, cluster usageCluster) []Entry {
	metadata := roleMetadata(roles, cluster.role)
	allSessions := clusterSessionIDs(cluster)
	collected, err := collectRoleUsage(projectRoot, homeDir, allSessions, metadata)
	if err != nil {
		return []Entry{unavailableEntry(cluster, metadata)}
	}

	if collected.harness == "claude" && len(cluster.invocations) > 1 {
		if entries, ok := splitClaudeCluster(record, projectRoot, homeDir, metadata, collected.metrics, cluster); ok {
			return entries
		}
		return []Entry{combinedEntry(cluster, metadata, collected.harness, collected.metrics)}
	}
	if len(cluster.invocations) > 1 {
		return []Entry{combinedEntry(cluster, metadata, collected.harness, collected.metrics)}
	}
	invocation := cluster.invocations[0]
	return []Entry{trackedEntry(invocation.iteration, invocation.role, metadata, collected.harness, collected.metrics)}
}

type collectedRoleUsage struct {
	harness string
	metrics Metrics
}

func collectRoleUsage(projectRoot, homeDir string, sessionIDs []string, metadata RoleMetadata) (collectedRoleUsage, error) {
	if len(sessionIDs) == 0 {
		return collectedRoleUsage{}, errors.New("no session ids")
	}
	claude, claudeErr := CollectClaudeAll(projectRoot, homeDir, sessionIDs)
	codex, codexErr := CollectCodex(homeDir, sessionIDs)

	configured := strings.ToLower(strings.TrimSpace(metadata.Harness))
	if configured == "claude" && claudeErr == nil {
		return collectedRoleUsage{harness: "claude", metrics: claude}, nil
	}
	if configured == "codex" && codexErr == nil {
		return collectedRoleUsage{harness: "codex", metrics: codex}, nil
	}
	if claudeErr == nil && codexErr != nil {
		return collectedRoleUsage{harness: "claude", metrics: claude}, nil
	}
	if codexErr == nil && claudeErr != nil {
		return collectedRoleUsage{harness: "codex", metrics: codex}, nil
	}
	if claudeErr == nil && codexErr == nil && (configured == "claude" || configured == "codex") {
		if configured == "claude" {
			return collectedRoleUsage{harness: "claude", metrics: claude}, nil
		}
		return collectedRoleUsage{harness: "codex", metrics: codex}, nil
	}
	return collectedRoleUsage{}, errors.New("usage reader could not identify a single harness")
}

func splitClaudeCluster(record runrecord.Record, projectRoot, homeDir string, metadata RoleMetadata, full Metrics, cluster usageCluster) ([]Entry, bool) {
	ends := make(map[int]time.Time, len(cluster.invocations))
	for _, invocation := range cluster.invocations {
		end, ok := roleArtifactModTime(record, invocation.iteration, cluster.role)
		if !ok {
			return nil, false
		}
		ends[invocation.iteration] = end
	}

	var previous time.Time
	entries := make([]Entry, 0, len(cluster.invocations))
	var combined Metrics
	for index, invocation := range cluster.invocations {
		start := time.Unix(0, 0).UTC()
		if index > 0 {
			start = previous.Add(time.Nanosecond)
		}
		end := ends[invocation.iteration]
		if !end.After(start) {
			return nil, false
		}
		metrics, err := CollectClaude(projectRoot, homeDir, invocation.sessions, start, end)
		if err != nil {
			return nil, false
		}
		combined = addClaudeMetrics(combined, metrics)
		entries = append(entries, trackedEntry(invocation.iteration, invocation.role, metadata, "claude", metrics))
		previous = end
	}
	if !sameClaudeMetrics(combined, full) {
		return nil, false
	}
	return entries, true
}

func roleArtifactModTime(record runrecord.Record, iteration int, role string) (time.Time, bool) {
	if modifiedAt, found := matchingRoleArtifactModTime(record.Artifacts, iteration, role); found {
		return modifiedAt, true
	}
	return fallbackFeedModTime(record.Artifacts, iteration)
}

func matchingRoleArtifactModTime(artifacts []runrecord.Artifact, iteration int, role string) (time.Time, bool) {
	var latest time.Time
	found := false
	for _, artifact := range artifacts {
		if !isRoleArtifact(artifact, iteration, role) {
			continue
		}
		if !found || artifact.ModTime.After(latest) {
			latest = artifact.ModTime
			found = true
		}
	}
	return latest, found
}

func fallbackFeedModTime(artifacts []runrecord.Artifact, iteration int) (time.Time, bool) {
	// Older runs may have retained only a generic feed. It is safe to use it
	// as an approximate boundary only when the role-specific artifacts do not
	// exist at all.
	if iteration == 0 {
		return time.Time{}, false
	}
	var latest time.Time
	found := false
	for _, artifact := range artifacts {
		if artifact.Iteration != iteration || !isFeedArtifact(artifact) {
			continue
		}
		if !found || artifact.ModTime.After(latest) {
			latest = artifact.ModTime
			found = true
		}
	}
	return latest, found
}

func isRoleArtifact(artifact runrecord.Artifact, iteration int, role string) bool {
	if iteration == 0 {
		return artifact.Standalone && artifact.Role == role && isFeedOrTranscript(artifact)
	}
	if artifact.Iteration != iteration || artifact.Role != role {
		return false
	}
	if artifact.Standalone {
		return false
	}
	return isFeedOrTranscript(artifact)
}

func isFeedOrTranscript(artifact runrecord.Artifact) bool {
	return isFeedArtifact(artifact) || artifact.Kind == runrecord.ImplementTranscript || artifact.Kind == runrecord.ReviewTranscript
}

func isFeedArtifact(artifact runrecord.Artifact) bool {
	return artifact.Kind == runrecord.ImplementFeed || artifact.Kind == runrecord.ReviewFeed
}

func clusterSessionIDs(cluster usageCluster) []string {
	var sessions []string
	for _, invocation := range cluster.invocations {
		for _, sessionID := range invocation.sessions {
			appendSession(&sessions, sessionID)
		}
	}
	return sessions
}

func roleMetadata(roles map[string]RoleMetadata, role string) RoleMetadata {
	metadata := roles[role]
	if strings.TrimSpace(metadata.Harness) == "" {
		metadata.Harness = "unknown"
	}
	if strings.TrimSpace(metadata.Model) == "" {
		metadata.Model = "unknown"
	}
	return metadata
}

func trackedEntry(iteration int, role string, metadata RoleMetadata, harness string, metrics Metrics) Entry {
	return Entry{
		Iteration: iteration,
		Role:      role,
		Harness:   harness,
		Model:     metadata.Model,
		Tracked:   true,
		Metrics:   &metrics,
	}
}

func unavailableEntry(cluster usageCluster, metadata RoleMetadata) Entry {
	role := cluster.role
	if len(cluster.invocations) > 1 {
		role = combinedRole(cluster)
	}
	return Entry{
		Iteration: cluster.invocations[0].iteration,
		Role:      role,
		Harness:   metadata.Harness,
		Model:     metadata.Model,
		Tracked:   false,
		Reason:    "usage unavailable",
	}
}

func combinedEntry(cluster usageCluster, metadata RoleMetadata, harness string, metrics Metrics) Entry {
	if harness == "" {
		harness = metadata.Harness
	}
	return Entry{
		Iteration: cluster.invocations[0].iteration,
		Role:      combinedRole(cluster),
		Harness:   harness,
		Model:     metadata.Model,
		Tracked:   true,
		Metrics:   &metrics,
	}
}

func combinedRole(cluster usageCluster) string {
	iterations := make([]int, 0, len(cluster.invocations))
	for _, invocation := range cluster.invocations {
		iterations = append(iterations, invocation.iteration)
	}
	return fmt.Sprintf("%s, iterations %s combined", cluster.role, formatIterationSpan(iterations))
}

func formatIterationSpan(iterations []int) string {
	if len(iterations) == 0 {
		return "unknown"
	}
	contiguous := true
	for index := 1; index < len(iterations); index++ {
		if iterations[index] != iterations[index-1]+1 {
			contiguous = false
			break
		}
	}
	if contiguous && len(iterations) > 1 {
		return fmt.Sprintf("%d–%d", iterations[0], iterations[len(iterations)-1])
	}
	parts := make([]string, len(iterations))
	for index, iteration := range iterations {
		parts[index] = strconv.Itoa(iteration)
	}
	return strings.Join(parts, ", ")
}

func addClaudeMetrics(total, metrics Metrics) Metrics {
	total.InputTokens += metrics.InputTokens
	total.OutputTokens += metrics.OutputTokens
	total.CacheWriteTokens += metrics.CacheWriteTokens
	total.CacheReadTokens += metrics.CacheReadTokens
	total.WeightedEstimate = float64(total.InputTokens) +
		1.25*float64(total.CacheWriteTokens) +
		0.1*float64(total.CacheReadTokens) +
		float64(total.OutputTokens)
	return total
}

func sameClaudeMetrics(left, right Metrics) bool {
	return left.InputTokens == right.InputTokens &&
		left.OutputTokens == right.OutputTokens &&
		left.CacheWriteTokens == right.CacheWriteTokens &&
		left.CacheReadTokens == right.CacheReadTokens &&
		left.WeightedEstimate == right.WeightedEstimate
}
