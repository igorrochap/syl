package readmodel

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/runstate"
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
	Kind          runstate.Kind
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
	entries, err := reader.files.ReadDir(path)
	if err != nil {
		return RunPage{}, fmt.Errorf("%w: %s: %v", ErrRunNotFound, path, err)
	}

	metadata := reader.readRunMetadata(path)
	summary, summaryExists := reader.readOptional(filepath.Join(path, "summary.txt"))
	sessions := reader.readRunSessions(path)
	artifact := reader.readRunUsage(path)
	detail := reader.buildRunDetail(path, metadata, summaryExists)
	addRunUsageTotals(&detail, artifact)
	page := RunPage{
		Run:           detail,
		Metadata:      buildRunMetadata(path, metadata, sessions),
		Summary:       summaryValue(string(summary), "Summary:"),
		SummaryExists: summaryExists,
		DiffStat:      summaryDiffStat(string(summary)),
		DiffStatKnown: summaryExists,
	}
	page.Iterations = reader.buildIterations(path, entries, detail, sessions, artifact, metadata)
	page.Usage = buildRoleUsage(artifact, page.Iterations, metadata)
	page.Resume = buildResumeCommands(detail.TicketRef, sessions)
	return page, nil
}

// ReadRun reads one Run directory using a fresh operating-system reader.
func ReadRun(sylHome, runDir string) (RunPage, error) {
	return NewReader(sylHome).ReadRun(runDir)
}

func (reader *Reader) readRunMetadata(runDir string) runMetadata {
	contents, err := reader.files.ReadFile(filepath.Join(runDir, "metadata.txt"))
	if err != nil {
		return runMetadata{}
	}
	return parseRunMetadata(contents)
}

func parseRunMetadata(contents []byte) runMetadata {
	metadata := runMetadata{}
	for _, line := range strings.Split(string(contents), "\n") {
		applyRunMetadataLine(&metadata, line)
	}
	return metadata
}

func applyRunMetadataLine(metadata *runMetadata, line string) {
	if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
		return
	}
	key, value, ok := strings.Cut(line, ":")
	if !ok {
		return
	}
	value = strings.TrimSpace(value)
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "branch":
		metadata.branch = value
		metadata.kind = runstate.Implement
	case "ticket":
		metadata.ticketRef = value
		if metadata.kind == "" {
			metadata.kind = runstate.Review
		}
	case "branch point":
		metadata.branchPoint = value
	case "work root":
		metadata.workRoot = value
	case "implementer harness":
		metadata.implementerHarness = value
	case "reviewer harness":
		metadata.reviewerHarness = value
		if metadata.kind == "" {
			metadata.kind = runstate.Review
		}
	}
}

func (reader *Reader) readRunSessions(runDir string) []Session {
	path := filepath.Join(runDir, "sessions.txt")
	contents, err := reader.files.ReadFile(path)
	if err != nil {
		return nil
	}
	records := usage.ParseSessionRecords(contents)
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

func (reader *Reader) readRunUsage(runDir string) usage.Artifact {
	path := filepath.Join(runDir, "usage.json")
	contents, err := reader.files.ReadFile(path)
	if err != nil {
		return usage.Artifact{}
	}
	artifact, err := usage.ParseArtifact(path, contents)
	if err != nil {
		return usage.Artifact{}
	}
	return artifact
}

func (reader *Reader) buildRunDetail(runDir string, metadata runMetadata, summaryExists bool) RunDetail {
	projectPath := filepath.Dir(filepath.Dir(filepath.Dir(runDir)))
	detail := RunDetail{
		RunDir:      runDir,
		ProjectPath: projectPath,
		ProjectName: filepath.Base(projectPath),
		TicketRef:   metadata.ticketRef,
		Branch:      metadata.branch,
		Kind:        metadata.kind,
		Status:      legacyRunStatus(summaryExists),
		StartedAt:   parseRunDirectoryTimestamp(filepath.Base(runDir)),
	}
	if detail.Kind == runstate.Implement && detail.TicketRef == "" {
		if number := legacyTicketNumber(filepath.Base(runDir)); number != "" {
			detail.TicketRef = "#" + number
		}
	}
	contents, err := reader.files.ReadFile(runstate.Path(runDir))
	if err != nil {
		return detail
	}
	state, err := runstate.Parse(runstate.Path(runDir), contents)
	if err != nil {
		return detail
	}
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
		detail.StartedAt = parseRunDirectoryTimestamp(filepath.Base(runDir))
	}
	if state.EndedAt != nil {
		endedAt := *state.EndedAt
		detail.EndedAt = &endedAt
		detail.Duration, detail.DurationKnown = historyDuration(detail.StartedAt, detail.EndedAt)
	} else if state.Status == runstate.Running {
		detail.Duration, detail.DurationKnown = historyDuration(detail.StartedAt, nil)
	}
	if state.Status == runstate.Running && isLocalHost("", state.Hostname, currentHostname()) {
		detail.Interrupted = !processIsAlive(state.PID)
	}
	if detail.Interrupted {
		detail.Status = "Interrupted"
	}
	detail.Refreshing = state.Status == runstate.Running && !detail.Interrupted
	return detail
}

func legacyRunStatus(summaryExists bool) string {
	if summaryExists {
		return "completed"
	}
	return "unknown"
}

func buildRunMetadata(runDir string, metadata runMetadata, sessions []Session) RunMetadata {
	return RunMetadata{
		Branch: metadata.branch, BranchPoint: metadata.branchPoint, WorkRoot: metadata.workRoot,
		RunDirectory: filepath.Join(".syl", "runs", filepath.Base(runDir)), Sessions: sessions,
	}
}

func (reader *Reader) buildIterations(
	runDir string,
	entries []os.DirEntry,
	detail RunDetail,
	sessions []Session,
	artifact usage.Artifact,
	metadata runMetadata,
) []Iteration {
	iterations := make(map[int]map[string]map[string]bool)
	verdictNames := make(map[int]string)
	addIteration := func(number int) {
		if number <= 0 {
			return
		}
		if iterations[number] == nil {
			iterations[number] = make(map[string]map[string]bool)
		}
	}
	addRole := func(number int, role string) {
		addIteration(number)
		if role == "" {
			return
		}
		if iterations[number][role] == nil {
			iterations[number][role] = make(map[string]bool)
		}
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		iteration, role, artifactName, ok := parseRunArtifact(entry.Name())
		if ok {
			addRole(iteration, role)
			iterations[iteration][role][artifactName] = true
			continue
		}
		if parsedIteration, ok := parseRunVerdict(entry.Name()); ok {
			addIteration(parsedIteration)
			verdictNames[parsedIteration] = entry.Name()
		}
	}
	for _, session := range sessions {
		addRole(session.Iteration, session.Role)
	}
	for _, entry := range artifact.Entries {
		addRole(normalizeIteration(entry.Iteration), entry.Role)
	}
	if detail.Iteration > 0 {
		addIteration(detail.Iteration)
		if role := activityRole(detail.Activity); role != "" {
			addRole(detail.Iteration, role)
		}
	}
	if summary, ok := reader.readOptional(filepath.Join(runDir, "summary.txt")); ok {
		if number := summaryIterations(string(summary)); number > 0 {
			addIteration(number)
		}
	}
	if metadata.kind == runstate.Review && len(iterations) == 0 {
		addIteration(1)
	}

	result := make([]Iteration, 0, len(iterations))
	for number, roles := range iterations {
		item := Iteration{Number: number}
		if detail.Iteration == number && detail.Activity != "" {
			item.Activity = detail.Activity
		}
		roleNames := sortedRoleNames(roles)
		for _, role := range roleNames {
			item.Roles = append(item.Roles, buildRoleRun(number, role, roles[role], artifact, metadata, detail.ProjectPath))
		}
		if verdictName := verdictNames[number]; verdictName != "" {
			item = reader.addVerdict(runDir, item, verdictName)
		}
		result = append(result, item)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Number < result[right].Number })
	return result
}

func buildRoleRun(number int, role string, names map[string]bool, artifact usage.Artifact, metadata runMetadata, projectPath string) RoleRun {
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
	for _, name := range []string{
		artifactName(names, ".diff"), artifactName(names, ".feed"), artifactName(names, ".transcript"),
	} {
		if name != "" {
			roleRun.Artifacts = append(roleRun.Artifacts, Artifact{Name: name})
		}
	}
	return roleRun
}

func artifactName(names map[string]bool, extension string) string {
	for name := range names {
		if strings.HasSuffix(name, extension) {
			return name
		}
	}
	return ""
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

func sortedRoleNames(roles map[string]map[string]bool) []string {
	names := make([]string, 0, len(roles))
	for role := range roles {
		names = append(names, role)
	}
	sort.Strings(names)
	return names
}

func (reader *Reader) addVerdict(runDir string, iteration Iteration, name string) Iteration {
	contents, err := reader.files.ReadFile(filepath.Join(runDir, name))
	if err != nil {
		return iteration
	}
	parsed, err := verdict.Parse(string(contents))
	if err != nil {
		return iteration
	}
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

func buildRoleUsage(artifact usage.Artifact, iterations []Iteration, metadata runMetadata) []RoleUsage {
	roles := make(map[string]RoleUsage)
	for _, iteration := range iterations {
		for _, role := range iteration.Roles {
			if _, exists := roles[role.Role]; !exists {
				roles[role.Role] = RoleUsage{Role: role.Role, Harness: role.Harness, Model: role.Model}
			}
		}
	}
	for _, entry := range artifact.Entries {
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
	for role, current := range roles {
		if current.Harness == "" {
			current.Harness = harnessForRole(metadata, role)
		}
		roles[role] = current
	}
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

func harnessForRole(metadata runMetadata, role string) string {
	if role == "review" {
		return metadata.reviewerHarness
	}
	return metadata.implementerHarness
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
	case string(runstate.Implementing):
		return "implement"
	case string(runstate.Reviewing):
		return "review"
	default:
		return ""
	}
}

func parseRunArtifact(name string) (int, string, string, bool) {
	if strings.HasPrefix(name, "review.") {
		if name == "review.diff" || name == "review.feed" || name == "review.transcript" {
			return 1, "review", name, true
		}
		return 0, "", "", false
	}
	if !strings.HasPrefix(name, "iteration-") {
		return 0, "", "", false
	}
	withoutPrefix := strings.TrimPrefix(name, "iteration-")
	dash := strings.IndexByte(withoutPrefix, '-')
	if dash < 1 {
		return 0, "", "", false
	}
	iteration, err := strconv.Atoi(withoutPrefix[:dash])
	if err != nil || iteration <= 0 {
		return 0, "", "", false
	}
	roleAndExtension := withoutPrefix[dash+1:]
	dot := strings.LastIndexByte(roleAndExtension, '.')
	if dot < 1 {
		return 0, "", "", false
	}
	role := roleAndExtension[:dot]
	if role != "implement" && role != "review" {
		return 0, "", "", false
	}
	extension := roleAndExtension[dot+1:]
	if extension != "diff" && extension != "feed" && extension != "transcript" {
		return 0, "", "", false
	}
	return iteration, role, name, true
}

func parseRunVerdict(name string) (int, bool) {
	if name == "verdict.txt" {
		return 1, true
	}
	if !strings.HasPrefix(name, "iteration-") || !strings.HasSuffix(name, "-verdict.txt") {
		return 0, false
	}
	value := strings.TrimSuffix(strings.TrimPrefix(name, "iteration-"), "-verdict.txt")
	iteration, err := strconv.Atoi(value)
	return iteration, err == nil && iteration > 0
}

func summaryDiffStat(contents string) string {
	lines := strings.Split(strings.ReplaceAll(contents, "\r\n", "\n"), "\n")
	for index, line := range lines {
		if !strings.HasPrefix(line, "Diff stat:") {
			continue
		}
		return strings.TrimSpace(strings.Join(lines[index+1:], "\n"))
	}
	return ""
}
