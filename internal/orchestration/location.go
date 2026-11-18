package orchestration

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/igorrochap/syl/internal/verdict"
)

const unverifiedLocationMark = "(location unverified)"

type findingLineLocation struct {
	path      string
	startLine uint64
	endLine   uint64
	hasLine   bool
	valid     bool
}

func verifyVerdictLocations(workRoot string, review verdict.Verdict) verdict.Verdict {
	checked := review
	checked.Findings = append([]verdict.Finding(nil), review.Findings...)
	for index, finding := range checked.Findings {
		checked.Findings[index].Location = verifyFindingLocation(workRoot, finding.Location)
	}
	return checked
}

func verifyFindingLocation(workRoot, location string) string {
	location = stripUnverifiedLocationMark(location)
	lineLocation := parseFindingLineLocation(location)
	path := location
	if lineLocation.hasLine {
		path = lineLocation.path
	}

	contents, readable := readWorkRootFile(workRoot, path)
	if !readable {
		return markUnreadableFindingLocation(location, path, lineLocation.hasLine)
	}
	if !lineLocation.hasLine {
		return location
	}
	lineCount := countFileLines(contents)
	isOutOfRange := !validFindingLineRange(lineLocation, lineCount)
	isInvalidLineLocation := !lineLocation.valid || isOutOfRange
	if isInvalidLineLocation {
		return markLocationUnverified(path)
	}
	return location
}

func stripUnverifiedLocationMark(location string) string {
	if !strings.HasSuffix(location, unverifiedLocationMark) {
		return location
	}
	location = strings.TrimSuffix(location, unverifiedLocationMark)
	return strings.TrimRight(location, " \t\r\n")
}

func markUnreadableFindingLocation(location, path string, hasLine bool) string {
	isFreeTextLocation := hasLine && !looksLikeFindingPath(path)
	if isFreeTextLocation {
		return markLocationUnverified(location)
	}
	return markLocationUnverified(path)
}

func parseFindingLineLocation(location string) findingLineLocation {
	separator := strings.LastIndexByte(location, ':')
	if separator < 0 {
		return findingLineLocation{}
	}
	suffix := location[separator+1:]
	if strings.HasPrefix(suffix, "-") && isDecimal(suffix[1:]) {
		return findingLineLocation{path: location[:separator], hasLine: true}
	}
	startText, endText := suffix, suffix
	if rangeSeparator := strings.IndexByte(suffix, '-'); rangeSeparator >= 0 {
		if strings.IndexByte(suffix[rangeSeparator+1:], '-') >= 0 {
			return findingLineLocation{}
		}
		startText = suffix[:rangeSeparator]
		endText = suffix[rangeSeparator+1:]
	}
	if !isDecimal(startText) || !isDecimal(endText) {
		return findingLineLocation{}
	}
	startLine, startValid := parseDecimal(startText)
	endLine, endValid := parseDecimal(endText)

	parsed := findingLineLocation{
		path:      location[:separator],
		startLine: startLine,
		endLine:   endLine,
		hasLine:   true,
		valid:     startValid && endValid,
	}
	return parsed
}

func looksLikeFindingPath(path string) bool {
	if strings.ContainsAny(path, `/\\`) || filepath.Ext(path) != "" {
		return true
	}
	return len(strings.Fields(path)) <= 1
}

func isDecimal(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func parseDecimal(value string) (uint64, bool) {
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func readWorkRootFile(workRoot, relativePath string) ([]byte, bool) {
	hasWorkRoot := strings.TrimSpace(workRoot) != ""
	isRelativePath := filepath.IsLocal(filepath.FromSlash(relativePath))
	isOutsideWorkRoot := !hasWorkRoot || !isRelativePath
	if isOutsideWorkRoot {
		return nil, false
	}
	root, err := filepath.Abs(workRoot)
	if err != nil {
		return nil, false
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, false
	}
	path := filepath.Join(root, filepath.FromSlash(relativePath))
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, false
	}
	relativeResolvedPath, err := filepath.Rel(root, resolvedPath)
	if err != nil || !filepath.IsLocal(relativeResolvedPath) {
		return nil, false
	}
	contents, err := os.ReadFile(path)
	return contents, err == nil
}

func validFindingLineRange(location findingLineLocation, lineCount int) bool {
	if location.startLine < 1 || location.endLine < location.startLine {
		return false
	}
	return location.endLine <= uint64(lineCount)
}

func countFileLines(contents []byte) int {
	if len(contents) == 0 {
		return 0
	}
	lines := bytes.Count(contents, []byte{'\n'})
	if contents[len(contents)-1] != '\n' {
		lines++
	}
	return lines
}

func markLocationUnverified(location string) string {
	if location == "" {
		return unverifiedLocationMark
	}
	return location + " " + unverifiedLocationMark
}
