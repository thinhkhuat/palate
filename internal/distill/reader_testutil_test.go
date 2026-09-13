package distill

import (
	"os"
	"path/filepath"
	"testing"
)

// copyFixture copies internal/distill/testdata/<name> to dst, so each
// reader test can Discover/Messages against a private temp directory
// without mutating the shared fixture.
func copyFixture(t *testing.T, name, dst string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
}
