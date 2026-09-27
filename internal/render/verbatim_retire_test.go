package render

import (
	"strings"
	"testing"

	"github.com/thinhkhuat/palate/internal/config"
)

// TestRenderHasNoMetadataSuffix: command-code parity — bullets carry the
// statement and its confidence only. Source and trust tier stay in the DB
// (queryable via recall -v), never in agent-visible bytes.
func TestRenderHasNoMetadataSuffix(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Exec(`INSERT INTO preferences(id, category, statement, confidence, source, trust_tier) VALUES('p1','General','Use tabs',0.8,'explicit_prefer','STATED')`); err != nil {
		t.Fatal(err)
	}
	out, err := Render(db, config.Default(), RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "- Use tabs. Confidence: 0.80\n") {
		t.Fatalf("command-code bullet shape missing: %q", out)
	}
	for _, leak := range []string{"(explicit_prefer", "STATED)", "DISTILLED)", "IMPORTED)"} {
		if strings.Contains(out, leak) {
			t.Fatalf("internal metadata leaked into render: %q in %q", leak, out)
		}
	}
}

// TestRenderSortsByConfidence: within a category the strongest taste reads
// first, so prewarm truncation (which drops trailing lines) drops the weakest
// rows first instead of the file-order accident.
func TestRenderSortsByConfidence(t *testing.T) {
	db := openTestDB(t)
	for _, r := range []struct {
		id, stmt string
		conf     float64
	}{
		{"w1", "Weakest habit", 0.30},
		{"s1", "Strongest habit", 0.90},
		{"m1", "Middle habit", 0.60},
	} {
		if _, err := db.Exec(`INSERT INTO preferences(id, category, statement, confidence, source, trust_tier) VALUES(?, 'Workflow', ?, ?, 'explicit_prefer', 'STATED')`, r.id, r.stmt, r.conf); err != nil {
			t.Fatal(err)
		}
	}
	out, err := Render(db, config.Default(), RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	si, mi, wi := strings.Index(out, "Strongest habit"), strings.Index(out, "Middle habit"), strings.Index(out, "Weakest habit")
	if si < 0 || mi < 0 || wi < 0 {
		t.Fatalf("all three rows must render: %q", out)
	}
	if !(si < mi && mi < wi) {
		t.Fatalf("rows must sort confidence-desc, got order s=%d m=%d w=%d in:\n%s", si, mi, wi, out)
	}
}
