package render

import (
	"strings"
	"testing"

	"github.com/thinhkhuat/palate/internal/config"
)

func TestBodySuppressesRulebookRows(t *testing.T) {
	db := openTestDB(t)
	insert := func(id, statement, provenance string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO preferences(id, category, statement, provenance) VALUES(?,?,?,?)`,
			id, "Testing", statement, provenance); err != nil {
			t.Fatal(err)
		}
	}
	insert("keep", "Prefers table-driven tests.", `["stated_preference"]`)
	insert("drop", "Prefers table-driven tests with mocks.", `["stated_preference","rulebook_overlap"]`)
	body, n, err := Body(db, config.Default(), RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rulebook-tagged rows must be suppressed from the render: n=%d body=%q", n, body)
	}
	if !strings.Contains(body, "Prefers table-driven tests.") || strings.Contains(body, "with mocks") {
		t.Fatalf("wrong row suppressed: %q", body)
	}
}
