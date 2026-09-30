package pageview

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/igorrochap/syl/internal/readmodel"
	"github.com/igorrochap/syl/internal/runrecord"
)

// Overview is the display-ready content for the Overview page.
type Overview struct {
	Chrome         PageChrome
	AwaitingAnswer []OverviewRun
	LiveRuns       []OverviewRun
	Projects       []OverviewProject
	TotalRunCount  int
	ProjectCount   int
}

// OverviewRun is one Run shown on the Overview page.
type OverviewRun struct {
	ProjectName          string
	ProjectPath          string
	RunDir               string
	Ticket               string
	Kind                 string
	Status               RunStatus
	Activity             string
	Question             string
	Iteration            string
	Harness              string
	WorkRoot             string
	PID                  int
	Started              string
	Hostname             string
	ShowRecordedActivity bool
	ShowRemoteHostname   bool
	Dismissible          bool
}

// OverviewProject is one Project card on the Overview page.
type OverviewProject struct {
	Name        string
	Path        string
	Health      string
	HealthClass string
	Summary     string
	Missing     bool
}

// BuildOverview builds display-ready values for the Overview page.
func BuildOverview(model readmodel.Overview, now time.Time) Overview {
	page := Overview{
		Chrome:         overviewChrome(),
		AwaitingAnswer: make([]OverviewRun, 0, len(model.AwaitingAnswer)),
		LiveRuns:       make([]OverviewRun, 0, len(model.LiveRuns)),
		Projects:       make([]OverviewProject, 0, len(model.Projects)),
		TotalRunCount:  len(model.AwaitingAnswer) + len(model.LiveRuns),
		ProjectCount:   len(model.Projects),
	}
	localHostname := localHostname()
	for _, run := range model.AwaitingAnswer {
		page.AwaitingAnswer = append(page.AwaitingAnswer, buildOverviewRun(run, now, localHostname))
	}
	for _, run := range model.LiveRuns {
		page.LiveRuns = append(page.LiveRuns, buildOverviewRun(run, now, localHostname))
	}
	for _, project := range model.Projects {
		page.Projects = append(page.Projects, buildOverviewProject(project))
	}
	return page
}

func overviewChrome() PageChrome {
	return PageChrome{
		Title:          "syl ui — Overview",
		ServerDotClass: "live-dot",
		ServerState:    "refreshing every 3s",
		CopyMode:       "path",
	}
}

func localHostname() string {
	hostname, err := os.Hostname()
	if err != nil {
		return ""
	}
	return hostname
}

func buildOverviewRun(run readmodel.Run, now time.Time, localHostname string) OverviewRun {
	status := PresentRunStatus(run.Status)
	return OverviewRun{
		ProjectName:          run.ProjectName,
		ProjectPath:          run.ProjectPath,
		RunDir:               run.RunDir,
		Ticket:               FormatTicketReference(run.TicketRef, TicketAsWritten),
		Kind:                 FormatRunKind(run.Kind, RunKindAsRun),
		Status:               status,
		Activity:             run.Activity,
		Question:             run.Question,
		Iteration:            FormatIteration(run.Iteration, run.MaxIterations, IterationFraction),
		Harness:              FormatHarnessAndModel(run.Harness, run.Model),
		WorkRoot:             run.WorkRoot,
		PID:                  run.PID,
		Started:              FormatRelativeStartTime(run.StartedAt, now),
		Hostname:             run.Hostname,
		ShowRecordedActivity: status.ShowRecordedActivity && run.Activity != "",
		ShowRemoteHostname:   run.Hostname != "" && run.Hostname != localHostname,
		Dismissible:          status.Dismissible,
	}
}

func buildOverviewProject(project readmodel.Project) OverviewProject {
	return OverviewProject{
		Name:        project.Name,
		Path:        project.Path,
		Health:      string(project.Health),
		HealthClass: HealthClass(project.Health),
		Summary:     formatProjectSummary(project),
		Missing:     project.Health == readmodel.HealthMissing,
	}
}

func formatProjectSummary(project readmodel.Project) string {
	prefix := ""
	switch project.Health {
	case readmodel.HealthInvalid:
		prefix = "config fails to load"
	case readmodel.HealthUninitialized:
		prefix = "no .syl/config.toml · run syl init"
	case readmodel.HealthMissing:
		prefix = "directory not found"
	}
	if prefix == "" {
		return formatLiveRunSummary(project)
	}
	return prefix + " · " + formatLiveRunSummary(project)
}

func formatLiveRunSummary(project readmodel.Project) string {
	parts := make([]string, 0, 2)
	if project.LiveRunCount > 0 {
		parts = append(parts, strconv.Itoa(project.LiveRunCount)+" live")
	}
	if project.InterruptedCount > 0 {
		parts = append(parts, strconv.Itoa(project.InterruptedCount)+" interrupted")
	}
	if len(parts) == 0 {
		return "No live Runs"
	}
	return strings.Join(parts, " · ")
}

// Project is the display-ready content for one Project page.
type Project struct {
	Chrome       PageChrome
	Name         string
	Path         string
	Health       string
	HealthClass  string
	IssueTracker string
	ReviewLog    string
	FirstSeen    string
	Runs         []ProjectRun
	RunCount     int
}

// ProjectRun is one history row on a Project page.
type ProjectRun struct {
	RunDir    string
	Ticket    string
	Kind      string
	Status    RunStatus
	Activity  string
	Iteration string
	Verdict   string
	Started   string
	Duration  string
	Tokens    string
}

// BuildProject builds display-ready values for a Project page.
func BuildProject(model readmodel.ProjectPage, now time.Time) Project {
	page := Project{
		Chrome:       projectChrome(),
		Name:         model.Project.Name,
		Path:         model.Project.Path,
		Health:       string(model.Project.Health),
		HealthClass:  HealthClass(model.Project.Health),
		IssueTracker: displayOrDash(string(model.Project.IssueTracker)),
		ReviewLog:    displayOrDash(string(model.Project.ReviewLog)),
		FirstSeen:    FormatAbsoluteTime(model.Project.FirstSeen, AbsoluteDate),
		Runs:         make([]ProjectRun, 0, len(model.Runs)),
		RunCount:     len(model.Runs),
	}
	for _, run := range model.Runs {
		page.Runs = append(page.Runs, buildProjectRun(run, now))
	}
	return page
}

func projectChrome() PageChrome {
	return PageChrome{
		Title:          "syl ui — Project runs",
		ServerDotClass: "live-dot",
		ServerState:    "refreshing every 3s",
	}
}

func buildProjectRun(run readmodel.HistoryRun, now time.Time) ProjectRun {
	duration := "—"
	if run.DurationKnown {
		duration = FormatDuration(run.Duration)
	}
	tokens := "—"
	if run.TokensKnown {
		tokens = FormatTokenCount(run.TotalTokens)
	}
	verdict := run.Verdict
	if verdict == "" {
		verdict = "—"
	}
	return ProjectRun{
		RunDir:    run.RunDir,
		Ticket:    FormatTicketReference(run.TicketRef, TicketByNumber),
		Kind:      FormatRunKind(run.Kind, RunKindAsDash),
		Status:    PresentRunStatus(run.Status),
		Activity:  run.Activity,
		Iteration: FormatIteration(run.Iteration, run.MaxIterations, IterationOptionalMaximum),
		Verdict:   verdict,
		Started:   FormatRelativeStartTime(run.StartedAt, now),
		Duration:  duration,
		Tokens:    tokens,
	}
}

// Run is the display-ready content for one Run page.
type Run struct {
	Chrome        PageChrome
	Run           RunDetail
	Metadata      RunMetadata
	Summary       string
	SummaryExists bool
	DiffStat      string
	DiffStatKnown bool
	Iterations    []RunIteration
	Usage         []RoleUsage
	Resume        []string
}

// RunDetail contains display-ready values shown in the Run page header.
type RunDetail struct {
	RunDir      string
	ProjectName string
	ProjectPath string
	Ticket      string
	Branch      string
	Kind        string
	Status      RunStatus
	Activity    string
	Iteration   string
	Started     string
	Ended       string
	Duration    string
	Tokens      string
	Refreshing  bool
}

// PageChrome contains the display values shared by a page's outer layout.
type PageChrome struct {
	Title          string
	ServerDotClass string
	ServerState    string
	MainClass      string
	ConfigStyles   bool
	CopyMode       string
}

// RunMetadata contains display-ready metadata and session labels.
type RunMetadata struct {
	Branch       string
	BranchPoint  string
	WorkRoot     string
	RunDirectory string
	Sessions     []string
}

// RunIteration is one formatted timeline entry.
type RunIteration struct {
	Number         int
	Activity       string
	Roles          []RunRole
	HasVerdict     bool
	VerdictStatus  string
	VerdictClass   string
	VerdictSummary string
	BlockingCount  int
	NitCount       int
	FindingGroups  []RunFindingGroup
}

// RunRole is one role invocation shown in an iteration.
type RunRole struct {
	Role      string
	Harness   string
	Artifacts []RunArtifact
}

// RunArtifact is a display label and raw name for a Run artifact link.
type RunArtifact struct {
	Name  string
	Label string
}

// RunFindingGroup groups findings by their severity.
type RunFindingGroup struct {
	Label    string
	Count    int
	Findings []RunFinding
}

// RunFinding contains the location and issue shown by the Run page.
type RunFinding struct {
	Location string
	Issue    string
}

// RoleUsage is one formatted usage row.
type RoleUsage struct {
	Role         string
	InputTokens  string
	OutputTokens string
	CachedTokens string
}

// BuildRun builds display-ready values for a Run page.
func BuildRun(model readmodel.RunPage) Run {
	page := Run{
		Run:           buildRunDetail(model.Run),
		Metadata:      buildRunMetadata(model.Metadata),
		Summary:       model.Summary,
		SummaryExists: model.SummaryExists,
		DiffStat:      model.DiffStat,
		DiffStatKnown: model.DiffStatKnown,
		Iterations:    make([]RunIteration, 0, len(model.Iterations)),
		Usage:         make([]RoleUsage, 0, len(model.Usage)),
		Resume:        make([]string, 0, len(model.Resume)),
	}
	page.Chrome = runChrome(page.Run)
	if page.SummaryExists && page.Summary == "" {
		page.Summary = "—"
	}
	if page.DiffStatKnown && page.DiffStat == "" {
		page.DiffStat = "—"
	}
	for _, iteration := range model.Iterations {
		page.Iterations = append(page.Iterations, buildRunIteration(iteration))
	}
	for _, usage := range model.Usage {
		page.Usage = append(page.Usage, buildRoleUsage(usage))
	}
	for _, command := range model.Resume {
		page.Resume = append(page.Resume, command.Command)
	}
	return page
}

func runChrome(run RunDetail) PageChrome {
	page := PageChrome{
		Title:          "syl ui — Run " + run.Ticket,
		ServerDotClass: "live-dot",
		ServerState:    "refreshing every 3s",
		MainClass:      "run-main-page",
		CopyMode:       "command",
	}
	if run.Refreshing {
		return page
	}
	page.ServerDotClass = "ended-dot"
	page.ServerState = "run ended, not refreshing"
	return page
}

// ConfigPageChrome returns the outer layout values for the Config page.
func ConfigPageChrome() PageChrome {
	return PageChrome{
		Title:          "syl ui — Project config",
		ServerDotClass: "live-dot",
		ServerState:    "refreshing every 3s",
		MainClass:      "config-main",
		ConfigStyles:   true,
	}
}

func buildRunDetail(run readmodel.RunDetail) RunDetail {
	ended := "—"
	if run.EndedAt != nil {
		ended = FormatAbsoluteTime(*run.EndedAt, AbsoluteDateTime)
	}
	duration := "—"
	if run.DurationKnown {
		duration = FormatDuration(run.Duration)
	}
	tokens := "—"
	if run.TokensKnown {
		tokens = FormatTokenCount(run.TotalTokens)
	}
	return RunDetail{
		RunDir:      run.RunDir,
		ProjectName: run.ProjectName,
		ProjectPath: run.ProjectPath,
		Ticket:      FormatTicketReference(run.TicketRef, TicketByNumber),
		Branch:      displayOrDash(run.Branch),
		Kind:        FormatRunKind(run.Kind, RunKindAsDash),
		Status:      PresentRunStatus(run.Status),
		Activity:    run.Activity,
		Iteration:   FormatIteration(run.Iteration, run.MaxIterations, IterationRunSummary),
		Started:     FormatAbsoluteTime(run.StartedAt, AbsoluteDateTime),
		Ended:       ended,
		Duration:    duration,
		Tokens:      tokens,
		Refreshing:  run.Refreshing,
	}
}

func buildRunMetadata(metadata readmodel.RunMetadata) RunMetadata {
	page := RunMetadata{
		Branch:       displayOrDash(metadata.Branch),
		BranchPoint:  displayOrDash(metadata.BranchPoint),
		WorkRoot:     displayOrDash(metadata.WorkRoot),
		RunDirectory: displayOrDash(metadata.RunDirectory),
		Sessions:     make([]string, 0, len(metadata.Sessions)),
	}
	for _, session := range metadata.Sessions {
		page.Sessions = append(page.Sessions,
			"it "+strconv.Itoa(session.Iteration)+" "+session.Role+": "+session.SessionID)
	}
	return page
}

func buildRunIteration(iteration readmodel.Iteration) RunIteration {
	page := RunIteration{
		Number:         iteration.Number,
		Activity:       iteration.Activity,
		Roles:          make([]RunRole, 0, len(iteration.Roles)),
		HasVerdict:     iteration.HasVerdict,
		VerdictStatus:  string(iteration.Verdict.Status),
		VerdictClass:   verdictClass(string(iteration.Verdict.Status)),
		VerdictSummary: iteration.Verdict.Summary,
		BlockingCount:  iteration.BlockingCount,
		NitCount:       iteration.NitCount,
		FindingGroups:  make([]RunFindingGroup, 0, len(iteration.FindingGroups)),
	}
	for _, role := range iteration.Roles {
		page.Roles = append(page.Roles, buildRunRole(role))
	}
	for _, group := range iteration.FindingGroups {
		page.FindingGroups = append(page.FindingGroups, buildRunFindingGroup(group))
	}
	return page
}

func buildRunRole(role readmodel.RoleRun) RunRole {
	page := RunRole{
		Role:      role.Role,
		Harness:   FormatHarnessAndModel(role.Harness, role.Model),
		Artifacts: make([]RunArtifact, 0, len(role.Artifacts)),
	}
	for _, artifact := range role.Artifacts {
		page.Artifacts = append(page.Artifacts, RunArtifact{
			Name:  artifact.Name,
			Label: runrecord.ArtifactLabel(artifact.Name),
		})
	}
	return page
}

func buildRunFindingGroup(group readmodel.FindingGroup) RunFindingGroup {
	page := RunFindingGroup{
		Label:    group.Label,
		Count:    group.Count,
		Findings: make([]RunFinding, 0, len(group.Findings)),
	}
	for _, finding := range group.Findings {
		page.Findings = append(page.Findings, RunFinding{Location: finding.Location, Issue: finding.Issue})
	}
	return page
}

func buildRoleUsage(usage readmodel.RoleUsage) RoleUsage {
	input := "—"
	output := "—"
	cached := "—"
	if usage.Known {
		input = FormatTokenCount(usage.InputTokens)
		output = FormatTokenCount(usage.OutputTokens)
		cached = FormatTokenCount(usage.CachedTokens)
	}
	return RoleUsage{Role: usage.Role, InputTokens: input, OutputTokens: output, CachedTokens: cached}
}

func displayOrDash(value string) string {
	if value == "" {
		return "—"
	}
	return value
}

func verdictClass(status string) string {
	switch status {
	case "approve":
		return "green"
	case "revise":
		return "plum"
	default:
		return "neutral"
	}
}
