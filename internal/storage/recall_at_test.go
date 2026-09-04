package storage

import (
	"testing"
	"time"
)

func TestRecallAtPinsDecay(t *testing.T) {
	db := openMem(t)
	mk := func(id, stmt, reinforced string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO preferences(id, category, statement, confidence, importance, source, provenance, valid_from, created_at, last_reinforced_at, trust_tier, veracity)
			VALUES(?,?,?,0.9,0.5,'stated_preference','[]',?,?,?,'STATED','stated')`,
			id, "Testing", stmt, reinforced, reinforced, reinforced); err != nil {
			t.Fatal(err)
		}
	}
	mk("pref-old", "decay probe statement alpha", "2026-01-01T00:00:00Z")
	mk("pref-new", "decay probe statement beta", "2026-05-30T00:00:00Z")

	late := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	lateRes, err := RecallAtWithDecay(db, "decay probe statement", 10, true, &late, 0.1, 168)
	if err != nil {
		t.Fatal(err)
	}
	first := ""
	for _, r := range lateRes {
		if r.Table == "preferences" {
			first = r.ID
			break
		}
	}
	if first != "pref-new" {
		t.Fatalf("late as-of must rank the fresher preference first, got %q", first)
	}
	again, err := RecallAtWithDecay(db, "decay probe statement", 10, true, &late, 0.1, 168)
	if err != nil {
		t.Fatal(err)
	}
	var second string
	for _, r := range again {
		if r.Table == "preferences" {
			second = r.ID
			break
		}
	}
	if second != first {
		t.Fatalf("same as-of must be deterministic: %q vs %q", second, first)
	}
}

func TestRecallConflictFilter(t *testing.T) {
	db := openMem(t)
	if _, err := db.Exec(`INSERT INTO preferences(id, category, statement, confidence, importance, source, provenance, valid_from, created_at, last_reinforced_at, trust_tier, veracity)
		VALUES('p1','Testing','conflict filter probe alpha',0.5,0.5,'stated_preference','[]',?,?,?,'STATED','stated')`,
		"2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO preferences(id, category, statement, confidence, importance, source, provenance, valid_from, created_at, last_reinforced_at, trust_tier, veracity)
		VALUES('p2','Testing','conflict filter probe beta',0.9,0.5,'stated_preference','[]',?,?,?,'STATED','stated')`,
		"2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO conflicts(id, preference_id, conflicting_id, reason) VALUES('c1','p1','p2','test')`); err != nil {
		t.Fatal(err)
	}
	with, err := RecallAt(db, "conflict filter probe", 10, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	without, err := RecallAt(db, "conflict filter probe", 10, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	count := func(rs []RecallResult) int {
		n := 0
		for _, r := range rs {
			if r.Table == "preferences" {
				n++
			}
		}
		return n
	}
	if count(with) != 2 {
		t.Fatalf("includeConflicted=true should show both, got %d", count(with))
	}
	ids := map[string]bool{}
	for _, r := range without {
		if r.Table == "preferences" {
			ids[r.ID] = true
		}
	}
	if len(ids) != 1 || !ids["p2"] {
		t.Fatalf("includeConflicted=false should hide only the loser (p1), got %v", ids)
	}
}
