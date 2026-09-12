package learn

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSummarizeDiffEmptyBefore(t *testing.T) {
	if got := ExtractCandidates("", "package main\nfunc main() {}\n"); got != nil {
		t.Fatalf("expected nil candidates without a baseline, got %v", got)
	}
}

func TestSummarizeDiffOnlyChangedLine(t *testing.T) {
	before := "package main\n\nfunc main() {\n\tfoo()\n}\n"
	after := "package main\n\nfunc main() {\n\tbar()\n}\n"
	got := ExtractCandidates(before, after)
	if len(got) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(got))
	}
	stmt := got[0].Statement
	if strings.Contains(stmt, "package main") {
		t.Fatalf("statement must not carry unchanged lines: %q", stmt)
	}
	if !strings.Contains(stmt, "bar()") {
		t.Fatalf("statement must carry the added line: %q", stmt)
	}
}

func TestSummarizeDiffNoAddedLines(t *testing.T) {
	// Only a deletion: nothing new to learn, no signal.
	if got := ExtractCandidates("a\nb\nc\n", "a\nc\n"); got != nil {
		t.Fatalf("expected nil candidates when nothing was added, got %v", got)
	}
}

func TestSummarizeDiffRuneSafeTruncation(t *testing.T) {
	long := strings.Repeat("café ", 60)
	got := ExtractCandidates("x", long)
	if len(got) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(got))
	}
	stmt := got[0].Statement
	if !utf8.ValidString(stmt) {
		t.Fatalf("truncation split a rune: %q", stmt)
	}
	if n := utf8.RuneCountInString(stmt); n > 161 {
		t.Fatalf("statement exceeds 160 runes + ellipsis: %d runes", n)
	}
	if !strings.HasSuffix(stmt, "…") {
		t.Fatalf("truncated statement must end with an ellipsis: %q", stmt)
	}
}
