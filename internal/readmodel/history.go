package readmodel

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/igorrochap/syl/internal/runrecord"
	"github.com/igorrochap/syl/internal/sylhome"
	"github.com/igorrochap/syl/internal/usage"
)

// ErrProjectNotFound means the requested path is not in the Project registry.
var ErrProjectNotFound = errors.New("project is not registered")

// FileSystem is the part of the filesystem needed to read Run history.
type FileSystem interface {
	ReadDir(name string) ([]os.DirEntry, error)
	ReadFile(name string) ([]byte, error)
	Stat(name string) (os.FileInfo, error)
	EvalSymlinks(path string) (string, error)
}

type osFileSystem struct{}

func (osFileSystem) ReadDir(name string) ([]os.DirEntry, error) {
	return os.ReadDir(name)
}

func (osFileSystem) ReadFile(name string) ([]byte, error) {
	return os.ReadFile(name)
}

func (osFileSystem) Stat(name string) (os.FileInfo, error) {
	return os.Stat(name)
}

func (osFileSystem) EvalSymlinks(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}

// Reader builds UI read models and retains immutable Run history in memory.
type Reader struct {
	sylHome sylhome.Dir
	files   FileSystem
	runs    *runrecord.Reader

	mu      sync.Mutex
	history map[string]projectHistory
}

// NewReader constructs a reader backed by the operating system filesystem.
func NewReader(sylHome sylhome.Dir) *Reader {
	return NewReaderWithFileSystem(sylHome, osFileSystem{})
}

// NewReaderWithFileSystem constructs a reader with an injected history filesystem.
func NewReaderWithFileSystem(sylHome sylhome.Dir, files FileSystem) *Reader {
	return NewReaderWithFileSystemAndProcessLiveness(sylHome, files, nil)
}

// NewReaderWithProcessLiveness constructs a reader with an injected process check.
func NewReaderWithProcessLiveness(
	sylHome sylhome.Dir,
	processAlive func(int) bool,
) *Reader {
	return NewReaderWithFileSystemAndProcessLiveness(sylHome, osFileSystem{}, processAlive)
}

// NewReaderWithFileSystemAndProcessLiveness constructs a reader with injected dependencies.
func NewReaderWithFileSystemAndProcessLiveness(
	sylHome sylhome.Dir,
	files FileSystem,
	processAlive func(int) bool,
) *Reader {
	if files == nil {
		files = osFileSystem{}
	}
	return &Reader{
		sylHome: sylHome,
		files:   files,
		runs:    runrecord.NewReaderWithProcessLiveness(files, processAlive),
		history: make(map[string]projectHistory),
	}
}

// ProjectPage is the complete read model for a Project page.
type ProjectPage struct {
	Project Project
	Runs    []HistoryRun
}

// HistoryRun is one Run found in a Project's .syl/runs directory.
type HistoryRun struct {
	RunDir        string
	TicketRef     string
	Kind          runrecord.Kind
	Status        runrecord.ObservedStatus
	Activity      string
	Iteration     int
	MaxIterations int
	Verdict       string
	StartedAt     time.Time
	Duration      time.Duration
	DurationKnown bool
	TotalTokens   int64
	TokensKnown   bool
}

// ReadProject reads one registered Project and only that Project's Run history.
func (reader *Reader) ReadProject(projectPath string) (ProjectPage, error) {
	entries, err := reader.sylHome.Projects()
	if err != nil {
		return ProjectPage{}, fmt.Errorf("read registered Projects: %w", err)
	}

	path := filepath.Clean(projectPath)
	entry, found := registeredProject(entries, path)
	if !found {
		return ProjectPage{}, fmt.Errorf("%w: %s", ErrProjectNotFound, path)
	}

	project, _, _ := inspectProject(path, entry)
	liveRunCount, err := reader.readLiveRunCount(path)
	if err != nil {
		return ProjectPage{}, err
	}
	project.LiveRunCount = liveRunCount
	page := ProjectPage{Project: project}
	if project.Health == HealthMissing {
		return page, nil
	}
	page.Runs, err = reader.readProjectHistory(path)
	if err != nil {
		return ProjectPage{}, err
	}
	return page, nil
}

func (reader *Reader) readLiveRunCount(projectPath string) (int, error) {
	runs, err := reader.sylHome.LiveRuns()
	if err != nil {
		return 0, fmt.Errorf("read live-run markers: %w", err)
	}
	canonicalProjectPath, err := filepath.EvalSymlinks(projectPath)
	if err != nil {
		canonicalProjectPath = projectPath
	}
	localHost := currentHostname()
	count := 0
	for _, run := range runs {
		if filepath.Clean(run.ProjectPath) != filepath.Clean(canonicalProjectPath) {
			continue
		}
		status := reader.runs.ObserveStatus(run.RunDir, run.Host, localHost)
		if status != runrecord.ObservedRunning {
			continue
		}
		count++
	}
	return count, nil
}

func (reader *Reader) markerHostForRun(runDir string) string {
	runs, err := reader.sylHome.LiveRuns()
	if err != nil {
		return ""
	}
	resolvedRunDir, err := filepath.EvalSymlinks(runDir)
	if err == nil {
		runDir = resolvedRunDir
	}
	for _, run := range runs {
		markerRunDir := run.RunDir
		resolvedMarkerRunDir, err := filepath.EvalSymlinks(markerRunDir)
		if err == nil {
			markerRunDir = resolvedMarkerRunDir
		}
		if filepath.Clean(markerRunDir) == filepath.Clean(runDir) {
			return run.Host
		}
	}
	return ""
}

// ReadProject reads one registered Project and only that Project's Run history.
func ReadProject(sylHome sylhome.Dir, projectPath string) (ProjectPage, error) {
	return NewReader(sylHome).ReadProject(projectPath)
}

type projectHistory struct {
	runs map[string]cachedHistoryRun
}

type cachedHistoryRun struct {
	run      HistoryRun
	name     string
	terminal bool
}

func registeredProject(entries []sylhome.RegisteredProject, path string) (sylhome.RegisteredProject, bool) {
	for _, entry := range entries {
		if filepath.Clean(entry.Path) == path {
			return entry, true
		}
	}
	return sylhome.RegisteredProject{}, false
}

func (reader *Reader) readProjectHistory(projectPath string) ([]HistoryRun, error) {
	runsPath := runrecord.RunsDirectory(projectPath)
	entries, err := reader.files.ReadDir(runsPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Run history %s: %w", runsPath, err)
	}

	reader.mu.Lock()
	defer reader.mu.Unlock()

	previous := reader.history[projectPath]
	if previous.runs == nil {
		previous.runs = make(map[string]cachedHistoryRun)
	}
	current := make(map[string]cachedHistoryRun, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		cached, exists := previous.runs[name]
		if !exists {
			cached = reader.readNewHistoryRun(runsPath, name)
		} else if !cached.terminal {
			cached = reader.refreshRunningHistoryRun(runsPath, cached)
		}
		current[name] = cached
	}
	reader.history[projectPath] = projectHistory{runs: current}

	result := make([]HistoryRun, 0, len(current))
	for _, cached := range current {
		result = append(result, cached.run)
	}
	sort.SliceStable(result, func(left, right int) bool {
		return historyRunSortKey(result[left]) > historyRunSortKey(result[right])
	})
	return result, nil
}

func (reader *Reader) readNewHistoryRun(runsPath, name string) cachedHistoryRun {
	runDir := filepath.Join(runsPath, name)
	record, _ := reader.runs.Read(runDir)
	metadata := record.Metadata
	timestamp := runrecord.DirectoryTimestamp(name)
	base := HistoryRun{
		RunDir:    runDir,
		TicketRef: metadata.TicketRef,
		Kind:      metadata.Kind,
		StartedAt: timestamp,
	}
	cached := cachedHistoryRun{name: name, run: base}

	if record.HasState {
		cached.run = reader.historyRunFromState(base, record.State)
		cached.terminal = record.State.Status != runrecord.Running
		if cached.terminal {
			reader.readFinalHistoryArtifacts(record, &cached)
		}
		reader.readHistoryUsage(record, &cached.run)
		return cached
	}

	reader.readLegacyHistoryRun(record, &cached)
	reader.readHistoryUsage(record, &cached.run)
	return cached
}

func (reader *Reader) refreshRunningHistoryRun(runsPath string, cached cachedHistoryRun) cachedHistoryRun {
	runDir := filepath.Join(runsPath, cached.name)
	record, err := reader.runs.Read(runDir)
	if err != nil {
		return cached
	}
	if !record.HasState {
		return cached
	}
	state := record.State
	cached.run = reader.historyRunFromState(cached.run, state)
	cached.run.TotalTokens = 0
	cached.run.TokensKnown = false
	reader.readHistoryUsage(record, &cached.run)
	if state.Status == runrecord.Running {
		return cached
	}
	cached.terminal = true
	reader.readFinalHistoryArtifacts(record, &cached)
	return cached
}

func (reader *Reader) historyRunFromState(run HistoryRun, state runrecord.State) HistoryRun {
	if state.TicketRef != "" {
		run.TicketRef = state.TicketRef
	}
	if state.Kind != "" {
		run.Kind = state.Kind
	}
	run.Status = reader.runs.ObserveStatus(run.RunDir, reader.markerHostForRun(run.RunDir), currentHostname())
	run.Activity = string(state.Activity)
	run.Iteration = state.Iteration
	run.MaxIterations = state.MaxIterations
	run.StartedAt = state.StartedAt
	if run.StartedAt.IsZero() {
		run.StartedAt = runrecord.DirectoryTimestamp(filepath.Base(run.RunDir))
	}
	if state.Status == runrecord.Running {
		run.Duration, run.DurationKnown = historyDuration(run.StartedAt, nil)
	} else if state.EndedAt != nil {
		run.Duration, run.DurationKnown = historyDuration(run.StartedAt, state.EndedAt)
	}
	return run
}

func (reader *Reader) readLegacyHistoryRun(record runrecord.Record, cached *cachedHistoryRun) {
	if cached.run.TicketRef == "" && cached.run.Kind == runrecord.Implement {
		if ticketNumber := runrecord.LegacyTicketNumber(cached.name); ticketNumber != "" {
			cached.run.TicketRef = "#" + ticketNumber
		}
	}
	cached.run.Status = reader.runs.ObserveStatus(
		cached.run.RunDir, reader.markerHostForRun(cached.run.RunDir), currentHostname(),
	)
	if cached.run.Status == runrecord.ObservedCompleted {
		cached.run.Verdict = record.Summary.FinalVerdict
		cached.run.Iteration = record.Summary.Iterations
	}
	if cached.run.Iteration == 0 {
		cached.run.Iteration = legacyIteration(cached.run.Kind, record)
	}
	cached.terminal = true
}

func (reader *Reader) readFinalHistoryArtifacts(record runrecord.Record, cached *cachedHistoryRun) {
	if record.SummaryExists {
		if cached.run.Verdict == "" {
			cached.run.Verdict = record.Summary.FinalVerdict
		}
		if cached.run.Iteration == 0 {
			cached.run.Iteration = record.Summary.Iterations
		}
	}
	if cached.run.Verdict == "" {
		cached.run.Verdict = record.VerdictText[1]
	}
}

func (reader *Reader) readHistoryUsage(record runrecord.Record, run *HistoryRun) {
	if !record.UsageExists {
		return
	}
	artifact, err := usage.ParseArtifact(runrecord.ArtifactName(runrecord.UsageFile, 0), record.UsageContents)
	if err != nil {
		return
	}
	run.TokensKnown = true
	for _, entry := range artifact.Entries {
		if entry.Tracked && entry.Metrics != nil {
			run.TotalTokens += entry.Metrics.TotalTokens
		}
	}
}

func legacyIteration(kind runrecord.Kind, record runrecord.Record) int {
	if kind == runrecord.Review {
		return 1
	}
	return record.HighestIteration
}

func historyRunSortKey(run HistoryRun) string {
	return runrecord.DirectorySortKey(filepath.Base(run.RunDir))
}

func historyDuration(startedAt time.Time, endedAt *time.Time) (time.Duration, bool) {
	if startedAt.IsZero() {
		return 0, false
	}
	if endedAt != nil {
		return endedAt.Sub(startedAt), true
	}
	return time.Since(startedAt), true
}
