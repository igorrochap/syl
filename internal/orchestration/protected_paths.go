package orchestration

import (
	"sort"
	"strconv"
	"strings"

	"github.com/igorrochap/syl/internal/config"
)

func protectedPathsInDiff(diff string, configured []string) []string {
	protected := (config.GateConfig{ProtectedPaths: configured}).EffectiveProtectedPaths()
	touched := make(map[string]bool)
	for _, line := range strings.Split(diff, "\n") {
		if !strings.HasPrefix(line, "diff --git ") {
			continue
		}
		for _, path := range pathsFromDiffHeader(strings.TrimPrefix(line, "diff --git ")) {
			if matchesProtectedPath(path, protected) {
				touched[path] = true
			}
		}
	}
	paths := make([]string, 0, len(touched))
	for path := range touched {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func matchesProtectedPath(path string, protected []string) bool {
	for _, prefix := range protected {
		base := strings.TrimSuffix(prefix, "/")
		isMatch := path == base || strings.HasPrefix(path, base+"/")
		if isMatch {
			return true
		}
	}
	return false
}

func pathsFromDiffHeader(header string) []string {
	separator := strings.LastIndex(header, " b/")
	quotedSeparator := strings.LastIndex(header, " \"b/")
	if quotedSeparator > separator {
		separator = quotedSeparator
	}
	if separator < 0 {
		return nil
	}
	var paths []string
	for _, value := range []string{header[:separator], header[separator+1:]} {
		if strings.HasPrefix(value, "\"") {
			decoded, err := strconv.Unquote(value)
			if err != nil {
				continue
			}
			value = decoded
		}
		if strings.HasPrefix(value, "a/") || strings.HasPrefix(value, "b/") {
			paths = append(paths, value[2:])
		}
	}
	return paths
}

func appendProtectedPaths(prompt string, paths []string) string {
	if len(paths) == 0 {
		return prompt
	}
	return prompt + "\n\n## Protected paths touched by the diff\n\n- " + strings.Join(paths, "\n- ") + "\n\nReport any loosening of a protected check that the ticket did not request as a [blocking] finding."
}
