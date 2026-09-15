package consolidate

import (
	"strings"
	"testing"

	"github.com/thinhkhuat/palate/internal/learn"
)

func TestBuildUserPromptRulebookMarker(t *testing.T) {
	rows := []learn.Preference{
		{ID: "a", Category: "Testing", Statement: "Untagged row.", Provenance: `["stated_preference"]`},
		{ID: "b", Category: "Testing", Statement: "Tagged row.", Provenance: `["stated_preference","rulebook_overlap"]`},
	}
	out := BuildUserPrompt(rows, "Always lint before commit.")
	if got := strings.Count(out, "| rulebook"); got != 1 {
		t.Fatalf("exactly the tagged row carries the marker, got %d in: %s", got, out)
	}
	if !strings.Contains(out, "CURATED RULEBOOK") || !strings.Contains(out, "Always lint before commit.") {
		t.Fatalf("a configured rulebook must ride along: %s", out)
	}
	out = BuildUserPrompt(rows, "")
	if strings.Contains(out, "CURATED RULEBOOK") {
		t.Fatal("no configured rulebook: the section must be omitted")
	}
}
