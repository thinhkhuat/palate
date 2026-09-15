package learn

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSummarizeDiffRetired(t *testing.T) {
	// The verbatim "prefers: <added lines>" path is retired (2026-09-15):
	// file diffs are work product, not preferences. Any diff shape —
	// code, prose, empty baseline — must yield no candidates.
	cases := [][2]string{
		{"", "package main\nfunc main() {}\n"},
		{"package main\n\nfunc main() {\n\tfoo()\n}\n", "package main\n\nfunc main() {\n\tbar()\n}\n"},
		{"a\nb\nc\n", "a\nc\n"},
		{"old news paragraph", "old news paragraph plus entirely new reporting content"},
	}
	for i, c := range cases {
		if got := ExtractCandidates(c[0], c[1]); got != nil {
			t.Fatalf("case %d: retired ExtractCandidates must return nil, got %v", i, got)
		}
	}
	if s := summarizeDiff("a", "b"); s != "" {
		t.Fatalf("retired summarizeDiff must return empty, got %q", s)
	}
	// TruncateRunes stays live for the git-signal path: keep its contract here
	// so the export is not dead code.
	long := strings.Repeat("café ", 60)
	tr := TruncateRunes("prefers: "+long, maxStatementRunes)
	if !utf8.ValidString(tr) {
		t.Fatalf("truncation split a rune: %q", tr)
	}
	if n := utf8.RuneCountInString(tr); n > 161 {
		t.Fatalf("statement exceeds 160 runes + ellipsis: %d runes", n)
	}
}

func TestCodeShapedDiffYieldsNoCandidates(t *testing.T) {
	// Gate G3's positive-shape control: the exact garbage shape found live in
	// TASTE.md (a Go regex literal added by a hand-edit) must not learn.
	before := "package check\n"
	after := "package check\n\nvar TABLE_SEP_RE = re.compile(r\"^\\s*\\|[\\s\\-:|]+\\|?\\s*$\")\n"
	if got := ExtractCandidates(before, after); got != nil {
		t.Fatalf("code-shaped diff must yield no candidates, got %v", got)
	}
	before, after = "report body", "report body\nprefers: *Tiến độ triển khai Công điện số 64/CĐ-TTg tại các địa phương*"
	if got := ExtractCandidates(before, after); got != nil {
		t.Fatalf("news-paragraph diff must yield no candidates, got %v", got)
	}
}

func TestUserDiffTierIsObserved(t *testing.T) {
	// Gate G2: if any user_diff row is ever written again (e.g. via ApplySignal
	// directly), it must land in OBSERVED, never STATED.
	db, cfg := testDB(t)
	p, err := ApplySignal(db, cfg, Signal{
		Type:      SignalUserDiff,
		Statement: "Uses early returns over nested conditionals.",
		Category:  "Workflow",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.TrustTier != "OBSERVED" {
		t.Fatalf("user_diff tier = %q, want OBSERVED", p.TrustTier)
	}
	var tier string
	_ = db.QueryRow(`SELECT trust_tier FROM preferences WHERE id=?`, p.ID).Scan(&tier)
	if tier != "OBSERVED" {
		t.Fatalf("stored user_diff tier = %q, want OBSERVED", tier)
	}
}
