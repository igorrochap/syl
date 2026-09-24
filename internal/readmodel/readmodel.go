// Package readmodel builds the disk-backed values displayed by syl ui.
package readmodel

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/igorrochap/syl/internal/config"
	"github.com/igorrochap/syl/internal/registry"
	"github.com/igorrochap/syl/internal/runmarker"
	"github.com/igorrochap/syl/internal/runstate"
)

// Health describes whether a registered Project can be used right now.
type Health string

const (
	// HealthOK means the Project directory and configuration are usable.
	HealthOK Health = "ok"
	// HealthMissing means the Project directory no longer exists.
	HealthMissing Health = "missing"
	// HealthUninitialized means the Project has no syl configuration.
	HealthUninitialized Health = "uninitialized"
	// HealthInvalid means the Project configuration cannot be loaded.
	HealthInvalid Health = "invalid"
)

var (
	// ErrRunMarkerNotFound means no live-run marker points at the requested Run.
	ErrRunMarkerNotFound = errors.New("live-run marker not found")
	// ErrRunNotInterrupted means the requested Run is not safe to dismiss.
	ErrRunNotInterrupted = errors.New("run is not interrupted")
)

// Overview is the complete read model for the Overview page.
type Overview struct {
	AwaitingAnswer []Run
	LiveRuns       []Run
	Projects       []Project
}

// Project is a registered Project and its derived health.
type Project struct {
	Name             string
	Path             string
	Health           Health
	IssueTracker     config.Tracker
	ReviewLog        config.Tracker
	FirstSeen        time.Time
	LiveRunCount     int
	InterruptedCount int
}

// Run is a Run found through a Live-run marker.
type Run struct {
	ProjectName   string
	ProjectPath   string
	RunDir        string
	TicketRef     string
	Kind          runstate.Kind
	Status        runstate.Status
	Activity      string
	Question      string
	Iteration     int
	MaxIterations int
	Harness       string
	Model         string
	WorkRoot      string
	Hostname      string
	PID           int
	StartedAt     time.Time
	Interrupted   bool
	Unknown       bool
}

// ReadOverview reads the registry, live markers, and referenced Run state.
// It does not write state or inspect historical Run directories.
func ReadOverview(sylHome string) (Overview, error) {
	return NewReader(sylHome).ReadOverview()
}

// Forget removes a Project from the registry without touching its directory.
func Forget(sylHome, projectPath string) error {
	return registry.Forget(sylHome, projectPath)
}

// Dismiss removes an orphaned live-run marker without touching the Run.
func Dismiss(sylHome, runDir string) error {
	if strings.TrimSpace(runDir) == "" {
		return errors.New("run directory is required")
	}
	resolvedRunDir, err := filepath.EvalSymlinks(runDir)
	if err != nil {
		return fmt.Errorf("resolve Run directory: %w", err)
	}

	pointers, err := runmarker.List(sylHome)
	if err != nil {
		return fmt.Errorf("read live-run markers: %w", err)
	}
	for _, pointer := range pointers {
		if filepath.Clean(pointer.RunDir) != filepath.Clean(resolvedRunDir) {
			continue
		}
		return dismissMarker(sylHome, pointer)
	}
	return fmt.Errorf("%w: %s", ErrRunMarkerNotFound, runDir)
}

func dismissMarker(sylHome string, pointer runmarker.Pointer) error {
	state, err := runstate.Read(runstate.Path(pointer.RunDir))
	if err != nil {
		return fmt.Errorf("read Run state: %w", err)
	}
	if !isInterrupted(pointer, state) {
		return fmt.Errorf("%w: %s", ErrRunNotInterrupted, pointer.RunDir)
	}
	if err := runmarker.Remove(sylHome, pointer); err != nil {
		return fmt.Errorf("remove live-run marker: %w", err)
	}
	return nil
}

func isInterrupted(pointer runmarker.Pointer, state runstate.State) bool {
	if state.Status != runstate.Running {
		return false
	}
	if !isLocalHost(pointer.Host, state.Hostname, currentHostname()) {
		return false
	}
	return !processIsAlive(state.PID)
}

// ReadOverview reads the registry, live markers, and referenced Run state.
// It does not write state or inspect historical Run directories.
func (reader *Reader) ReadOverview() (Overview, error) {
	entries, err := registry.List(reader.sylHome)
	if err != nil {
		return Overview{}, fmt.Errorf("read registered Projects: %w", err)
	}

	projects, projectConfigs := inspectProjects(entries)
	pointers, err := runmarker.List(reader.sylHome)
	if err != nil {
		return Overview{}, fmt.Errorf("read live-run markers: %w", err)
	}

	localHost := currentHostname()
	overview := Overview{Projects: projects}
	for _, pointer := range pointers {
		run := buildRun(pointer, projectConfigs, localHost)
		isAwaitingAnswer := run.Status == runstate.Running && run.Activity == string(runstate.AwaitingAnswer)
		if isAwaitingAnswer && !run.Interrupted {
			overview.AwaitingAnswer = append(overview.AwaitingAnswer, run)
			continue
		}
		overview.LiveRuns = append(overview.LiveRuns, run)
	}

	sortRuns(overview.AwaitingAnswer)
	sortRuns(overview.LiveRuns)
	addRunCounts(&overview)
	return overview, nil
}

type projectRecord struct {
	project       Project
	configuration config.Config
	configLoaded  bool
}

func inspectProjects(entries []registry.Entry) ([]Project, map[string]projectRecord) {
	projects := make([]Project, 0, len(entries))
	records := make(map[string]projectRecord, len(entries))
	for _, entry := range entries {
		path := filepath.Clean(entry.Path)
		project, configuration, loaded := inspectProject(path, entry)
		projects = append(projects, project)
		records[path] = projectRecord{
			project:       project,
			configuration: configuration,
			configLoaded:  loaded,
		}
	}
	sort.Slice(projects, func(left, right int) bool { return projects[left].Path < projects[right].Path })
	return projects, records
}

func inspectProject(path string, entry registry.Entry) (Project, config.Config, bool) {
	project := Project{Name: filepath.Base(path), Path: path, Health: HealthInvalid, FirstSeen: entry.FirstSeen}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		project.Health = HealthMissing
		return project, config.Config{}, false
	}
	if err != nil || !info.IsDir() {
		return project, config.Config{}, false
	}

	if _, err := os.Stat(config.Path(path)); errors.Is(err, os.ErrNotExist) {
		project.Health = HealthUninitialized
		return project, config.Config{}, false
	}
	configuration, err := config.Load(path)
	if err != nil {
		return project, config.Config{}, false
	}
	project.Health = HealthOK
	project.IssueTracker = configuration.Tracker.Issues
	project.ReviewLog = configuration.Tracker.Reviews
	return project, configuration, true
}

func buildRun(pointer runmarker.Pointer, projects map[string]projectRecord, localHost string) Run {
	projectPath := filepath.Clean(pointer.ProjectPath)
	record := projects[projectPath]
	run := Run{
		ProjectName: filepath.Base(projectPath),
		ProjectPath: projectPath,
		RunDir:      pointer.RunDir,
		TicketRef:   pointer.TicketRef,
		PID:         pointer.PID,
		Hostname:    pointer.Host,
	}
	if record.project.Name != "" {
		run.ProjectName = record.project.Name
	}

	metadata := readMetadata(filepath.Join(pointer.RunDir, "metadata.txt"))
	state, err := runstate.Read(runstate.Path(pointer.RunDir))
	if err != nil {
		run.Unknown = true
		run.Activity = "unknown"
		run.Harness = metadata.implementerHarness
		run.WorkRoot = metadata.workRoot
		return run
	}

	run.Kind = state.Kind
	run.Status = state.Status
	run.Activity = string(state.Activity)
	run.Question = state.Question
	run.Iteration = state.Iteration
	run.MaxIterations = state.MaxIterations
	run.PID = state.PID
	run.StartedAt = state.StartedAt
	if pointer.Host == "" && state.Hostname != "" {
		run.Hostname = state.Hostname
	}
	run.WorkRoot = metadata.workRoot
	run.Harness, run.Model = roleConfiguration(state, metadata, record)
	if state.Status == runstate.Running && isLocalHost(pointer.Host, state.Hostname, localHost) {
		run.Interrupted = !processIsAlive(state.PID)
	}
	return run
}

func roleConfiguration(state runstate.State, metadata runMetadata, project projectRecord) (string, string) {
	harness := metadata.harnessFor(state.Kind)
	model := ""
	if !project.configLoaded {
		return harness, model
	}

	role := project.configuration.Roles.Review
	if state.Kind == runstate.Implement && state.Activity != runstate.Reviewing {
		role = project.configuration.Roles.Implement
	}
	if harness == "" {
		harness = string(role.Harness)
	}
	return harness, role.Model
}

func isLocalHost(markerHost, stateHost, localHost string) bool {
	host := markerHost
	if host == "" {
		host = stateHost
	}
	return localHost != "" && host != "" && host == localHost
}

func processIsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EPERM)
}

func currentHostname() string {
	host, err := os.Hostname()
	if err != nil {
		return ""
	}
	return host
}

type runMetadata struct {
	workRoot           string
	implementerHarness string
	reviewerHarness    string
	branch             string
	branchPoint        string
	ticketRef          string
	kind               runstate.Kind
}

func readMetadata(path string) runMetadata {
	contents, err := os.ReadFile(path)
	if err != nil {
		return runMetadata{}
	}
	metadata := runMetadata{}
	for _, line := range strings.Split(string(contents), "\n") {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "work root":
			metadata.workRoot = value
		case "implementer harness":
			metadata.implementerHarness = value
		case "reviewer harness":
			metadata.reviewerHarness = value
		}
	}
	return metadata
}

func (m runMetadata) harnessFor(kind runstate.Kind) string {
	if kind == runstate.Review {
		return m.reviewerHarness
	}
	return m.implementerHarness
}

func sortRuns(runs []Run) {
	sort.Slice(runs, func(left, right int) bool {
		if runs[left].StartedAt.Equal(runs[right].StartedAt) {
			return runs[left].RunDir < runs[right].RunDir
		}
		return runs[left].StartedAt.After(runs[right].StartedAt)
	})
}

func addRunCounts(overview *Overview) {
	counts := make(map[string]*Project, len(overview.Projects))
	for index := range overview.Projects {
		counts[overview.Projects[index].Path] = &overview.Projects[index]
	}
	for _, run := range append(append([]Run{}, overview.AwaitingAnswer...), overview.LiveRuns...) {
		project := counts[run.ProjectPath]
		if project == nil {
			continue
		}
		if run.Interrupted {
			project.InterruptedCount++
			continue
		}
		project.LiveRunCount++
	}
}
