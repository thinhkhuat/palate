package learn

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The verbatim "prefers: <added lines>" diff path is retired (2026-09-15):
// these tests pin the OLD contract only as documentation of what was removed.
// ExtractCandidates must now return nil for every shape below; the live
// retirement assertions live in verbatim_retire_test.go.
func TestSummarizeDiffEmptyBefore(t *testing.T) {
	if got := ExtractCandidates("", "package main\nfunc main() {}\n"); got != nil {
		t.Fatalf("expected nil candidates without a baseline, got %v", got)
	}
}

func TestSummarizeDiffOnlyChangedLine(t *testing.T) {
	before := "package main\n\nfunc main() {\n\tfoo()\n}\n"
	after := "package main\n\nfunc main() {\n\tbar()\n}\n"
	if got := ExtractCandidates(before, after); got != nil {
		t.Fatalf("retired path must yield nil, got %v", got)
	}
}

func TestSummarizeDiffNoAddedLines(t *testing.T) {
	// Only a deletion: nothing new to learn, no signal.
	if got := ExtractCandidates("a\nb\nc\n", "a\nc\n"); got != nil {
		t.Fatalf("expected nil candidates when nothing was added, got %v", got)
	}
}

func TestSummarizeDiffRuneSafeTruncation(t *testing.T) {
	// Retired: the rune-safe truncation contract moved to TruncateRunes itself
	// (see TestRetiredTruncationStaysRuneSafe in verbatim_retire_test.go).
	long := strings.Repeat("café ", 60)
	if got := ExtractCandidates("x", long); got != nil {
		t.Fatalf("retired path must yield nil, got %v", got)
	}
	_ = utf8.ValidString
	_ = strings.HasSuffix
}
