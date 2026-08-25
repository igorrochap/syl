package tracker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

var (
	ticketFilename = regexp.MustCompile(`^([0-9]+)-.+\.md$`)
	ticketHeading  = regexp.MustCompile(`^#\s+([0-9]+)\s+—\s+(.+)$`)
)

type ticketFile struct {
	number int
	path   string
}

// Local stores tickets as markdown files under a project's .scratch tree.
// featureSlug is the destination directory for newly created tickets;
// resolution and listing always scan every feature directory.
type Local struct {
	projectRoot string
	featureSlug string
}

var _ Tracker = (*Local)(nil)

// NewLocal creates a local tracker rooted at projectRoot. The feature slug is
// used only when creating tickets and may be empty for read-only use.
func NewLocal(projectRoot, featureSlug string) (*Local, error) {
	if strings.TrimSpace(projectRoot) == "" {
		projectRoot = "."
	}
	if strings.TrimSpace(featureSlug) != "" {
		if filepath.IsAbs(featureSlug) || featureSlug == "." || featureSlug == ".." || strings.ContainsAny(featureSlug, `/\`) {
			return nil, fmt.Errorf("invalid local tracker feature slug %q", featureSlug)
		}
	}
	return &Local{projectRoot: projectRoot, featureSlug: featureSlug}, nil
}

// CopyTicketTo copies a resolved ticket into another project checkout while
// preserving its relative .scratch path. It is used when the source ticket
// has not been committed into the ref from which a worktree was created.
func (l *Local) CopyTicketTo(ctx context.Context, number int, destinationRoot string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(destinationRoot) == "" {
		return errors.New("cannot copy a local ticket without a destination root")
	}

	ticketFile, err := l.resolveSingleFile(number)
	if err != nil {
		return err
	}
	relativePath, err := filepath.Rel(l.projectRoot, ticketFile.path)
	if err != nil {
		return fmt.Errorf("resolve local ticket path: %w", err)
	}
	destination := filepath.Join(destinationRoot, relativePath)
	contents, err := os.ReadFile(ticketFile.path)
	if err != nil {
		return fmt.Errorf("read local ticket %s: %w", ticketFile.path, err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return fmt.Errorf("create local ticket directory: %w", err)
	}
	if err := os.WriteFile(destination, contents, 0o644); err != nil {
		return fmt.Errorf("write local ticket %s: %w", destination, err)
	}
	return nil
}

func (l *Local) Resolve(ctx context.Context, reference string) (Ticket, error) {
	if err := contextError(ctx); err != nil {
		return Ticket{}, err
	}
	number, err := parseReference(reference)
	if err != nil {
		return Ticket{}, err
	}
	ticketFile, err := l.resolveSingleFile(number)
	if err != nil {
		return Ticket{}, err
	}
	return readTicket(ticketFile)
}

func (l *Local) Create(ctx context.Context, title, body string) (Ticket, error) {
	if err := contextError(ctx); err != nil {
		return Ticket{}, err
	}
	if strings.TrimSpace(title) == "" {
		return Ticket{}, errors.New("cannot create a local ticket without a title")
	}
	if strings.TrimSpace(l.featureSlug) == "" {
		return Ticket{}, errors.New("cannot create a local ticket without a feature slug")
	}

	files, err := l.ticketFiles()
	if err != nil {
		return Ticket{}, err
	}
	maximum := 0
	for _, file := range files {
		if file.number > maximum {
			maximum = file.number
		}
	}
	number := maximum + 1
	issuesDir := filepath.Join(l.projectRoot, ".scratch", l.featureSlug, "issues")
	if err := os.MkdirAll(issuesDir, 0o755); err != nil {
		return Ticket{}, fmt.Errorf("create local tracker issues directory: %w", err)
	}
	slug := slugify(title)
	if slug == "" {
		slug = fmt.Sprintf("ticket-%02d", number)
	}
	path := filepath.Join(issuesDir, fmt.Sprintf("%02d-%s.md", number, slug))
	contents := fmt.Sprintf("# %02d — %s\n\n**What to build:** %s\n\n**Blocked by:** None — can start immediately.\n\n**Status:** todo\n", number, strings.TrimSpace(title), strings.TrimSpace(body))
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		return Ticket{}, fmt.Errorf("write local ticket %s: %w", path, err)
	}
	return readTicket(ticketFile{number: number, path: path})
}

func (l *Local) List(ctx context.Context) ([]Ticket, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	files, err := l.ticketFiles()
	if err != nil {
		return nil, err
	}
	tickets := make([]Ticket, 0, len(files))
	for _, file := range files {
		ticket, err := readTicket(file)
		if err != nil {
			return nil, err
		}
		tickets = append(tickets, ticket)
	}
	return tickets, nil
}

func (l *Local) UpdateStatus(ctx context.Context, number int, status string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	status = strings.TrimSpace(status)
	if status != "todo" && status != "doing" {
		return fmt.Errorf("invalid local ticket status %q; want todo or doing", status)
	}
	ticketFile, err := l.resolveSingleFile(number)
	if err != nil {
		return err
	}

	path := ticketFile.path
	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read local ticket %s: %w", path, err)
	}
	updated, err := replaceStatusValue(contents, status)
	if err != nil {
		return fmt.Errorf("update local ticket %s: %w", path, err)
	}
	if err := os.WriteFile(path, updated, 0o644); err != nil {
		return fmt.Errorf("write local ticket %s: %w", path, err)
	}
	return nil
}

func (l *Local) AddComment(ctx context.Context, number int, note string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	note = strings.TrimSpace(note)
	if note == "" {
		return errors.New("cannot add an empty local ticket note")
	}
	ticketFile, err := l.resolveSingleFile(number)
	if err != nil {
		return err
	}

	path := ticketFile.path
	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read local ticket %s: %w", path, err)
	}
	hasNotes := bytes.Contains(contents, []byte("## Notes"))
	suffix := ""
	if hasNotes {
		if len(contents) > 0 && contents[len(contents)-1] != '\n' {
			suffix += "\n"
		}
		suffix += "- " + note + "\n"
	} else {
		if len(contents) > 0 && contents[len(contents)-1] != '\n' {
			suffix += "\n"
		}
		if !bytes.HasSuffix(contents, []byte("\n\n")) {
			suffix += "\n"
		}
		suffix += "## Notes\n\n- " + note + "\n"
	}

	noteFile, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open local ticket %s for note: %w", path, err)
	}
	defer noteFile.Close()
	if _, err := noteFile.WriteString(suffix); err != nil {
		return fmt.Errorf("append note to local ticket %s: %w", path, err)
	}
	return nil
}

func (l *Local) filesForNumber(number int) ([]ticketFile, error) {
	files, err := l.ticketFiles()
	if err != nil {
		return nil, err
	}
	matches := make([]ticketFile, 0, 1)
	for _, file := range files {
		if file.number == number {
			matches = append(matches, file)
		}
	}
	return matches, nil
}

func (l *Local) resolveSingleFile(number int) (ticketFile, error) {
	files, err := l.filesForNumber(number)
	if err != nil {
		return ticketFile{}, err
	}
	if len(files) == 0 {
		return ticketFile{}, fmt.Errorf("local ticket #%d not found", number)
	}
	if len(files) > 1 {
		paths := make([]string, len(files))
		for i, file := range files {
			paths[i] = file.path
		}
		return ticketFile{}, fmt.Errorf("local ticket #%d is ambiguous; matching files: %s", number, strings.Join(paths, ", "))
	}
	return files[0], nil
}

func (l *Local) ticketFiles() ([]ticketFile, error) {
	scratchDir := filepath.Join(l.projectRoot, ".scratch")
	features, err := os.ReadDir(scratchDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read local tracker directory %s: %w", scratchDir, err)
	}

	files := make([]ticketFile, 0)
	for _, feature := range features {
		if !feature.IsDir() {
			continue
		}
		issuesDir := filepath.Join(scratchDir, feature.Name(), "issues")
		entries, err := os.ReadDir(issuesDir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read local tracker issues directory %s: %w", issuesDir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			match := ticketFilename.FindStringSubmatch(entry.Name())
			if match == nil {
				continue
			}
			number, err := strconv.Atoi(match[1])
			if err != nil || number < 1 {
				continue
			}
			files = append(files, ticketFile{
				number: number,
				path:   filepath.Join(issuesDir, entry.Name()),
			})
		}
	}

	sort.Slice(files, func(i, j int) bool {
		if files[i].number != files[j].number {
			return files[i].number < files[j].number
		}
		return files[i].path < files[j].path
	})
	return files, nil
}

func readTicket(file ticketFile) (Ticket, error) {
	contents, err := os.ReadFile(file.path)
	if err != nil {
		return Ticket{}, fmt.Errorf("read local ticket %s: %w", file.path, err)
	}
	lines := strings.Split(string(contents), "\n")
	if len(lines) == 0 {
		return Ticket{}, fmt.Errorf("local ticket %s is empty", file.path)
	}

	heading := strings.TrimSuffix(lines[0], "\r")
	match := ticketHeading.FindStringSubmatch(heading)
	if match == nil {
		return Ticket{}, fmt.Errorf("local ticket %s has an invalid title heading", file.path)
	}
	status, statusIndex := ticketStatus(lines)
	if statusIndex < 0 {
		return Ticket{}, fmt.Errorf("local ticket %s is missing a **Status:** line", file.path)
	}

	bodyLines := make([]string, 0, len(lines)-2)
	for index, line := range lines[1:] {
		if index == statusIndex-1 {
			continue
		}
		bodyLines = append(bodyLines, line)
	}

	number, err := strconv.Atoi(match[1])
	if err != nil {
		return Ticket{}, fmt.Errorf("local ticket %s has an invalid number: %w", file.path, err)
	}
	return Ticket{
		Number: number,
		Title:  strings.TrimSpace(match[2]),
		Body:   strings.TrimSpace(strings.Join(bodyLines, "\n")),
		Status: status,
	}, nil
}

func ticketStatus(lines []string) (string, int) {
	const marker = "**Status:**"
	for index, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, marker) {
			return strings.TrimSpace(strings.TrimPrefix(line, marker)), index
		}
	}
	return "", -1
}

func parseReference(reference string) (int, error) {
	value := strings.TrimSpace(reference)
	value = strings.TrimSpace(strings.TrimPrefix(value, "#"))
	number, err := strconv.Atoi(value)
	if err != nil || number < 1 {
		return 0, fmt.Errorf("invalid ticket reference %q; want N or #N", reference)
	}
	return number, nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func replaceStatusValue(contents []byte, status string) ([]byte, error) {
	const marker = "**Status:**"
	markerBytes := []byte(marker)
	found := false
	for lineStart := 0; lineStart <= len(contents); {
		lineEnd := bytes.IndexByte(contents[lineStart:], '\n')
		if lineEnd < 0 {
			lineEnd = len(contents)
		} else {
			lineEnd += lineStart
		}
		line := contents[lineStart:lineEnd]
		if bytes.HasPrefix(line, markerBytes) {
			if found {
				return nil, errors.New("multiple **Status:** lines")
			}
			found = true
			valueStart := lineStart + len(markerBytes)
			for valueStart < lineEnd && (contents[valueStart] == ' ' || contents[valueStart] == '\t') {
				valueStart++
			}
			valueEnd := lineEnd
			if valueEnd > valueStart && contents[valueEnd-1] == '\r' {
				valueEnd--
			}
			updated := make([]byte, 0, len(contents)+len(status))
			updated = append(updated, contents[:valueStart]...)
			updated = append(updated, status...)
			updated = append(updated, contents[valueEnd:]...)
			contents = updated
			lineStart = valueStart + len(status)
			continue
		}
		if lineEnd == len(contents) {
			break
		}
		lineStart = lineEnd + 1
	}
	if !found {
		return nil, errors.New("missing **Status:** line")
	}
	return contents, nil
}

func slugify(value string) string {
	var builder strings.Builder
	needsSeparator := false
	for _, char := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			if needsSeparator && builder.Len() > 0 {
				builder.WriteByte('-')
			}
			builder.WriteRune(char)
			needsSeparator = false
			continue
		}
		needsSeparator = builder.Len() > 0
	}
	return builder.String()
}
