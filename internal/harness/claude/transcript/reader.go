// Package transcript finds and reads Claude Code session transcripts.
package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ErrParse identifies a malformed transcript entry.
var ErrParse = errors.New("Claude transcript parse failure")

// Entry is one JSON record and its physical line in a session transcript.
type Entry struct {
	Line int
	Raw  json.RawMessage
}

// Reader finds transcripts in a Claude Code home directory and reads their entries.
type Reader struct {
	homeDir string
}

// New returns a transcript reader. An empty homeDir uses the current user's home directory.
func New(homeDir string) Reader {
	return Reader{homeDir: homeDir}
}

// Find returns the parent transcript for a project directory and session ID.
func (r Reader) Find(projectDir, sessionID string) (string, error) {
	projectsDir, err := r.projectsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(
		projectsDir,
		projectSlug(projectDir),
		strings.TrimSpace(sessionID)+".jsonl",
	), nil
}

// FindSubagents returns the session's subagent transcripts in path order.
func (r Reader) FindSubagents(projectDir, sessionID string) ([]string, error) {
	projectsDir, err := r.projectsDir()
	if err != nil {
		return nil, err
	}
	pattern := filepath.Join(
		projectsDir,
		projectSlug(projectDir),
		strings.TrimSpace(sessionID),
		"subagents",
		"*.jsonl",
	)
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("find Claude subagent transcripts for %s: %w", sessionID, err)
	}
	sort.Strings(paths)
	return paths, nil
}

// Read returns the non-empty JSON entries from a transcript in file order.
func (r Reader) Read(path string) ([]Entry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read Claude transcript %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var entries []Entry
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var entry json.RawMessage
		if err := json.Unmarshal(line, &entry); err != nil {
			return nil, fmt.Errorf("%w: %s line %d: %v", ErrParse, path, lineNumber, err)
		}
		entries = append(entries, Entry{Line: lineNumber, Raw: entry})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read Claude transcript %s: %w", path, err)
	}
	return entries, nil
}

func (r Reader) projectsDir() (string, error) {
	homeDir := strings.TrimSpace(r.homeDir)
	if homeDir == "" {
		var err error
		homeDir, err = os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("determine Claude home directory: %w", err)
		}
	}
	return filepath.Join(homeDir, ".claude", "projects"), nil
}

func projectSlug(projectDir string) string {
	if absolute, err := filepath.Abs(projectDir); err == nil {
		projectDir = absolute
	}
	// Claude Code uses a dash for every path character outside ASCII letters
	// and digits, including the dot in hidden directories such as .syl.
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, projectDir)
}
