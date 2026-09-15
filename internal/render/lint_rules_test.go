package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLintRejectsTruncatedAndFragmentBullets: the retired verbatim diff path
// wrote bullets whose statement was cut mid-sentence ("prefers: <code>…") —
// the full text was lost, so the file lied about what was learned. Lint now
// rejects a truncated statement and a fragment too short to be a preference.
func TestLintRejectsTruncatedAndFragmentBullets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "TASTE.md")

	_ = os.WriteFile(path, []byte("# Taste\n\n## General\n\n- Prefers: TABLE_SEP_RE = re.compile(r\"^\\s*\\|\") / NUMBERED… Confidence: 0.30\n"), 0o644)
	diags := Lint(path)
	if len(diags) == 0 {
		t.Fatal("a truncated statement must be flagged")
	}
	if !strings.Contains(strings.Join(diags, "\n"), "truncated statement") {
		t.Fatalf("expected a truncation diagnostic, got %v", diags)
	}

	_ = os.WriteFile(path, []byte("# Taste\n\n## General\n\n- prefers: Confidence: 0.30\n"), 0o644)
	if diags := Lint(path); len(diags) == 0 {
		t.Fatal("a fragment bullet must be flagged")
	}

	// Control: the same file minus the defect lints clean, so the gate fails
	// for the intended reason and not on any bullet at all.
	_ = os.WriteFile(path, []byte("# Taste\n\n## General\n\n- Prefers early returns over nested conditionals. Confidence: 0.60\n"), 0o644)
	if diags := Lint(path); len(diags) != 0 {
		t.Fatalf("clean bullet must lint clean, got %v", diags)
	}
}
