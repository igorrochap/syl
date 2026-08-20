package usage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
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

type runMetadata struct {
	kind string
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

	metadata := readRunMetadata(filepath.Join(runDir, "metadata.txt"))
	invocations := readSessionInvocations(filepath.Join(runDir, "sessions.txt"))
	invocations = mergeArtifactInvocations(runDir, metadata, invocations)
	clusters := clusterInvocations(invocations)

	artifact := NewArtifact()
	for _, cluster := range clusters {
		for _, entry := range recomputeCluster(runDir, projectRoot, homeDir, roles, cluster) {
			artifact.Upsert(entry)
		}
	}
	return artifact, nil
}

func readRunMetadata(path string) runMetadata {
	contents, err := os.ReadFile(path)
	if err != nil {
		return runMetadata{}
	}
	var metadata runMetadata
	for _, line := range strings.Split(string(contents), "\n") {
		key, _, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "branch":
			metadata.kind = "implement"
		case "ticket":
			metadata.kind = "review"
		}
	}
	return metadata
}

func readSessionInvocations(path string) []sessionInvocation {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	byInvocation := make(map[string]*sessionInvocation)
	for _, line := range strings.Split(string(contents), "\n") {
		invocation, ok := parseSessionLine(line)
		if !ok {
			continue
		}
		key := invocationKey(invocation.iteration, invocation.role)
		current := byInvocation[key]
		if current == nil {
			grouped := invocation
			grouped.sessions = nil
			byInvocation[key] = &grouped
			current = &grouped
		}
		appendSession(&current.sessions, invocation.sessions[0])
	}

	result := make([]sessionInvocation, 0, len(byInvocation))
	for _, invocation := range byInvocation {
		result = append(result, *invocation)
	}
	sortInvocations(result)
	return result
}

func parseSessionLine(line string) (sessionInvocation, bool) {
	left, sessionID, ok := strings.Cut(strings.TrimSpace(line), ":")
	if !ok {
		return sessionInvocation{}, false
	}
	fields := strings.Fields(left)
	if len(fields) != 3 || !strings.EqualFold(fields[0], "iteration") {
		return sessionInvocation{}, false
	}
	iteration, err := strconv.Atoi(fields[1])
	if err != nil || iteration < 0 {
		return sessionInvocation{}, false
	}
	role := strings.TrimSpace(fields[2])
	sessionID = strings.TrimSpace(sessionID)
	if role == "" || sessionID == "" {
		return sessionInvocation{}, false
	}
	return sessionInvocation{iteration: iteration, role: role, sessions: []string{sessionID}}, true
}

func mergeArtifactInvocations(runDir string, metadata runMetadata, invocations []sessionInvocation) []sessionInvocation {
	byInvocation := make(map[string]int, len(invocations))
	for index, invocation := range invocations {
		byInvocation[invocationKey(invocation.iteration, invocation.role)] = index
	}

	add := func(iteration int, role string) {
		key := invocationKey(iteration, role)
		if _, exists := byInvocation[key]; exists {
			return
		}
		byInvocation[key] = len(invocations)
		invocations = append(invocations, sessionInvocation{iteration: iteration, role: role})
	}

	paths, _ := filepath.Glob(filepath.Join(runDir, "iteration-*-*.feed"))
	transcripts, _ := filepath.Glob(filepath.Join(runDir, "iteration-*-*.transcript"))
	paths = append(paths, transcripts...)
	for _, path := range paths {
		iteration, role, ok := parseIterationArtifact(filepath.Base(path))
		if ok {
			add(iteration, role)
		}
	}

	if metadata.kind == "review" {
		for _, name := range []string{"review.feed", "review.transcript"} {
			if _, err := os.Stat(filepath.Join(runDir, name)); err == nil {
				add(0, "review")
			}
		}
	}
	sortInvocations(invocations)
	return invocations
}

func parseIterationArtifact(name string) (int, string, bool) {
	if !strings.HasPrefix(name, "iteration-") {
		return 0, "", false
	}
	withoutPrefix := strings.TrimPrefix(name, "iteration-")
	dash := strings.IndexByte(withoutPrefix, '-')
	if dash < 1 {
		return 0, "", false
	}
	iteration, err := strconv.Atoi(withoutPrefix[:dash])
	if err != nil || iteration < 0 {
		return 0, "", false
	}
	roleAndExtension := withoutPrefix[dash+1:]
	dot := strings.LastIndexByte(roleAndExtension, '.')
	if dot < 1 {
		return 0, "", false
	}
	role := roleAndExtension[:dot]
	extension := roleAndExtension[dot+1:]
	if (extension != "feed" && extension != "transcript") || (role != "implement" && role != "review") {
		return 0, "", false
	}
	return iteration, role, true
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

func recomputeCluster(runDir, projectRoot, homeDir string, roles map[string]RoleMetadata, cluster usageCluster) []Entry {
	metadata := roleMetadata(roles, cluster.role)
	allSessions := clusterSessionIDs(cluster)
	collected, err := collectRoleUsage(projectRoot, homeDir, allSessions, metadata)
	if err != nil {
		return []Entry{unavailableEntry(cluster, metadata)}
	}

	if collected.harness == "claude" && len(cluster.invocations) > 1 {
		if entries, ok := splitClaudeCluster(runDir, projectRoot, homeDir, metadata, collected.metrics, cluster); ok {
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

func splitClaudeCluster(runDir, projectRoot, homeDir string, metadata RoleMetadata, full Metrics, cluster usageCluster) ([]Entry, bool) {
	ends := make(map[int]time.Time, len(cluster.invocations))
	for _, invocation := range cluster.invocations {
		end, ok := roleArtifactModTime(runDir, invocation.iteration, cluster.role)
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

func roleArtifactModTime(runDir string, iteration int, role string) (time.Time, bool) {
	patterns := []string{}
	if iteration == 0 {
		patterns = append(patterns, filepath.Join(runDir, role+".feed"), filepath.Join(runDir, role+".transcript"))
	} else {
		prefix := filepath.Join(runDir, fmt.Sprintf("iteration-%02d-%s", iteration, role))
		patterns = append(patterns, prefix+".feed", prefix+".transcript")
	}
	var latest time.Time
	found := false
	for _, pattern := range patterns {
		info, err := os.Stat(pattern)
		if err != nil {
			continue
		}
		if !found || info.ModTime().After(latest) {
			latest = info.ModTime()
			found = true
		}
	}
	if found {
		return latest, true
	}

	// Older runs may have retained only a generic feed. It is safe to use it
	// as an approximate boundary only when the role-specific artifacts do not
	// exist at all.
	if iteration == 0 {
		return time.Time{}, false
	}
	paths, _ := filepath.Glob(filepath.Join(runDir, fmt.Sprintf("iteration-%02d-*.feed", iteration)))
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if !found || info.ModTime().After(latest) {
			latest = info.ModTime()
			found = true
		}
	}
	return latest, found
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
