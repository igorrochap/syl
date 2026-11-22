package initializer

import (
	"os"
	"path/filepath"
	"testing"
)

const projectVerifyRecipeContents = "# Project verification\n\nRun the app with make dev.\n"

func writeProjectVerifyRecipe(t *testing.T, projectRoot string) string {
	t.Helper()
	recipePath := filepath.Join(projectRoot, ".syl", "verify.md")
	if err := os.MkdirAll(filepath.Dir(recipePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recipePath, []byte(projectVerifyRecipeContents), 0o644); err != nil {
		t.Fatal(err)
	}
	return recipePath
}

func assertProjectVerifyRecipeUnchanged(t *testing.T, recipePath string) {
	t.Helper()
	contents, err := os.ReadFile(recipePath)
	if err != nil || string(contents) != projectVerifyRecipeContents {
		t.Fatalf("project recipe = %q, error %v; want unchanged", contents, err)
	}
}
