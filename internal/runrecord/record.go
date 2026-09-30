// Package runrecord owns the durable files stored in one Run directory.
package runrecord

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/igorrochap/syl/internal/verdict"
)

const (
	metadataFile = "metadata.txt"
	sessionsFile = "sessions.txt"
	summaryFile  = "summary.txt"
	usageFile    = "usage.json"
)

// Metadata contains the values and role context blocks recorded for a Run.
type Metadata struct {
	Branch             string
	BranchPoint        string
	WorkRoot           string
	ImplementerHarness string
	ReviewerHarness    string
	ImplementContext   string
	ReviewContext      string
	TicketRef          string
	Kind               Kind
}

// Session is one role session recorded in sessions.txt.
type Session struct {
	Iteration int
	Role      string
	SessionID string
}

// Summary contains the interpreted values stored in summary.txt.
type Summary struct {
	Contents     string
	Iterations   int
	FinalVerdict string
	Text         string
	DiffStat     string
}

// ArtifactKind identifies a durable artifact without exposing its file name.
type ArtifactKind uint8

const (
	// ImplementFeed is an implementer feed file.
	ImplementFeed ArtifactKind = iota + 1
	// ImplementTranscript is an implementer transcript file.
	ImplementTranscript
	// ImplementHandoff is an implementer handoff document.
	ImplementHandoff
	// ReviewDiff is a reviewer diff file.
	ReviewDiff
	// ReviewFeed is a reviewer feed file.
	ReviewFeed
	// ReviewTranscript is a reviewer transcript file.
	ReviewTranscript
	// VerdictFile is a structured review verdict file.
	VerdictFile
	// SummaryFile is a Run summary file.
	SummaryFile
	// SessionsFile stores role session identifiers.
	SessionsFile
	// UsageFile stores token usage bytes owned by the usage package.
	UsageFile
	// MetadataFile stores Run metadata.
	MetadataFile
)

// Artifact describes a recognized file in a Run directory.
type Artifact struct {
	Kind       ArtifactKind
	Name       string
	Standalone bool
	Iteration  int
	Role       string
	Extension  string
	ModTime    time.Time
}

// Record is the readable contents and recognized artifacts of one Run.
type Record struct {
	Directory        string
	Metadata         Metadata
	Sessions         []Session
	Summary          Summary
	SummaryExists    bool
	State            State
	HasState         bool
	UsageContents    []byte
	UsageExists      bool
	Artifacts        []Artifact
	Verdicts         map[int]verdict.Verdict
	VerdictText      map[int]string
	HighestIteration int
}

// FileSystem is the filesystem surface used to read Run records.
type FileSystem interface {
	ReadDir(name string) ([]os.DirEntry, error)
	ReadFile(name string) ([]byte, error)
	Stat(name string) (os.FileInfo, error)
	EvalSymlinks(path string) (string, error)
}

type osFileSystem struct{}

// Reader reads one Run record. A nil filesystem uses the operating system.
type Reader struct {
	files        FileSystem
	processAlive func(int) bool
}

// NewReader constructs a Run record reader.
func NewReader(files FileSystem) *Reader {
	return NewReaderWithProcessLiveness(files, checkProcessAlive)
}

// NewReaderWithProcessLiveness constructs a reader with a process liveness check.
func NewReaderWithProcessLiveness(files FileSystem, processAlive func(int) bool) *Reader {
	if files == nil {
		files = osFileSystem{}
	}
	if processAlive == nil {
		processAlive = checkProcessAlive
	}
	return &Reader{files: files, processAlive: processAlive}
}

// Read loads all known Run record files and artifacts from runDir.
func (reader *Reader) Read(runDir string) (Record, error) {
	entries, err := reader.files.ReadDir(runDir)
	if err != nil {
		return Record{}, err
	}
	record := Record{
		Directory: runDir, Verdicts: make(map[int]verdict.Verdict), VerdictText: make(map[int]string),
	}
	reader.readRecordFiles(&record, runDir)
	for _, entry := range entries {
		reader.readEntry(&record, runDir, entry)
	}
	sort.Slice(record.Artifacts, func(left, right int) bool {
		return record.Artifacts[left].Name < record.Artifacts[right].Name
	})
	return record, nil
}

// readRecordFiles fills the record from the fixed-name files, skipping any that are absent.
func (reader *Reader) readRecordFiles(record *Record, runDir string) {
	if contents, err := reader.files.ReadFile(filePath(runDir, metadataFile)); err == nil {
		record.Metadata = ParseMetadata(contents)
	}
	if contents, err := reader.files.ReadFile(filePath(runDir, sessionsFile)); err == nil {
		record.Sessions = ParseSessions(contents)
	}
	if contents, err := reader.files.ReadFile(filePath(runDir, summaryFile)); err == nil {
		record.Summary = ParseSummary(contents)
		record.SummaryExists = true
	}
	if contents, err := reader.files.ReadFile(filePath(runDir, usageFile)); err == nil {
		record.UsageContents = contents
		record.UsageExists = true
	}
	if state, err := reader.ReadState(runDir); err == nil {
		record.State = state
		record.HasState = true
	}
}

// readEntry adds one directory entry to the record when it is a known artifact.
func (reader *Reader) readEntry(record *Record, runDir string, entry os.DirEntry) {
	if entry.Type().IsRegular() {
		record.HighestIteration = max(record.HighestIteration, ParseIterationNumber(entry.Name()))
	}
	artifact, ok := ParseArtifactName(entry.Name())
	if !ok || entry.IsDir() {
		return
	}
	if info, err := entry.Info(); err == nil {
		artifact.ModTime = info.ModTime()
	}
	record.Artifacts = append(record.Artifacts, artifact)
	if artifact.Kind == VerdictFile {
		reader.readVerdict(record, runDir, artifact)
	}
}

func (reader *Reader) readVerdict(record *Record, runDir string, artifact Artifact) {
	contents, err := reader.files.ReadFile(filePath(runDir, artifact.Name))
	if err != nil {
		return
	}
	record.VerdictText[artifact.Iteration] = ParseVerdictLabel(contents)
	if parsed, err := verdict.Parse(string(contents)); err == nil {
		record.Verdicts[artifact.Iteration] = parsed
	}
}

// ReadMetadata reads and parses metadata.txt.
func (reader *Reader) ReadMetadata(runDir string) (Metadata, error) {
	contents, err := reader.files.ReadFile(filePath(runDir, metadataFile))
	if err != nil {
		return Metadata{}, err
	}
	return ParseMetadata(contents), nil
}

// ReadSessions reads and parses sessions.txt.
func (reader *Reader) ReadSessions(runDir string) ([]Session, error) {
	contents, err := reader.files.ReadFile(filePath(runDir, sessionsFile))
	if err != nil {
		return nil, err
	}
	return ParseSessions(contents), nil
}

// ReadSummary reads and parses summary.txt.
func (reader *Reader) ReadSummary(runDir string) (Summary, error) {
	contents, err := reader.files.ReadFile(filePath(runDir, summaryFile))
	if err != nil {
		return Summary{}, err
	}
	return ParseSummary(contents), nil
}

// HasSummary reports whether summary.txt exists without reading its contents.
func (reader *Reader) HasSummary(runDir string) (bool, error) {
	_, err := reader.files.Stat(filePath(runDir, summaryFile))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// ReadUsage reads the bytes of usage.json for the usage module to decode.
func (reader *Reader) ReadUsage(runDir string) ([]byte, error) {
	return reader.files.ReadFile(filePath(runDir, usageFile))
}

// ReadState reads and validates run-state.json.
func (reader *Reader) ReadState(runDir string) (State, error) {
	contents, err := reader.files.ReadFile(Path(runDir))
	if err != nil {
		return State{}, err
	}
	return Parse(Path(runDir), contents)
}

// Directories returns the child directories found under originRoot's runs directory.
func (reader *Reader) Directories(originRoot string) ([]string, error) {
	runsDir := RunsDirectory(originRoot)
	entries, err := reader.files.ReadDir(runsDir)
	if err != nil {
		return nil, err
	}
	directories := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			directories = append(directories, RunDirectory(originRoot, entry.Name()))
		}
	}
	return directories, nil
}

// ResolveArtifact resolves an artifact name as a regular file inside runDir.
func (reader *Reader) ResolveArtifact(runDir, artifactName string) (string, error) {
	if strings.TrimSpace(runDir) == "" || strings.TrimSpace(artifactName) == "" {
		return "", errors.New("run directory and artifact are required")
	}
	if isAbsoluteArtifactPath(artifactName) {
		return "", errors.New("absolute artifact paths are not allowed")
	}
	if hasParentPathComponent(artifactName) {
		return "", errors.New("artifact path traversal is not allowed")
	}
	root, err := reader.ResolveRunDirectory(runDir)
	if err != nil {
		return "", err
	}
	resolved, err := reader.files.EvalSymlinks(filepath.Join(root, artifactName))
	if err != nil {
		return "", err
	}
	if !pathWithin(root, resolved) {
		return "", errors.New("artifact resolves outside Run directory")
	}
	info, err := reader.files.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("artifact is not a regular file")
	}
	return resolved, nil
}

// ResolveRunDirectory resolves and validates an existing Run directory path.
func (reader *Reader) ResolveRunDirectory(runDir string) (string, error) {
	root, err := reader.files.EvalSymlinks(runDir)
	if err != nil {
		return "", err
	}
	rootInfo, err := reader.files.Stat(root)
	if err != nil {
		return "", err
	}
	if !rootInfo.IsDir() {
		return "", errors.New("run directory is not a directory")
	}
	if !IsRunDirectory(root) {
		return "", errors.New("path is not a Run directory")
	}
	return root, nil
}

func (osFileSystem) ReadDir(name string) ([]os.DirEntry, error) { return os.ReadDir(name) }
func (osFileSystem) ReadFile(name string) ([]byte, error)       { return os.ReadFile(name) }
func (osFileSystem) Stat(name string) (os.FileInfo, error)      { return os.Stat(name) }
func (osFileSystem) EvalSymlinks(path string) (string, error)   { return filepath.EvalSymlinks(path) }

// HarnessFor returns the recorded Harness for a Run kind.
func (metadata Metadata) HarnessFor(kind Kind) string {
	if kind == Review {
		return metadata.ReviewerHarness
	}
	return metadata.ImplementerHarness
}

// ParseVerdictLabel reads the first VERDICT field from a verdict file.
func ParseVerdictLabel(contents []byte) string {
	for _, line := range strings.Split(string(contents), "\n") {
		if strings.HasPrefix(line, "VERDICT:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "VERDICT:"))
		}
	}
	return ""
}

// metadataFields sets one metadata.txt field, keyed by its lowercase name.
var metadataFields = map[string]func(metadata *Metadata, value string){
	"branch": func(metadata *Metadata, value string) {
		metadata.Branch = value
		metadata.Kind = Implement
	},
	"ticket": func(metadata *Metadata, value string) {
		metadata.TicketRef = value
		metadata.inferKind(Review)
	},
	"branch point":        func(metadata *Metadata, value string) { metadata.BranchPoint = value },
	"work root":           func(metadata *Metadata, value string) { metadata.WorkRoot = value },
	"implementer harness": func(metadata *Metadata, value string) { metadata.ImplementerHarness = value },
	"reviewer harness": func(metadata *Metadata, value string) {
		metadata.ReviewerHarness = value
		metadata.inferKind(Review)
	},
}

// contextRoles maps a metadata.txt context header to the field its block fills.
var contextRoles = map[string]func(metadata *Metadata) *string{
	"implementer context": func(metadata *Metadata) *string { return &metadata.ImplementContext },
	"reviewer context":    func(metadata *Metadata) *string { return &metadata.ReviewContext },
}

func (metadata *Metadata) inferKind(kind Kind) {
	if metadata.Kind == "" {
		metadata.Kind = kind
	}
}

// contextBlock collects the indented lines that follow a role context header.
type contextBlock struct {
	target *string
	lines  []string
}

func (block *contextBlock) add(line string) {
	if block.target != nil {
		block.lines = append(block.lines, strings.TrimPrefix(line, "  "))
	}
}

func (block *contextBlock) flush() {
	if block.target != nil {
		*block.target = strings.TrimSpace(strings.Join(block.lines, "\n"))
	}
	*block = contextBlock{}
}

// ParseMetadata parses metadata.txt, including its indented role context blocks.
func ParseMetadata(contents []byte) Metadata {
	var metadata Metadata
	var block contextBlock
	for _, line := range strings.Split(string(contents), "\n") {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			block.add(line)
			continue
		}
		block.flush()
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if setField, known := metadataFields[key]; known {
			setField(&metadata, value)
		}
		if target, known := contextRoles[key]; known {
			block.target = target(&metadata)
		}
	}
	block.flush()
	return metadata
}

// ParseSessions returns the valid sessions.txt records and ignores malformed lines.
func ParseSessions(contents []byte) []Session {
	var sessions []Session
	for _, line := range strings.Split(string(contents), "\n") {
		session, ok := parseSessionLine(line)
		if ok {
			sessions = append(sessions, session)
		}
	}
	return sessions
}

func parseSessionLine(line string) (Session, bool) {
	left, sessionID, ok := strings.Cut(strings.TrimSpace(line), ":")
	if !ok {
		return Session{}, false
	}
	fields := strings.Fields(left)
	if len(fields) != 3 || !strings.EqualFold(fields[0], "iteration") {
		return Session{}, false
	}
	iteration, err := strconv.Atoi(fields[1])
	if err != nil || iteration < 0 {
		return Session{}, false
	}
	role := strings.TrimSpace(fields[2])
	sessionID = strings.TrimSpace(sessionID)
	if role == "" || sessionID == "" {
		return Session{}, false
	}
	return Session{Iteration: iteration, Role: role, SessionID: sessionID}, true
}

// ParseSummary interprets the stable fields stored in summary.txt.
func ParseSummary(contents []byte) Summary {
	text := string(contents)
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	summary := Summary{Contents: text}
	summary.Iterations = firstPositiveIterations(lines)
	if index := firstLineIndex(lines, "Final verdict:"); index >= 0 {
		summary.FinalVerdict = fieldValue(lines[index], "Final verdict:")
	}
	if index := firstLineIndex(lines, "Summary:"); index >= 0 {
		summary.Text = fieldValue(lines[index], "Summary:")
	}
	if index := firstLineIndex(lines, "Diff stat:"); index >= 0 {
		summary.DiffStat = strings.TrimSpace(strings.Join(lines[index+1:], "\n"))
	}
	return summary
}

// firstPositiveIterations skips Iterations lines that are not positive numbers.
func firstPositiveIterations(lines []string) int {
	for _, line := range lines {
		if !strings.HasPrefix(line, "Iterations:") {
			continue
		}
		iteration, err := strconv.Atoi(fieldValue(line, "Iterations:"))
		if err == nil && iteration > 0 {
			return iteration
		}
	}
	return 0
}

func firstLineIndex(lines []string, prefix string) int {
	for index, line := range lines {
		if strings.HasPrefix(line, prefix) {
			return index
		}
	}
	return -1
}

func fieldValue(line, prefix string) string {
	return strings.TrimSpace(strings.TrimPrefix(line, prefix))
}

type roleExtension struct{ role, extension string }

var iterationArtifactKinds = map[roleExtension]ArtifactKind{
	{"implement", "feed"}:       ImplementFeed,
	{"implement", "transcript"}: ImplementTranscript,
	{"review", "diff"}:          ReviewDiff,
	{"review", "feed"}:          ReviewFeed,
	{"review", "transcript"}:    ReviewTranscript,
	{"verdict", "txt"}:          VerdictFile,
}

// ParseArtifactName parses a known standalone or iteration artifact name.
func ParseArtifactName(name string) (Artifact, bool) {
	if artifact, ok := parseStandaloneArtifact(name); ok {
		return artifact, true
	}
	if artifact, ok := parseHandoffArtifact(name); ok {
		return artifact, true
	}
	return parseIterationArtifact(name)
}

func parseStandaloneArtifact(name string) (Artifact, bool) {
	switch name {
	case "review.diff":
		return Artifact{Kind: ReviewDiff, Name: name, Standalone: true, Iteration: 1, Role: "review", Extension: "diff"}, true
	case "review.feed":
		return Artifact{Kind: ReviewFeed, Name: name, Standalone: true, Iteration: 1, Role: "review", Extension: "feed"}, true
	case "review.transcript":
		return Artifact{Kind: ReviewTranscript, Name: name, Standalone: true, Iteration: 1, Role: "review", Extension: "transcript"}, true
	case "verdict.txt":
		return Artifact{Kind: VerdictFile, Name: name, Standalone: true, Iteration: 1}, true
	case summaryFile:
		return Artifact{Kind: SummaryFile, Name: name, Standalone: true}, true
	case sessionsFile:
		return Artifact{Kind: SessionsFile, Name: name, Standalone: true}, true
	case usageFile:
		return Artifact{Kind: UsageFile, Name: name, Standalone: true}, true
	}
	return Artifact{}, false
}

func parseHandoffArtifact(name string) (Artifact, bool) {
	number, found := strings.CutPrefix(name, "handoff-")
	if !found {
		return Artifact{}, false
	}
	number, found = strings.CutSuffix(number, ".md")
	if !found {
		return Artifact{}, false
	}
	iteration, err := strconv.Atoi(number)
	if err != nil || iteration <= 0 {
		return Artifact{}, false
	}
	return Artifact{Kind: ImplementHandoff, Name: name, Iteration: iteration, Role: "implement", Extension: "md"}, true
}

func parseIterationArtifact(name string) (Artifact, bool) {
	iteration := ParseIterationNumber(name)
	if iteration == 0 {
		return Artifact{}, false
	}
	withoutPrefix := strings.TrimPrefix(name, "iteration-")
	roleAndExtension := withoutPrefix[strings.IndexByte(withoutPrefix, '-')+1:]
	dot := strings.LastIndexByte(roleAndExtension, '.')
	if dot < 1 {
		return Artifact{}, false
	}
	role, extension := roleAndExtension[:dot], roleAndExtension[dot+1:]
	kind, ok := iterationArtifactKinds[roleExtension{role, extension}]
	if !ok {
		return Artifact{}, false
	}
	return Artifact{Kind: kind, Name: name, Iteration: iteration, Role: role, Extension: extension}, true
}

// ArtifactLabel returns the artifact kind suffix shown in the Run page.
func ArtifactLabel(name string) string {
	artifact, ok := ParseArtifactName(name)
	if ok && artifact.Extension != "" {
		return artifact.Extension
	}
	return name
}

// ParseIterationNumber reads a positive iteration prefix from an artifact name.
func ParseIterationNumber(name string) int {
	if !strings.HasPrefix(name, "iteration-") {
		return 0
	}
	withoutPrefix := strings.TrimPrefix(name, "iteration-")
	separator := strings.IndexByte(withoutPrefix, '-')
	if separator < 1 {
		return 0
	}
	iteration, err := strconv.Atoi(withoutPrefix[:separator])
	if err != nil || iteration <= 0 {
		return 0
	}
	return iteration
}

// RunsDirectory returns the Run directory parent for an origin root.
func RunsDirectory(originRoot string) string {
	return filepath.Join(originRoot, ".syl", "runs")
}

// RelativeDirectory returns the Project-relative location of a named Run.
func RelativeDirectory(name string) string {
	return filepath.Join(".syl", "runs", name)
}

// RunDirectory returns a named Run directory under originRoot.
func RunDirectory(originRoot, name string) string {
	return filepath.Join(RunsDirectory(originRoot), name)
}

// Directories returns the child directories found under originRoot's runs directory.
func Directories(originRoot string) ([]string, error) {
	return NewReader(nil).Directories(originRoot)
}

// IsRunDirectory reports whether path has the durable .syl/runs/<run> shape.
func IsRunDirectory(path string) bool {
	runsDirectory := filepath.Dir(path)
	return filepath.Base(path) != "" && filepath.Base(runsDirectory) == "runs" &&
		filepath.Base(filepath.Dir(runsDirectory)) == ".syl"
}

// ResolveArtifact resolves an artifact name as a regular file inside runDir.
func ResolveArtifact(runDir, artifactName string) (string, error) {
	return NewReader(nil).ResolveArtifact(runDir, artifactName)
}

// ResolveRunDirectory resolves and validates an existing Run directory path.
func ResolveRunDirectory(runDir string) (string, error) {
	return NewReader(nil).ResolveRunDirectory(runDir)
}

func isAbsoluteArtifactPath(path string) bool {
	if filepath.IsAbs(path) || filepath.VolumeName(path) != "" || strings.HasPrefix(path, "\\") {
		return true
	}
	return len(path) >= 2 && path[1] == ':'
}

func hasParentPathComponent(path string) bool {
	for _, component := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if component == ".." {
			return true
		}
	}
	return false
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(relative) {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func filePath(runDir, name string) string { return filepath.Join(runDir, name) }

// DirectoryName formats the stable timestamp and ticket suffix for a Run.
func DirectoryName(now time.Time, kind Kind, ticketRef string) (string, error) {
	suffix := strings.TrimPrefix(strings.TrimSpace(ticketRef), "#")
	if suffix == "" {
		suffix = string(kind)
	}
	switch kind {
	case Implement:
	case Review:
		number, err := strconv.Atoi(suffix)
		if err != nil || number <= 0 {
			suffix = "review"
		}
	default:
		return "", fmt.Errorf("unsupported Run kind %q", kind)
	}
	return now.UTC().Format("20060102T150405.000000000Z") + "-" + suffix, nil
}

// DirectoryTimestamp reads the timestamp prefix of a Run directory name.
func DirectoryTimestamp(name string) time.Time {
	separator := strings.IndexByte(name, '-')
	if separator < 1 {
		return time.Time{}
	}
	timestamp, err := time.Parse("20060102T150405.000000000Z", name[:separator])
	if err != nil {
		return time.Time{}
	}
	return timestamp
}

// LegacyTicketNumber extracts the ticket suffix used by early implement Runs.
func LegacyTicketNumber(name string) string {
	separator := strings.IndexByte(name, '-')
	if separator < 1 || separator+1 >= len(name) {
		return ""
	}
	number := name[separator+1:]
	parsed, err := strconv.Atoi(number)
	if err != nil || parsed <= 0 {
		return ""
	}
	return number
}

// DirectorySortKey is the stable newest-first key used by Run history.
func DirectorySortKey(name string) string {
	separator := strings.IndexByte(name, '-')
	if separator > 0 {
		return name[:separator] + "\x00" + name
	}
	return name
}
