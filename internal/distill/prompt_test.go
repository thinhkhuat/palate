package distill

import (
	"strings"
	"testing"

	"github.com/thinhkhuat/palate/internal/learn"
)

func TestFormatExistingPreferences(t *testing.T) {
	if got := FormatExistingPreferences(nil); got != "" {
		t.Fatalf("empty prefs: got %q, want empty string", got)
	}
	prefs := []learn.Preference{
		{Category: "Testing", Statement: "Prefers table-driven tests.", Confidence: 0.9},
		{Category: "General", Statement: "Uses tabs.", Confidence: 0.42},
	}
	got := FormatExistingPreferences(prefs)
	want := "- [Testing] Prefers table-driven tests. (confidence 0.90)\n- [General] Uses tabs. (confidence 0.42)"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBuildUserPromptSections(t *testing.T) {
	out := BuildUserPrompt("- [Testing] Prefers table-driven tests. (confidence 0.90)", "old evidence", "new evidence")

	sections := []string{
		"## CURRENT PREFERENCES",
		"- [Testing] Prefers table-driven tests. (confidence 0.90)",
		"## PREVIOUSLY ANALYZED (context only)",
		"## NEW EVIDENCE (learn ONLY from this)",
	}
	lastIdx := -1
	for _, s := range sections {
		idx := strings.Index(out, s)
		if idx < 0 {
			t.Fatalf("missing section %q in output:\n%s", s, out)
		}
		if idx <= lastIdx {
			t.Fatalf("section %q out of order in output:\n%s", s, out)
		}
		lastIdx = idx
	}

	if !strings.Contains(out, "<evidence>\nold evidence\n</evidence>") {
		t.Fatalf("PREVIOUSLY ANALYZED not wrapped in <evidence>: %s", out)
	}
	if !strings.Contains(out, "<evidence>\nnew evidence\n</evidence>") {
		t.Fatalf("NEW EVIDENCE not wrapped in <evidence>: %s", out)
	}
	if strings.Contains(out, "<evidence>\n- [Testing]") {
		t.Fatalf("CURRENT PREFERENCES must stay unwrapped: %s", out)
	}
}

func TestBuildUserPromptEmptyExisting(t *testing.T) {
	out := BuildUserPrompt("", "prev", "new")
	if !strings.Contains(out, "## CURRENT PREFERENCES\n(none yet)") {
		t.Fatalf("expected placeholder for empty CURRENT PREFERENCES: %s", out)
	}
}

func TestScanAndRedactEvidence(t *testing.T) {
	lines := []string{
		"the user prefers early returns",
		"leaked key sk-ant-api03-abcdefghijklmnopqrstuvwxyz1234567890 in a script",
		"another clean line",
	}
	redacted, dropped := ScanAndRedactEvidence(lines)
	if dropped != 1 {
		t.Fatalf("dropped = %d, want 1", dropped)
	}
	if len(redacted) != 3 {
		t.Fatalf("redacted has %d lines, want 3", len(redacted))
	}
	if redacted[0] != lines[0] || redacted[2] != lines[2] {
		t.Fatalf("clean lines must pass through unchanged: %v", redacted)
	}
	if redacted[1] != "[redacted secret]" {
		t.Fatalf("secret line = %q, want [redacted secret]", redacted[1])
	}
}

func TestScanAndRedactEvidenceNoSecrets(t *testing.T) {
	lines := []string{"line one", "line two"}
	redacted, dropped := ScanAndRedactEvidence(lines)
	if dropped != 0 {
		t.Fatalf("dropped = %d, want 0", dropped)
	}
	for i, l := range lines {
		if redacted[i] != l {
			t.Fatalf("line %d changed unexpectedly: %q != %q", i, redacted[i], l)
		}
	}
}
