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
	out := BuildUserPrompt(nil, rows, "Always lint before commit.", 2000, 10, nil, nil)
	if got := strings.Count(out, "| rulebook"); got != 1 {
		t.Fatalf("exactly the tagged row carries the marker, got %d in: %s", got, out)
	}
	if !strings.Contains(out, "CURATED RULEBOOK") || !strings.Contains(out, "Always lint before commit.") {
		t.Fatalf("a configured rulebook must ride along: %s", out)
	}
	out = BuildUserPrompt(nil, rows, "", 2000, 10, nil, nil)
	if strings.Contains(out, "CURATED RULEBOOK") {
		t.Fatal("no configured rulebook: the section must be omitted")
	}
}

// The profile and the batch carry the markers the model needs: stated rows are
// flagged so they are never rewritten, the session count backs merge and
// retire calls, and a conflicting pair names its other side.
func TestBuildUserPromptMarkers(t *testing.T) {
	profile := []learn.Preference{{ID: "p1", Category: "Testing", Statement: "Stated row.", TrustTier: "STATED"}}
	batch := []learn.Preference{{ID: "b1", Category: "Testing", Statement: "Candidate row.", TrustTier: "DISTILLED"}}
	out := BuildUserPrompt(profile, batch, "", 2000, 10, map[string]int{"p1": 3, "b1": 1}, map[string]string{"b1": "p1"})
	for _, want := range []string{"| sessions=3 | stated", "| sessions=1 | conflicts-with=p1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing marker %q in: %s", want, out)
		}
	}
	if strings.Count(out, "| stated") != 1 {
		t.Fatalf("only the STATED row carries the stated marker: %s", out)
	}
}
