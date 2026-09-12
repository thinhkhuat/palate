package storage

import "testing"

func TestForgetDeletesPreference(t *testing.T) {
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
	if count != 0 {
		t.Fatalf("preference row still present after Forget")
	}
	var events int
	_ = db.QueryRow(`SELECT count(*) FROM events WHERE event_type='forget' AND payload LIKE '%preferences%'`).Scan(&events)
	if events != 1 {
		t.Fatalf("expected one forget event for the preference, got %d", events)
	}
}

func TestForgetDeletesPreferenceWithConflict(t *testing.T) {
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
		t.Fatalf("Forget(winner) with a referencing conflicts row: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM preferences WHERE id=?`, winner).Scan(&count); err != nil {
		t.Fatalf("verify query: %v", err)
	}
	if count != 0 {
		t.Fatalf("preference row still present after Forget")
	}
}

func TestForgetUnknownIDStillErrors(t *testing.T) {
	db := openMem(t)
	if err := Forget(db, "does-not-exist"); err == nil {
		t.Fatal("Forget of an unknown id must return an error")
	}
}
