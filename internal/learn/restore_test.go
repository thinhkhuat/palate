package learn

import (
	"strings"
	"testing"

	"github.com/thinhkhuat/palate/internal/config"
)

// TestRestoreReopensRetired covers the retired-row branch of phase-02 req 11:
// valid_until clears, last_reinforced_at bumps, and a restore event with
// was="retired" lands in the events log.
func TestRestoreReopensRetired(t *testing.T) {
	db, _ := testDB(t)
	insertPrefFull(t, db, "r1", "General", "Prefers tabs", "DISTILLED", 0.5, "[]")
	retirePref(t, db, "r1")
	setLastReinforced(t, db, "r1", "2020-01-01T00:00:00Z")

	found, err := Restore(db, "r1")
	if err != nil || !found {
		t.Fatalf("Restore: found=%v err=%v", found, err)
	}
	var validUntil *string
	var lastReinforced string
	if err := db.QueryRow(`SELECT valid_until, last_reinforced_at FROM preferences WHERE id='r1'`).Scan(&validUntil, &lastReinforced); err != nil {
		t.Fatal(err)
	}
	if validUntil != nil {
		t.Fatalf("valid_until must clear, got %v", *validUntil)
	}
	if lastReinforced == "2020-01-01T00:00:00Z" {
		t.Fatal("last_reinforced_at must bump to now")
	}
	var n int
	_ = db.QueryRow(`SELECT count(*) FROM events WHERE event_type='restore' AND payload LIKE '%"was":"retired"%' AND payload LIKE '%r1%'`).Scan(&n)
	if n != 1 {
		t.Fatalf("expected one restore event, got %d", n)
	}
}

// TestRestoreCandidatePromotes covers the live-but-excluded branch: a
// DISTILLED, project-tagged row promotes to STATED with the tag dropped, and
// a restore event with was="candidate" records the prior tier — so it enters
// the profile at the next render regardless of its session count.
func TestRestoreCandidatePromotes(t *testing.T) {
	db, _ := testDB(t)
	insertPrefFull(t, db, "c1", "General", "Prefers spaces", "DISTILLED", 0.5, `["llm_distilled","project_specific"]`)

	found, err := Restore(db, "c1")
	if err != nil || !found {
		t.Fatalf("Restore: found=%v err=%v", found, err)
	}
	var tier, prov string
	if err := db.QueryRow(`SELECT trust_tier, provenance FROM preferences WHERE id='c1'`).Scan(&tier, &prov); err != nil {
		t.Fatal(err)
	}
	if tier != "STATED" {
		t.Fatalf("trust_tier = %q, want STATED", tier)
	}
	if strings.Contains(prov, TagProjectSpecific) {
		t.Fatalf("project_specific tag must be dropped, got %q", prov)
	}
	var n int
	_ = db.QueryRow(`SELECT count(*) FROM events WHERE event_type='restore' AND payload LIKE '%"was":"candidate"%' AND payload LIKE '%"from_tier":"DISTILLED"%'`).Scan(&n)
	if n != 1 {
		t.Fatalf("expected one restore event naming the prior tier, got %d", n)
	}

	// SelectProfile must accept it now: STATED bypasses the session-count
	// eligibility floor that excluded it before.
	profile, err := SelectProfile(db, config.Default(), 2000)
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, p := range profile.In {
		if p.ID == "c1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("restored candidate must enter the profile: excluded=%v", profile.Excluded)
	}
}

// TestRestoreUnknownIDErrors: no row named id at all.
func TestRestoreUnknownIDErrors(t *testing.T) {
	db, _ := testDB(t)
	if _, err := Restore(db, "does-not-exist"); err == nil {
		t.Fatal("Restore of an unknown id must return an error")
	}
}
