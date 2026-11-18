package orchestration

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/igorrochap/syl/internal/verdict"
)

func TestVerifyVerdictLocations(t *testing.T) {
	root := t.TempDir()
	writeLocationTestFile(t, root, "valid.go", "package valid\nfunc one() {}\nfunc two() {}\n")
	writeLocationTestFile(t, root, "single-line.go", "package single\n")
	writeLocationTestFile(t, root, "without-final-newline.go", "package nofinal")
	writeLocationTestFile(t, root, "empty.go", "")
	if err := os.Mkdir(filepath.Join(root, "directory"), 0o755); err != nil {
		t.Fatal(err)
	}
	unreadablePath := filepath.Join(root, "unreadable.go")
	writeLocationTestFile(t, root, "unreadable.go", "package unreadable\n")
	if err := os.Chmod(unreadablePath, 0); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		location string
		want     string
	}{
		{name: "existing line", location: "valid.go:3", want: "valid.go:3"},
		{name: "line past end", location: "valid.go:4", want: "valid.go (location unverified)"},
		{name: "marked line past end", location: "valid.go:9999 (location unverified)", want: "valid.go (location unverified)"},
		{name: "marked line past end with tab", location: "valid.go:9999\t(location unverified)", want: "valid.go (location unverified)"},
		{name: "marked valid line is rechecked", location: "valid.go:2 (location unverified)", want: "valid.go:2"},
		{name: "zero line", location: "valid.go:0", want: "valid.go (location unverified)"},
		{name: "negative line", location: "valid.go:-1", want: "valid.go (location unverified)"},
		{name: "existing path without line", location: "valid.go", want: "valid.go"},
		{name: "missing path with line", location: "missing.go:3", want: "missing.go (location unverified)"},
		{name: "missing path", location: "missing.go", want: "missing.go (location unverified)"},
		{name: "valid range", location: "valid.go:2-3", want: "valid.go:2-3"},
		{name: "range past end", location: "valid.go:2-4", want: "valid.go (location unverified)"},
		{name: "invalid range start", location: "valid.go:0-2", want: "valid.go (location unverified)"},
		{name: "reversed range", location: "valid.go:3-2", want: "valid.go (location unverified)"},
		{name: "line number overflow", location: "valid.go:18446744073709551616", want: "valid.go (location unverified)"},
		{name: "file without final newline", location: "without-final-newline.go:1", want: "without-final-newline.go:1"},
		{name: "empty file line", location: "empty.go:1", want: "empty.go (location unverified)"},
		{name: "free text", location: "the changed behavior", want: "the changed behavior (location unverified)"},
		{name: "free text with line-like suffix", location: "around validation:20", want: "around validation:20 (location unverified)"},
		{name: "free text with nonnumeric suffix", location: "around validation:later", want: "around validation:later (location unverified)"},
		{name: "cannot read path as a file", location: "directory", want: "directory (location unverified)"},
		{name: "path outside work root", location: "../outside.go:1", want: "../outside.go (location unverified)"},
		{name: "absolute path", location: filepath.Join(root, "valid.go") + ":1", want: filepath.Join(root, "valid.go") + " (location unverified)"},
		{name: "empty path", location: ":1", want: "(location unverified)"},
	}
	if _, err := os.ReadFile(unreadablePath); err != nil {
		tests = append(tests, struct {
			name     string
			location string
			want     string
		}{name: "unreadable file", location: "unreadable.go:1", want: "unreadable.go (location unverified)"})
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := verifyFindingLocation(root, test.location)
			if got != test.want {
				t.Fatalf("verifyFindingLocation(%q) = %q, want %q", test.location, got, test.want)
			}
		})
	}
}

func TestVerifyVerdictLocationsPreservesVerdictAndFindingKinds(t *testing.T) {
	root := t.TempDir()
	writeLocationTestFile(t, root, "existing.go", "package existing\n")
	input := verdict.Verdict{
		Status:  verdict.Revise,
		Summary: "Review needs changes.",
		Findings: []verdict.Finding{
			{Kind: verdict.Blocking, Location: "missing.go:7", Issue: "Fix the error path."},
			{Kind: verdict.Nit, Location: "existing.go:1", Issue: "Rename the helper."},
		},
	}

	got := verifyVerdictLocations(root, input)
	if got.Status != input.Status || len(got.Findings) != len(input.Findings) {
		t.Fatalf("checked verdict = %#v, want status %q and %d findings", got, input.Status, len(input.Findings))
	}
	gotKinds := []verdict.FindingKind{got.Findings[0].Kind, got.Findings[1].Kind}
	wantKinds := []verdict.FindingKind{input.Findings[0].Kind, input.Findings[1].Kind}
	if !reflect.DeepEqual(gotKinds, wantKinds) {
		t.Fatalf("finding kinds = %v, want %v", gotKinds, wantKinds)
	}
	if got.Findings[0].Location != "missing.go (location unverified)" || got.Findings[1].Location != "existing.go:1" {
		t.Fatalf("checked locations = (%q, %q), want checked missing and existing locations", got.Findings[0].Location, got.Findings[1].Location)
	}
	if input.Findings[0].Location != "missing.go:7" {
		t.Fatalf("input verdict was mutated: %#v", input.Findings[0])
	}
}

func writeLocationTestFile(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
