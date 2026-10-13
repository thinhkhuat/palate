package learn

import (
	"strconv"
	"testing"
)

// TestCompareSet covers phase-03's CompareSet table: the profile is always
// included, an FTS-matching row outside the profile is added, an unrelated
// row is not, the total stays bounded, diacritics fold, FTS operator
// characters in free text never error, and empty text degrades to the
// profile alone.
func TestCompareSet(t *testing.T) {
	t.Run("profile row always included", func(t *testing.T) {
		db, cfg := testDB(t)
		insertPrefFull(t, db, "p1", "General", "Prefers tabs over spaces.", "STATED", 0.7, "[]")
		insertEvent(t, db, "create", "p1", "sess1", nowStr(0))

		set, err := CompareSet(db, cfg, "totally unrelated query text with no overlap")
		if err != nil {
			t.Fatal(err)
		}
		if !containsID(set, "p1") {
			t.Fatalf("a STATED profile row must always be in the compare set: %+v", idsOf(set))
		}
	})

	t.Run("FTS match outside the profile is included, unrelated row is not", func(t *testing.T) {
		db, cfg := testDB(t)
		// Always a profile member (STATED), unrelated to the query text.
		insertPrefFull(t, db, "anchor", "General", "Prefers early returns.", "STATED", 0.7, "[]")
		insertEvent(t, db, "create", "anchor", "sess1", nowStr(0))
		// DISTILLED, one session only: excluded from the profile
		// ("1 session (needs 2)"), but its statement shares a rare word with
		// the query text — CompareSet must still surface it.
		insertPrefFull(t, db, "match", "Tooling", "Prefers deploying with kubernetes manifests.", "DISTILLED", 0.5, "[]")
		insertEvent(t, db, "create", "match", "", nowStr(0))
		// DISTILLED, one session only, and shares no word with the query
		// text: must stay out of the compare set entirely.
		insertPrefFull(t, db, "unrelated", "General", "Prefers dark mode themes.", "DISTILLED", 0.5, "[]")
		insertEvent(t, db, "create", "unrelated", "", nowStr(0))

		set, err := CompareSet(db, cfg, "deploying kubernetes clusters is annoying today")
		if err != nil {
			t.Fatal(err)
		}
		if !containsID(set, "anchor") {
			t.Fatalf("profile row missing: %+v", idsOf(set))
		}
		if !containsID(set, "match") {
			t.Fatalf("an FTS-matching row outside the profile must be added: %+v", idsOf(set))
		}
		if containsID(set, "unrelated") {
			t.Fatalf("a non-matching, non-profile row must not be added: %+v", idsOf(set))
		}
	})

	t.Run("set is bounded by profile plus compareTopK", func(t *testing.T) {
		db, cfg := testDB(t)
		insertPrefFull(t, db, "anchor", "General", "Prefers early returns.", "STATED", 0.7, "[]")
		insertEvent(t, db, "create", "anchor", "sess1", nowStr(0))
		for i := 0; i < compareTopK+10; i++ {
			id := "row" + strconv.Itoa(i)
			insertPrefFull(t, db, id, "Tooling", "Prefers zephyr build tool variant "+strconv.Itoa(i)+".", "DISTILLED", 0.5, "[]")
			insertEvent(t, db, "create", id, "", nowStr(0))
		}
		set, err := CompareSet(db, cfg, "zephyr build tool preference across the board")
		if err != nil {
			t.Fatal(err)
		}
		if len(set) > 1+compareTopK {
			t.Fatalf("compare set = %d rows, want <= profile(1) + compareTopK(%d)", len(set), compareTopK)
		}
	})

	t.Run("Vietnamese diacritics fold (unicode61)", func(t *testing.T) {
		db, cfg := testDB(t)
		insertPrefFull(t, db, "vn1", "Testing", "Ưu tiên viết test theo kiểu bảng.", "DISTILLED", 0.5, "[]")
		insertEvent(t, db, "create", "vn1", "", nowStr(0))

		set, err := CompareSet(db, cfg, "uu tien viet test cho ro rang")
		if err != nil {
			t.Fatal(err)
		}
		if !containsID(set, "vn1") {
			t.Fatalf("a diacritic-folded token must still match under unicode61: %+v", idsOf(set))
		}
	})

	t.Run("FTS operator characters in free text never error", func(t *testing.T) {
		db, cfg := testDB(t)
		insertPrefFull(t, db, "p1", "General", "Prefers tabs over spaces.", "STATED", 0.7, "[]")
		insertEvent(t, db, "create", "p1", "sess1", nowStr(0))

		if _, err := CompareSet(db, cfg, `foo-bar "quoted phrase" *star* key:value NEAR(1)`); err != nil {
			t.Fatalf("FTS syntax characters in free text must never error: %v", err)
		}
	})

	t.Run("empty text falls back to the profile alone", func(t *testing.T) {
		db, cfg := testDB(t)
		insertPrefFull(t, db, "p1", "General", "Prefers tabs over spaces.", "STATED", 0.7, "[]")
		insertEvent(t, db, "create", "p1", "sess1", nowStr(0))
		insertPrefFull(t, db, "excluded", "General", "Prefers something else entirely.", "DISTILLED", 0.5, "[]")
		insertEvent(t, db, "create", "excluded", "", nowStr(0))

		set, err := CompareSet(db, cfg, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(set) != 1 || set[0].ID != "p1" {
			t.Fatalf("empty text must yield the profile alone, got %+v", idsOf(set))
		}
	})
}

func containsID(ps []Preference, id string) bool {
	for _, p := range ps {
		if p.ID == id {
			return true
		}
	}
	return false
}
