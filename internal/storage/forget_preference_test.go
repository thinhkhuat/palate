package storage

import "testing"

// TestForgetSoftRetiresPreference: forget on a preference sets valid_until,
// resolves its open conflicts and appends a forget event naming the table
// and the reason — the row itself stays in the DB so learn.Restore
// (palate restore) can bring it back (phase-02 req 10).
func TestForgetSoftRetiresPreference(t *testing.T) {
	db := openMem(t)
	id := "pref-test-1"
	if _, err := db.Exec(`INSERT INTO preferences(id, category, statement) VALUES(?,?,?)`, id, "General", "prefers tabs"); err != nil {
		t.Fatalf("seed insert: %v", err)
	}
	if err := Forget(db, id); err != nil {
		t.Fatalf("Forget(%q): %v", id, err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM preferences WHERE id=?`, id).Scan(&count); err != nil {
		t.Fatalf("verify query: %v", err)
	}
	if count != 1 {
		t.Fatalf("a soft forget must leave the row in the DB, got count=%d", count)
	}
	var validUntil *string
	if err := db.QueryRow(`SELECT valid_until FROM preferences WHERE id=?`, id).Scan(&validUntil); err != nil {
		t.Fatal(err)
	}
	if validUntil == nil {
		t.Fatal("valid_until must be set")
	}
	var events int
	_ = db.QueryRow(`SELECT count(*) FROM events WHERE event_type='forget' AND payload LIKE '%preferences%' AND payload LIKE '%owner forget%'`).Scan(&events)
	if events != 1 {
		t.Fatalf("expected one forget event with the owner-forget reason, got %d", events)
	}
	// A second forget of an already-retired row must not find a live match.
	if err := Forget(db, id); err == nil {
		t.Fatal("forgetting an already-retired preference must error, not silently re-fire")
	}
}

func TestForgetResolvesOpenConflict(t *testing.T) {
	db := openMem(t)
	winner, loser := "pref-winner", "pref-loser"
	for _, row := range [][]any{{winner, "tabs", 0.8}, {loser, "spaces", 0.3}} {
		if _, err := db.Exec(`INSERT INTO preferences(id, category, statement, confidence) VALUES(?,?,?,?)`, row[0], "General", row[1], row[2]); err != nil {
			t.Fatalf("seed %v: %v", row[0], err)
		}
	}
	if _, err := db.Exec(`INSERT INTO conflicts(id, preference_id, conflicting_id) VALUES(?,?,?)`, "conf-1", winner, loser); err != nil {
		t.Fatalf("seed conflict: %v", err)
	}
	if err := Forget(db, winner); err != nil {
		t.Fatalf("Forget(winner): %v", err)
	}
	var resolvedAt *string
	if err := db.QueryRow(`SELECT resolved_at FROM conflicts WHERE id='conf-1'`).Scan(&resolvedAt); err != nil {
		t.Fatal(err)
	}
	if resolvedAt == nil {
		t.Fatal("forgetting a side of an open conflict must resolve it")
	}
}

func TestForgetUnknownIDStillErrors(t *testing.T) {
	db := openMem(t)
	if err := Forget(db, "does-not-exist"); err == nil {
		t.Fatal("Forget of an unknown id must return an error")
	}
}

// TestPurgeHardDeletesPreference: --purge keeps the pre-phase-02 hard delete
// (row and its conflicts gone, one forget event naming "purge" as the
// reason) — the escape hatch for when the owner wants a row truly gone.
func TestPurgeHardDeletesPreference(t *testing.T) {
	db := openMem(t)
	winner, loser := "purge-winner", "purge-loser"
	for _, row := range [][]any{{winner, "tabs", 0.8}, {loser, "spaces", 0.3}} {
		if _, err := db.Exec(`INSERT INTO preferences(id, category, statement, confidence) VALUES(?,?,?,?)`, row[0], "General", row[1], row[2]); err != nil {
			t.Fatalf("seed %v: %v", row[0], err)
		}
	}
	if _, err := db.Exec(`INSERT INTO conflicts(id, preference_id, conflicting_id) VALUES(?,?,?)`, "conf-purge", winner, loser); err != nil {
		t.Fatalf("seed conflict: %v", err)
	}
	if err := Purge(db, winner); err != nil {
		t.Fatalf("Purge(%q): %v", winner, err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM preferences WHERE id=?`, winner).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("preference row still present after Purge")
	}
	var conflicts int
	_ = db.QueryRow(`SELECT count(*) FROM conflicts WHERE id='conf-purge'`).Scan(&conflicts)
	if conflicts != 0 {
		t.Fatal("conflicts row still present after Purge")
	}
	var events int
	_ = db.QueryRow(`SELECT count(*) FROM events WHERE event_type='forget' AND payload LIKE '%"reason":"purge"%'`).Scan(&events)
	if events != 1 {
		t.Fatalf("expected one forget event naming purge as the reason, got %d", events)
	}
}

func TestPurgeUnknownIDStillErrors(t *testing.T) {
	db := openMem(t)
	if err := Purge(db, "does-not-exist"); err == nil {
		t.Fatal("Purge of an unknown id must return an error")
	}
}
