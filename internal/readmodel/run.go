package readmodel

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/runrecord"
	"github.com/igorrochap/syl/internal/sylhome"
	"github.com/igorrochap/syl/internal/usage"
	"github.com/igorrochap/syl/internal/verdict"
)

// ErrRunNotFound means the requested Run directory cannot be read.
var ErrRunNotFound = errors.New("run not found")

// RunPage is the complete read model for one Run page.
type RunPage struct {
	Run           RunDetail
	Metadata      RunMetadata
	Summary       string
	SummaryExists bool
	DiffStat      string
	DiffStatKnown bool
	Iterations    []Iteration
	Usage         []RoleUsage
	Resume        []ResumeCommand
}

// RunDetail contains the values shown in the Run page header.
type RunDetail struct {
	RunDir        string
	ProjectName   string
	ProjectPath   string
	TicketRef     string
	Branch        string
	Kind          runrecord.Kind
	Status        string
	Activity      string
	Iteration     int
	MaxIterations int
	StartedAt     time.Time
	EndedAt       *time.Time
	Duration      time.Duration
	DurationKnown bool
	TotalTokens   int64
	TokensKnown   bool
	Interrupted   bool
	Refreshing    bool
}

// RunMetadata contains the metadata and recorded session IDs for a Run.
type RunMetadata struct {
	Branch       string
	BranchPoint  string
	WorkRoot     string
	RunDirectory string
	Sessions     []Session
}

// Session is one role session recorded in sessions.txt.
type Session struct {
	Iteration int
	Role      string
	SessionID string
}

// Iteration is one timeline entry, including roles and its review verdict.
type Iteration struct {
	Number        int
	Activity      string
	Roles         []RoleRun
	Verdict       verdict.Verdict
	HasVerdict    bool
	BlockingCount int
	NitCount      int
	FindingGroups []FindingGroup
}

// RoleRun is one role invocation shown in an iteration.
type RoleRun struct {
	Role      string
	Harness   string
	Model     string
	Artifacts []Artifact
}

// Artifact is a raw Run artifact link.
type Artifact struct {
	Name string
}

// FindingGroup groups findings by their severity.
type FindingGroup struct {
	Kind     verdict.FindingKind
	Label    string
	Count    int
	Findings []verdict.Finding
}

// RoleUsage is the aggregate token usage for one role.
type RoleUsage struct {
	Role         string
	Harness      string
	Model        string
	InputTokens  int64
	OutputTokens int64
	CachedTokens int64
	Known        bool
}

// ResumeCommand is a command for re-entering a recorded role session.
type ResumeCommand struct {
	Role    string
	Command string
}

// ReadRun reads one Run directory and all artifacts displayed by its page.
func (reader *Reader) ReadRun(runDir string) (RunPage, error) {
	path, err := filepath.Abs(filepath.Clean(runDir))
	if err != nil {
		return RunPage{}, fmt.Errorf("resolve Run directory: %w", err)
	}
	record, err := reader.runs.Read(path)
	if err != nil {
		return RunPage{}, fmt.Errorf("%w: %s: %v", ErrRunNotFound, path, err)
	}

	metadata := record.Metadata
	sessions := readRunSessions(record.Sessions)
	artifact := readRunUsage(record)
	detail := buildRunDetail(path, metadata, record.SummaryExists, record.State, record.HasState)
	addRunUsageTotals(&detail, artifact)
	page := RunPage{
		Run:           detail,
		Metadata:      buildRunMetadata(path, metadata, sessions),
		Summary:       record.Summary.Text,
		SummaryExists: record.SummaryExists,
		DiffStat:      record.Summary.DiffStat,
		DiffStatKnown: record.SummaryExists,
	}
	page.Iterations = reader.buildIterations(record, detail, sessions, artifact, metadata)
	page.Usage = buildRoleUsage(artifact, page.Iterations, metadata)
	page.Resume = buildResumeCommands(detail.TicketRef, sessions)
	return page, nil
}

// ReadRun reads one Run directory using a fresh operating-system reader.
func ReadRun(sylHome sylhome.Dir, runDir string) (RunPage, error) {
	return NewReader(sylHome).ReadRun(runDir)
}

func readRunSessions(records []runrecord.Session) []Session {
	sessions := make([]Session, 0, len(records))
	for _, record := range records {
		sessions = append(sessions, Session{
			Iteration: normalizeIteration(record.Iteration), Role: record.Role, SessionID: record.SessionID,
		})
	}
	sort.SliceStable(sessions, func(left, right int) bool {
		if sessions[left].Iteration != sessions[right].Iteration {
			return sessions[left].Iteration < sessions[right].Iteration
		}
		return sessions[left].Role < sessions[right].Role
	})
	return sessions
}

func readRunUsage(record runrecord.Record) usage.Artifact {
	if !record.UsageExists {
		return usage.Artifact{}
	}
	artifact, err := usage.ParseArtifact(runrecord.ArtifactName(runrecord.UsageFile, 0), record.UsageContents)
	if err != nil {
		return usage.Artifact{}
	}
	return artifact
}

func buildRunDetail(runDir string, metadata runrecord.Metadata, summaryExists bool, state runrecord.State, hasState bool) RunDetail {
	projectPath := filepath.Dir(filepath.Dir(filepath.Dir(runDir)))
	detail := RunDetail{
		RunDir:      runDir,
		ProjectPath: projectPath,
		ProjectName: filepath.Base(projectPath),
		TicketRef:   metadata.TicketRef,
		Branch:      metadata.Branch,
		Kind:        metadata.Kind,
		Status:      legacyRunStatus(summaryExists),
		StartedAt:   runrecord.DirectoryTimestamp(filepath.Base(runDir)),
	}
	detail = applyLegacyRunTicket(detail, runDir)
	if !hasState {
		return detail
	}
	return applyRunState(detail, state, runDir)
}

func applyLegacyRunTicket(detail RunDetail, runDir string) RunDetail {
	if detail.Kind == runrecord.Implement && detail.TicketRef == "" {
		if number := runrecord.LegacyTicketNumber(filepath.Base(runDir)); number != "" {
			detail.TicketRef = "#" + number
		}
	}
	return detail
}

func applyRunState(detail RunDetail, state runrecord.State, runDir string) RunDetail {
	applyRunStateValues(&detail, state, runDir)
	applyRunStateTiming(&detail, state)
	applyRunStateStatus(&detail, state)
	detail.Refreshing = state.Status == runrecord.Running && !detail.Interrupted
	return detail
}

func applyRunStateValues(detail *RunDetail, state runrecord.State, runDir string) {
	if state.TicketRef != "" {
		detail.TicketRef = state.TicketRef
	}
	if state.Kind != "" {
		detail.Kind = state.Kind
	}
	detail.Status = string(state.Status)
	detail.Activity = string(state.Activity)
	detail.Iteration = state.Iteration
	detail.MaxIterations = state.MaxIterations
	detail.StartedAt = state.StartedAt
	if detail.StartedAt.IsZero() {
		detail.StartedAt = runrecord.DirectoryTimestamp(filepath.Base(runDir))
	}
}

func applyRunStateTiming(detail *RunDetail, state runrecord.State) {
	if state.EndedAt != nil {
		endedAt := *state.EndedAt
		detail.EndedAt = &endedAt
		detail.Duration, detail.DurationKnown = historyDuration(detail.StartedAt, detail.EndedAt)
	} else if state.Status == runrecord.Running {
		detail.Duration, detail.DurationKnown = historyDuration(detail.StartedAt, nil)
	}
}

func applyRunStateStatus(detail *RunDetail, state runrecord.State) {
	if state.Status == runrecord.Running && isLocalHost("", state.Hostname, currentHostname()) {
		detail.Interrupted = !processIsAlive(state.PID)
	}
	if detail.Interrupted {
		detail.Status = "Interrupted"
	}
}

func legacyRunStatus(summaryExists bool) string {
	if summaryExists {
		return "completed"
	}
	return "unknown"
}

func buildRunMetadata(runDir string, metadata runrecord.Metadata, sessions []Session) RunMetadata {
	return RunMetadata{
		Branch: metadata.Branch, BranchPoint: metadata.BranchPoint, WorkRoot: metadata.WorkRoot,
		RunDirectory: runrecord.RelativeDirectory(filepath.Base(runDir)), Sessions: sessions,
	}
}

func (reader *Reader) buildIterations(
	record runrecord.Record,
	detail RunDetail,
	sessions []Session,
	artifact usage.Artifact,
	metadata runrecord.Metadata,
) []Iteration {
	index := collectIterationEntries(record.Artifacts, record.Verdicts)
	for _, session := range sessions {
		index.addRole(session.Iteration, session.Role)
	}
	for _, entry := range artifact.Entries {
		index.addRole(normalizeIteration(entry.Iteration), entry.Role)
	}
	index.addDetail(detail)
	index.addSummary(record.Summary, record.SummaryExists)
	index.addLegacyReview(metadata)
	return reader.buildIterationResults(index, detail, artifact, metadata)
}

type iterationIndex struct {
	iterations     map[int]map[string]map[runrecord.ArtifactKind]string
	verdictPresent map[int]bool
	verdicts       map[int]verdict.Verdict
}

func newIterationIndex() iterationIndex {
	return iterationIndex{
		iterations:     make(map[int]map[string]map[runrecord.ArtifactKind]string),
		verdictPresent: make(map[int]bool),
		verdicts:       make(map[int]verdict.Verdict),
	}
}

func collectIterationEntries(entries []runrecord.Artifact, verdicts map[int]verdict.Verdict) iterationIndex {
	index := newIterationIndex()
	for _, entry := range entries {
		if entry.Kind == runrecord.ImplementFeed || entry.Kind == runrecord.ImplementTranscript ||
			entry.Kind == runrecord.ReviewDiff || entry.Kind == runrecord.ReviewFeed ||
			entry.Kind == runrecord.ReviewTranscript {
			index.addRole(entry.Iteration, entry.Role)
			index.iterations[entry.Iteration][entry.Role][entry.Kind] = entry.Name
			continue
		}
		if entry.Kind == runrecord.VerdictFile {
			index.addIteration(entry.Iteration)
			index.verdictPresent[entry.Iteration] = true
			if parsed, ok := verdicts[entry.Iteration]; ok {
				index.verdicts[entry.Iteration] = parsed
			}
		}
	}
	return index
}

func (index *iterationIndex) addIteration(number int) {
	if number <= 0 {
		return
	}
	if index.iterations[number] == nil {
		index.iterations[number] = make(map[string]map[runrecord.ArtifactKind]string)
	}
}

func (index *iterationIndex) addRole(number int, role string) {
	index.addIteration(number)
	if role == "" {
		return
	}
	if index.iterations[number][role] == nil {
		index.iterations[number][role] = make(map[runrecord.ArtifactKind]string)
	}
}

func (index *iterationIndex) addDetail(detail RunDetail) {
	if detail.Iteration <= 0 {
		return
	}
	index.addIteration(detail.Iteration)
	role := activityRole(detail.Activity)
	if role == "" {
		return
	}
	index.addRole(detail.Iteration, role)
}

func (index *iterationIndex) addSummary(summary runrecord.Summary, exists bool) {
	if !exists {
		return
	}
	number := summary.Iterations
	if number <= 0 {
		return
	}
	index.addIteration(number)
}

func (index *iterationIndex) addLegacyReview(metadata runrecord.Metadata) {
	if metadata.Kind == runrecord.Review && len(index.iterations) == 0 {
		index.addIteration(1)
	}
}

func (reader *Reader) buildIterationResults(index iterationIndex, detail RunDetail, artifact usage.Artifact, metadata runrecord.Metadata) []Iteration {
	result := make([]Iteration, 0, len(index.iterations))
	for number, roles := range index.iterations {
		item := Iteration{Number: number}
		if detail.Iteration == number && detail.Activity != "" {
			item.Activity = detail.Activity
		}
		roleNames := sortedRoleNames(roles)
		for _, role := range roleNames {
			item.Roles = append(item.Roles, buildRoleRun(number, role, roles[role], artifact, metadata, detail.ProjectPath))
		}
		if index.verdictPresent[number] {
			if parsed, ok := index.verdicts[number]; ok {
				item = addVerdict(item, parsed)
			}
		}
		result = append(result, item)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Number < result[right].Number })
	return result
}

func buildRoleRun(number int, role string, names map[runrecord.ArtifactKind]string, artifact usage.Artifact, metadata runrecord.Metadata, projectPath string) RoleRun {
	roleRun := RoleRun{Role: role, Harness: harnessForRole(metadata, role)}
	for _, entry := range artifact.Entries {
		if normalizeIteration(entry.Iteration) != number || entry.Role != role {
			continue
		}
		if entry.Harness != "" {
			roleRun.Harness = entry.Harness
		}
		if entry.Model != "" {
			roleRun.Model = entry.Model
		}
	}
	if roleRun.Model == "" {
		roleRun.Model = roleModel(projectPath, role)
	}
	for _, name := range artifactNamesForRole(role, names) {
		if name != "" {
			roleRun.Artifacts = append(roleRun.Artifacts, Artifact{Name: name})
		}
	}
	return roleRun
}

func artifactNamesForRole(role string, names map[runrecord.ArtifactKind]string) []string {
	if role == "review" {
		return []string{names[runrecord.ReviewDiff], names[runrecord.ReviewFeed], names[runrecord.ReviewTranscript]}
	}
	return []string{names[runrecord.ImplementFeed], names[runrecord.ImplementTranscript]}
}

func roleModel(projectPath, role string) string {
	project, err := config.Load(projectPath)
	if err != nil {
		return ""
	}
	if role == "review" {
		return project.Roles.Review.Model
	}
	if role == "implement" {
		return project.Roles.Implement.Model
	}
	return ""
}

func sortedRoleNames(roles map[string]map[runrecord.ArtifactKind]string) []string {
	names := make([]string, 0, len(roles))
	for role := range roles {
		names = append(names, role)
	}
	sort.Strings(names)
	return names
}

func addVerdict(iteration Iteration, parsed verdict.Verdict) Iteration {
	iteration.Verdict = parsed
	iteration.HasVerdict = true
	iteration.BlockingCount = countFindings(parsed.Findings, verdict.Blocking)
	iteration.NitCount = countFindings(parsed.Findings, verdict.Nit)
	for _, kind := range []verdict.FindingKind{verdict.Blocking, verdict.Nit} {
		findings := findingsOfKind(parsed.Findings, kind)
		if len(findings) == 0 {
			continue
		}
		iteration.FindingGroups = append(iteration.FindingGroups, FindingGroup{
			Kind: kind, Label: findingGroupLabel(kind), Count: len(findings), Findings: findings,
		})
	}
	return iteration
}

func countFindings(findings []verdict.Finding, kind verdict.FindingKind) int {
	count := 0
	for _, finding := range findings {
		if finding.Kind == kind {
			count++
		}
	}
	return count
}

func findingGroupLabel(kind verdict.FindingKind) string {
	if kind == verdict.Blocking {
		return "Blocking"
	}
	return "Nit"
}

func findingsOfKind(findings []verdict.Finding, kind verdict.FindingKind) []verdict.Finding {
	matching := make([]verdict.Finding, 0)
	for _, finding := range findings {
		if finding.Kind == kind {
			matching = append(matching, finding)
		}
	}
	return matching
}

func buildRoleUsage(artifact usage.Artifact, iterations []Iteration, metadata runrecord.Metadata) []RoleUsage {
	roles := roleUsageFromIterations(iterations)
	mergeUsageEntries(roles, artifact.Entries)
	completeRoleUsage(roles, metadata)
	return sortedRoleUsage(roles)
}

func roleUsageFromIterations(iterations []Iteration) map[string]RoleUsage {
	roles := make(map[string]RoleUsage)
	for _, iteration := range iterations {
		for _, role := range iteration.Roles {
			if _, exists := roles[role.Role]; !exists {
				roles[role.Role] = RoleUsage{Role: role.Role, Harness: role.Harness, Model: role.Model}
			}
		}
	}
	return roles
}

func mergeUsageEntries(roles map[string]RoleUsage, entries []usage.Entry) {
	for _, entry := range entries {
		current := roles[entry.Role]
		if current.Role == "" {
			current = RoleUsage{Role: entry.Role, Harness: entry.Harness, Model: entry.Model}
		}
		if current.Harness == "" {
			current.Harness = entry.Harness
		}
		if current.Model == "" {
			current.Model = entry.Model
		}
		if entry.Tracked && entry.Metrics != nil {
			current.Known = true
			current.InputTokens += entry.Metrics.InputTokens
			current.OutputTokens += entry.Metrics.OutputTokens
			current.CachedTokens += entry.Metrics.CachedInputTokens + entry.Metrics.CacheWriteInputTokens
			current.CachedTokens += entry.Metrics.CacheReadTokens + entry.Metrics.CacheWriteTokens
		}
		roles[entry.Role] = current
	}
}

func completeRoleUsage(roles map[string]RoleUsage, metadata runrecord.Metadata) {
	for role, current := range roles {
		if current.Harness == "" {
			current.Harness = harnessForRole(metadata, role)
		}
		roles[role] = current
	}
}

func sortedRoleUsage(roles map[string]RoleUsage) []RoleUsage {
	result := make([]RoleUsage, 0, len(roles))
	for _, current := range roles {
		result = append(result, current)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Role < result[right].Role })
	return result
}

func addRunUsageTotals(detail *RunDetail, artifact usage.Artifact) {
	for _, entry := range artifact.Entries {
		if !entry.Tracked || entry.Metrics == nil {
			continue
		}
		detail.TokensKnown = true
		if entry.Metrics.TotalTokens > 0 {
			detail.TotalTokens += entry.Metrics.TotalTokens
			continue
		}
		detail.TotalTokens += entry.Metrics.InputTokens + entry.Metrics.OutputTokens
		detail.TotalTokens += entry.Metrics.CachedInputTokens + entry.Metrics.CacheWriteInputTokens
		detail.TotalTokens += entry.Metrics.CacheReadTokens + entry.Metrics.CacheWriteTokens
	}
}

func harnessForRole(metadata runrecord.Metadata, role string) string {
	if role == "review" {
		return metadata.HarnessFor(runrecord.Review)
	}
	return metadata.HarnessFor(runrecord.Implement)
}

func buildResumeCommands(ticket string, sessions []Session) []ResumeCommand {
	roles := make(map[string]bool)
	for _, session := range sessions {
		if session.Role != "" && session.SessionID != "" {
			roles[session.Role] = true
		}
	}
	roleNames := make([]string, 0, len(roles))
	for role := range roles {
		roleNames = append(roleNames, role)
	}
	sort.Strings(roleNames)
	commands := make([]ResumeCommand, 0, len(roleNames))
	for _, role := range roleNames {
		commands = append(commands, ResumeCommand{Role: role, Command: "syl resume " + role + " " + resumeTicket(ticket)})
	}
	return commands
}

func resumeTicket(ticket string) string {
	ticket = strings.TrimSpace(ticket)
	if ticket == "" {
		return "—"
	}
	if strings.HasPrefix(ticket, "#") {
		return ticket
	}
	if number, err := strconv.Atoi(ticket); err == nil && number > 0 {
		return "#" + strconv.Itoa(number)
	}
	return ticket
}

func normalizeIteration(iteration int) int {
	if iteration == 0 {
		return 1
	}
	return iteration
}

func activityRole(activity string) string {
	switch activity {
	case string(runrecord.Implementing):
		return "implement"
	case string(runrecord.Reviewing):
		return "review"
	default:
		return ""
	}
}
