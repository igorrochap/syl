package orchestration

import (
	"testing"

	"github.com/igorrochap/syl/internal/sylhome"
)

func testSylHome(t *testing.T, path string) sylhome.Dir {
	t.Helper()
	dir, err := sylhome.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
