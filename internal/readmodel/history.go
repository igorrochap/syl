package readmodel

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/igorrochap/syl/internal/registry"
	"github.com/igorrochap/syl/internal/runstate"
	"github.com/igorrochap/syl/internal/usage"
)

const runDirectoryTimestampLayout = "20060102T150405.000000000Z"

// ErrProjectNotFound means the requested path is not in the Project registry.
var ErrProjectNotFound = errors.New("project is not registered")

// FileSystem is the part of the filesystem needed to read Run history.
type FileSystem interface {
	ReadDir(name string) ([]os.DirEntry, error)
	ReadFile(name string) ([]byte, error)
}

type osFileSystem struct{}

func (osFileSystem) ReadDir(name string) ([]os.DirEntry, error) {
	return os.ReadDir(name)
}

func (osFileSystem) ReadFile(name string) ([]byte, error) {
	return os.ReadFile(name)
}

// Reader builds UI read models and retains immutable Run history in memory.
type Reader struct {
	sylHome string
	files   FileSystem

	mu      sync.Mutex
	history map[string]projectHistory
}

// NewReader constructs a reader backed by the operating system filesystem.
func NewReader(sylHome string) *Reader {
	return NewReaderWithFileSystem(sylHome, osFileSystem{})
}

// NewReaderWithFileSystem constructs a reader with an injected history filesystem.
func NewReaderWithFileSystem(sylHome string, files FileSystem) *Reader {
	if files == nil {
		files = osFileSystem{}
	}
	return &Reader{
		sylHome: sylHome,
		files:   files,
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
	Kind          runstate.Kind
	Status        string
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
	entries, err := registry.List(reader.sylHome)
	if err != nil {
		return ProjectPage{}, fmt.Errorf("read registered Projects: %w", err)
	}

	path := filepath.Clean(projectPath)
	entry, found := registeredProject(entries, path)
	if !found {
		return ProjectPage{}, fmt.Errorf("%w: %s", ErrProjectNotFound, path)
	}

	project, _, _ := inspectProject(path, entry)
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

// ReadProject reads one registered Project and only that Project's Run history.
func ReadProject(sylHome, projectPath string) (ProjectPage, error) {
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

type historyMetadata struct {
	ticketRef string
	kind      runstate.Kind
}

func registeredProject(entries []registry.Entry, path string) (registry.Entry, bool) {
	for _, entry := range entries {
		if filepath.Clean(entry.Path) == path {
			return entry, true
		}
	}
	return registry.Entry{}, false
}

func (reader *Reader) readProjectHistory(projectPath string) ([]HistoryRun, error) {
	runsPath := filepath.Join(projectPath, ".syl", "runs")
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
	metadata := reader.readHistoryMetadata(filepath.Join(runDir, "metadata.txt"))
	timestamp := parseRunDirectoryTimestamp(name)
	base := HistoryRun{
		RunDir:    runDir,
		TicketRef: metadata.ticketRef,
		Kind:      metadata.kind,
		StartedAt: timestamp,
	}
	cached := cachedHistoryRun{name: name, run: base}

	contents, err := reader.files.ReadFile(runstate.Path(runDir))
	if err == nil {
		state, parseErr := runstate.Parse(runstate.Path(runDir), contents)
		if parseErr == nil {
			cached.run = reader.historyRunFromState(base, state)
			cached.terminal = state.Status != runstate.Running
			if cached.terminal {
				reader.readFinalHistoryArtifacts(runDir, &cached)
			}
			reader.readHistoryUsage(runDir, &cached.run)
			return cached
		}
	}

	reader.readLegacyHistoryRun(runDir, &cached)
	reader.readHistoryUsage(runDir, &cached.run)
	return cached
}

func (reader *Reader) refreshRunningHistoryRun(runsPath string, cached cachedHistoryRun) cachedHistoryRun {
	contents, err := reader.files.ReadFile(runstate.Path(filepath.Join(runsPath, cached.name)))
	if err != nil {
		return cached
	}
	state, err := runstate.Parse(runstate.Path(filepath.Join(runsPath, cached.name)), contents)
	if err != nil {
		return cached
	}
	cached.run = reader.historyRunFromState(cached.run, state)
	runDir := filepath.Join(runsPath, cached.name)
	cached.run.TotalTokens = 0
	cached.run.TokensKnown = false
	reader.readHistoryUsage(runDir, &cached.run)
	if state.Status == runstate.Running {
		return cached
	}
	cached.terminal = true
	reader.readFinalHistoryArtifacts(runDir, &cached)
	return cached
}

func (reader *Reader) historyRunFromState(run HistoryRun, state runstate.State) HistoryRun {
	if state.TicketRef != "" {
		run.TicketRef = state.TicketRef
	}
	if state.Kind != "" {
		run.Kind = state.Kind
	}
	run.Status = string(state.Status)
	run.Activity = string(state.Activity)
	run.Iteration = state.Iteration
	run.MaxIterations = state.MaxIterations
	run.StartedAt = state.StartedAt
	if run.StartedAt.IsZero() {
		run.StartedAt = parseRunDirectoryTimestamp(filepath.Base(run.RunDir))
	}
	if state.Status == runstate.Running {
		run.Duration, run.DurationKnown = historyDuration(run.StartedAt, nil)
	} else if state.EndedAt != nil {
		run.Duration, run.DurationKnown = historyDuration(run.StartedAt, state.EndedAt)
	}
	localRun := state.Hostname == "" || isLocalHost("", state.Hostname, currentHostname())
	if state.Status == runstate.Running && localRun && !processIsAlive(state.PID) {
		run.Status = "Interrupted"
	}
	return run
}

func (reader *Reader) readLegacyHistoryRun(runDir string, cached *cachedHistoryRun) {
	if cached.run.TicketRef == "" && cached.run.Kind == runstate.Implement {
		if ticketNumber := legacyTicketNumber(cached.name); ticketNumber != "" {
			cached.run.TicketRef = "#" + ticketNumber
		}
	}
	summary, summaryExists := reader.readOptional(filepath.Join(runDir, "summary.txt"))
	if summaryExists {
		cached.run.Status = "completed"
		cached.run.Verdict = summaryValue(string(summary), "Final verdict:")
		cached.run.Iteration = summaryIterations(string(summary))
	} else {
		cached.run.Status = "unknown"
	}
	if cached.run.Iteration == 0 {
		cached.run.Iteration = legacyIteration(cached.run.Kind, reader.files, runDir)
	}
	cached.terminal = true
}

func (reader *Reader) readFinalHistoryArtifacts(runDir string, cached *cachedHistoryRun) {
	summary, summaryExists := reader.readOptional(filepath.Join(runDir, "summary.txt"))
	if summaryExists {
		if cached.run.Verdict == "" {
			cached.run.Verdict = summaryValue(string(summary), "Final verdict:")
		}
		if cached.run.Iteration == 0 {
			cached.run.Iteration = summaryIterations(string(summary))
		}
	}
	verdictContents, verdictExists := reader.readOptional(filepath.Join(runDir, "verdict.txt"))
	if verdictExists && cached.run.Verdict == "" {
		cached.run.Verdict = summaryValue(string(verdictContents), "VERDICT:")
	}
}

func (reader *Reader) readHistoryUsage(runDir string, run *HistoryRun) {
	contents, err := reader.files.ReadFile(filepath.Join(runDir, "usage.json"))
	if err != nil {
		return
	}
	artifact, err := usage.ParseArtifact(filepath.Join(runDir, "usage.json"), contents)
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

func (reader *Reader) readHistoryMetadata(path string) historyMetadata {
	contents, err := reader.files.ReadFile(path)
	if err != nil {
		return historyMetadata{}
	}
	var metadata historyMetadata
	for _, line := range strings.Split(string(contents), "\n") {
		applyHistoryMetadataLine(&metadata, line)
	}
	return metadata
}

func applyHistoryMetadataLine(metadata *historyMetadata, line string) {
	if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
		return
	}
	key, value, ok := strings.Cut(line, ":")
	if !ok {
		return
	}
	value = strings.TrimSpace(value)
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "ticket":
		metadata.ticketRef = value
	case "branch", "implementer harness":
		metadata.kind = runstate.Implement
	case "reviewer harness":
		if metadata.kind == "" {
			metadata.kind = runstate.Review
		}
	}
}

func (reader *Reader) readOptional(path string) ([]byte, bool) {
	contents, err := reader.files.ReadFile(path)
	return contents, err == nil
}

func summaryValue(contents, prefix string) string {
	for _, line := range strings.Split(contents, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func summaryIterations(contents string) int {
	for _, line := range strings.Split(contents, "\n") {
		if !strings.HasPrefix(line, "Iterations:") {
			continue
		}
		iteration, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Iterations:")))
		if err == nil && iteration > 0 {
			return iteration
		}
	}
	return 0
}

func legacyIteration(kind runstate.Kind, files FileSystem, runDir string) int {
	if kind == runstate.Review {
		return 1
	}
	entries, err := files.ReadDir(runDir)
	if err != nil {
		return 0
	}
	highest := 0
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		withoutPrefix := strings.TrimPrefix(entry.Name(), "iteration-")
		if withoutPrefix == entry.Name() {
			continue
		}
		separator := strings.IndexByte(withoutPrefix, '-')
		if separator < 1 {
			continue
		}
		iteration, err := strconv.Atoi(withoutPrefix[:separator])
		if err == nil && iteration > highest {
			highest = iteration
		}
	}
	return highest
}

func legacyTicketNumber(name string) string {
	separator := strings.IndexByte(name, '-')
	if separator < 1 || separator+1 >= len(name) {
		return ""
	}
	ticketNumber := name[separator+1:]
	if number, err := strconv.Atoi(ticketNumber); err != nil || number <= 0 {
		return ""
	}
	return ticketNumber
}

func parseRunDirectoryTimestamp(name string) time.Time {
	separator := strings.IndexByte(name, '-')
	if separator < 1 {
		return time.Time{}
	}
	timestamp, err := time.Parse(runDirectoryTimestampLayout, name[:separator])
	if err != nil {
		return time.Time{}
	}
	return timestamp
}

func historyRunSortKey(run HistoryRun) string {
	name := filepath.Base(run.RunDir)
	separator := strings.IndexByte(name, '-')
	if separator > 0 {
		return name[:separator] + "\x00" + name
	}
	return name
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
