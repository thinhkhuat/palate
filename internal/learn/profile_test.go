package learn

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// --- fixture helpers: direct SQL so session/day/tier/provenance/confidence
// are pinned exactly, independent of ApplySignal's own matching rules. ---

func insertPrefFull(t *testing.T, db *sql.DB, id, category, statement, tier string, confidence float64, provenance string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO preferences(id, category, statement, trust_tier, confidence, provenance) VALUES(?,?,?,?,?,?)`,
		id, category, statement, tier, confidence, provenance); err != nil {
		t.Fatal(err)
	}
}

func retirePref(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE preferences SET valid_until=? WHERE id=?`, "2026-01-01T00:00:00Z", id); err != nil {
		t.Fatal(err)
	}
}

func setProvenance(t *testing.T, db *sql.DB, id, provenanceJSON string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE preferences SET provenance=? WHERE id=?`, provenanceJSON, id); err != nil {
		t.Fatal(err)
	}
}

func setLastReinforced(t *testing.T, db *sql.DB, id, ts string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE preferences SET last_reinforced_at=? WHERE id=?`, ts, id); err != nil {
		t.Fatal(err)
	}
}

func insertEvent(t *testing.T, db *sql.DB, eventType, id, session, createdAt string) {
	t.Helper()
	payload := fmt.Sprintf(`{"id":%q}`, id)
	if session != "" {
		payload = strings.TrimSuffix(payload, "}") + fmt.Sprintf(`,"session":%q}`, session)
	}
	if _, err := db.Exec(`INSERT INTO events(event_type, payload, created_at) VALUES(?,?,?)`, eventType, payload, createdAt); err != nil {
		t.Fatal(err)
	}
}

func insertMembershipEvent(t *testing.T, db *sql.DB, eventType, id string, sessions int, seed bool) {
	t.Helper()
	payload := fmt.Sprintf(`{"id":%q,"sessions":%d,"seed":%t}`, id, sessions, seed)
	if _, err := db.Exec(`INSERT INTO events(event_type, payload) VALUES(?,?)`, eventType, payload); err != nil {
		t.Fatal(err)
	}
}

func insertConflictRow(t *testing.T, db *sql.DB, preferenceID, conflictingID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO conflicts(id, preference_id, conflicting_id, reason) VALUES(?,?,?,?)`, newID(), preferenceID, conflictingID, "contradiction"); err != nil {
		t.Fatal(err)
	}
}

func nowStr(offsetDays int) string {
	return time.Now().UTC().AddDate(0, 0, offsetDays).Format(time.RFC3339)
}

func idsOf(ps []Preference) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.ID
	}
	return out
}

// TestSessionCounts covers phase-01's SessionCounts table.
func TestSessionCounts(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, db *sql.DB)
		id    string
		want  int
	}{
		{
			name: "one create no session",
			setup: func(t *testing.T, db *sql.DB) {
				insertPrefFull(t, db, "a1", "General", "s1", "STATED", 0.5, "[]")
				insertEvent(t, db, "create", "a1", "", "2026-09-01T10:00:00Z")
			},
			id: "a1", want: 1,
		},
		{
			name: "create+reinforce same session key",
			setup: func(t *testing.T, db *sql.DB) {
				insertPrefFull(t, db, "a2", "General", "s2", "STATED", 0.5, "[]")
				insertEvent(t, db, "create", "a2", "sessA", "2026-09-01T10:00:00Z")
				insertEvent(t, db, "reinforce", "a2", "sessA", "2026-09-02T10:00:00Z")
			},
			id: "a2", want: 1,
		},
		{
			name: "create+reinforce other key",
			setup: func(t *testing.T, db *sql.DB) {
				insertPrefFull(t, db, "a3", "General", "s3", "STATED", 0.5, "[]")
				insertEvent(t, db, "create", "a3", "sessA", "2026-09-01T10:00:00Z")
				insertEvent(t, db, "reinforce", "a3", "sessB", "2026-09-02T10:00:00Z")
			},
			id: "a3", want: 2,
		},
		{
			name: "two legacy events same UTC day",
			setup: func(t *testing.T, db *sql.DB) {
				insertPrefFull(t, db, "a4", "General", "s4", "STATED", 0.5, "[]")
				insertEvent(t, db, "create", "a4", "", "2026-09-01T10:00:00Z")
				insertEvent(t, db, "reinforce", "a4", "", "2026-09-01T18:00:00Z")
			},
			id: "a4", want: 1,
		},
		{
			name: "two legacy events different days",
			setup: func(t *testing.T, db *sql.DB) {
				insertPrefFull(t, db, "a5", "General", "s5", "STATED", 0.5, "[]")
				insertEvent(t, db, "create", "a5", "", "2026-09-01T10:00:00Z")
				insertEvent(t, db, "reinforce", "a5", "", "2026-09-02T10:00:00Z")
			},
			id: "a5", want: 2,
		},
		{
			name: "merge survivor unions a closed row's sessions",
			setup: func(t *testing.T, db *sql.DB) {
				insertPrefFull(t, db, "keep", "General", "s6", "STATED", 0.5, "[]")
				insertPrefFull(t, db, "closed", "General", "s6b", "DISTILLED", 0.5, "[]")
				insertEvent(t, db, "create", "keep", "sessA", "2026-09-01T10:00:00Z")
				insertEvent(t, db, "create", "closed", "sessB", "2026-09-05T10:00:00Z")
				retirePref(t, db, "closed")
				setProvenance(t, db, "keep", `["stated_preference","merged_from:[\"closed\"]"]`)
			},
			id: "keep", want: 2,
		},
		{
			name: "merged_from cycle does not hang",
			setup: func(t *testing.T, db *sql.DB) {
				insertPrefFull(t, db, "cyc1", "General", "c1", "STATED", 0.5, "[]")
				insertPrefFull(t, db, "cyc2", "General", "c2", "STATED", 0.5, "[]")
				insertEvent(t, db, "create", "cyc1", "sessA", "2026-09-01T10:00:00Z")
				insertEvent(t, db, "create", "cyc2", "sessB", "2026-09-02T10:00:00Z")
				setProvenance(t, db, "cyc1", `["stated_preference","merged_from:[\"cyc2\"]"]`)
				setProvenance(t, db, "cyc2", `["stated_preference","merged_from:[\"cyc1\"]"]`)
			},
			id: "cyc1", want: 2,
		},
		{
			// red-team M4: a legacy (no-session) event and a real-key event
			// on the same UTC day must not double-count as two sessions.
			name: "legacy day proxy plus real-key reinforce same day collapses (M4)",
			setup: func(t *testing.T, db *sql.DB) {
				insertPrefFull(t, db, "a7", "General", "s7", "STATED", 0.5, "[]")
				insertEvent(t, db, "create", "a7", "", "2026-09-01T10:00:00Z")
				insertEvent(t, db, "reinforce", "a7", "sessX", "2026-09-01T20:00:00Z")
			},
			id: "a7", want: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, _ := testDB(t)
			tc.setup(t, db)
			counts, err := SessionCounts(db)
			if err != nil {
				t.Fatal(err)
			}
			if got := counts[tc.id]; got != tc.want {
				t.Fatalf("SessionCounts[%s] = %d, want %d (all: %+v)", tc.id, got, tc.want, counts)
			}
		})
	}
}

// TestReinforceRecordsSession proves the create/reinforce event plumbing
// phase-01 req 1 depends on: ApplySignal and ApplyJudged write "session" on
// create and reinforce, and an empty session writes no field at all.
func TestReinforceRecordsSession(t *testing.T) {
	db, cfg := testDB(t)

	p, err := ApplySignal(db, cfg, Signal{Type: SignalExplicitPrefer, Statement: "use table-driven tests", Category: "Testing", Session: "sess-A"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertEventSession(t, db, "create", p.ID, "sess-A")

	if _, err := ApplySignal(db, cfg, Signal{Type: SignalExplicitPrefer, Statement: "use table-driven tests", Category: "Testing", Session: "sess-B"}, nil); err != nil {
		t.Fatal(err)
	}
	assertEventSession(t, db, "reinforce", p.ID, "sess-B")

	p2, err := ApplySignal(db, cfg, Signal{Type: SignalExplicitPrefer, Statement: "use early returns", Category: "General"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertNoEventSession(t, db, "create", p2.ID)

	base, err := ApplyJudged(db, cfg, Signal{Type: SignalExplicitPrefer, Session: "sess-C"}, Assessment{Statement: "Prefers named subtests.", Category: "Testing", Confidence: 0.6, Relation: RelationNew, By: "test"})
	if err != nil {
		t.Fatal(err)
	}
	assertEventSession(t, db, "create", base.ID, "sess-C")

	if _, err := ApplyJudged(db, cfg, Signal{Type: SignalExplicitPrefer, Session: "sess-D"}, Assessment{Statement: base.Statement, Category: "Testing", Confidence: 0.6, Relation: RelationSame, TargetID: base.ID, By: "test"}); err != nil {
		t.Fatal(err)
	}
	assertEventSession(t, db, "reinforce", base.ID, "sess-D")

	base2, err := ApplyJudged(db, cfg, Signal{Type: SignalExplicitPrefer}, Assessment{Statement: "Prefers guard clauses.", Category: "Error Handling", Confidence: 0.6, Relation: RelationNew, By: "test"})
	if err != nil {
		t.Fatal(err)
	}
	assertNoEventSession(t, db, "create", base2.ID)
}

func assertEventSession(t *testing.T, db *sql.DB, eventType, id, want string) {
	t.Helper()
	var payload string
	if err := db.QueryRow(`SELECT payload FROM events WHERE event_type=? AND json_extract(payload,'$.id')=? ORDER BY id DESC LIMIT 1`, eventType, id).Scan(&payload); err != nil {
		t.Fatalf("query %s event for %s: %v", eventType, id, err)
	}
	var got struct {
		Session string `json:"session"`
	}
	if err := json.Unmarshal([]byte(payload), &got); err != nil {
		t.Fatal(err)
	}
	if got.Session != want {
		t.Fatalf("%s event session = %q, want %q (payload=%s)", eventType, got.Session, want, payload)
	}
}

func assertNoEventSession(t *testing.T, db *sql.DB, eventType, id string) {
	t.Helper()
	var payload string
	if err := db.QueryRow(`SELECT payload FROM events WHERE event_type=? AND json_extract(payload,'$.id')=? ORDER BY id DESC LIMIT 1`, eventType, id).Scan(&payload); err != nil {
		t.Fatalf("query %s event for %s: %v", eventType, id, err)
	}
	if strings.Contains(payload, `"session"`) {
		t.Fatalf("empty session must write no field, got %s", payload)
	}
}

// TestEligibility covers phase-01's eligibility table.
func TestEligibility(t *testing.T) {
	db, cfg := testDB(t)

	insertPrefFull(t, db, "stated1", "General", "STATED text", "STATED", 0.6, "[]")
	insertEvent(t, db, "create", "stated1", "", nowStr(0))

	insertPrefFull(t, db, "dist1", "General", "distilled text one", "DISTILLED", 0.5, "[]")
	insertEvent(t, db, "create", "dist1", "sessA", nowStr(0))

	insertPrefFull(t, db, "dist2", "General", "distilled text two", "DISTILLED", 0.5, "[]")
	insertEvent(t, db, "create", "dist2", "sessA", nowStr(0))
	insertEvent(t, db, "reinforce", "dist2", "sessB", nowStr(1))

	insertPrefFull(t, db, "rb1", "General", "rulebook text", "STATED", 0.6, `["rulebook_overlap"]`)
	insertEvent(t, db, "create", "rb1", "", nowStr(0))

	insertPrefFull(t, db, "proj1", "General", "project text", "STATED", 0.6, `["project_specific"]`)
	insertEvent(t, db, "create", "proj1", "", nowStr(0))

	insertPrefFull(t, db, "winA", "General", "winner statement", "STATED", 0.8, "[]")
	insertPrefFull(t, db, "loseA", "General", "loser statement", "STATED", 0.3, "[]")
	insertEvent(t, db, "create", "winA", "", nowStr(0))
	insertEvent(t, db, "create", "loseA", "", nowStr(0))
	insertConflictRow(t, db, "winA", "loseA")

	insertPrefFull(t, db, "eqA", "General", "equal statement a", "STATED", 0.5, "[]")
	insertPrefFull(t, db, "eqB", "General", "equal statement b", "STATED", 0.5, "[]")
	insertEvent(t, db, "create", "eqA", "", nowStr(0))
	insertEvent(t, db, "create", "eqB", "", nowStr(0))
	insertConflictRow(t, db, "eqA", "eqB")

	profile, err := SelectProfile(db, cfg, 100000)
	if err != nil {
		t.Fatal(err)
	}
	in := map[string]bool{}
	for _, p := range profile.In {
		in[p.ID] = true
	}

	if !in["stated1"] {
		t.Fatal("STATED with fewer than 2 sessions must still be eligible")
	}
	if reason := profile.Excluded["dist1"]; reason != "1 session (needs 2)" {
		t.Fatalf("dist1 excluded reason = %q", reason)
	}
	if !in["dist2"] {
		t.Fatal("DISTILLED with 2 sessions must be in")
	}
	if reason := profile.Excluded["rb1"]; reason != "restates rulebook" {
		t.Fatalf("rb1 excluded reason = %q", reason)
	}
	if reason := profile.Excluded["proj1"]; reason != "project-specific" {
		t.Fatalf("proj1 excluded reason = %q", reason)
	}
	if !in["winA"] {
		t.Fatal("the conflict winner must be in (the stale-side render bug case)")
	}
	if reason := profile.Excluded["loseA"]; reason != "conflict loser to winA" {
		t.Fatalf("loseA excluded reason = %q", reason)
	}
	if !in["eqA"] || !in["eqB"] {
		t.Fatal("an equal-confidence conflict must keep both sides eligible (red-team L2)")
	}

	cfg2 := cfg
	cfg2.Profile.ExcludeProjectSpecific = false
	profile2, err := SelectProfile(db, cfg2, 100000)
	if err != nil {
		t.Fatal(err)
	}
	in2 := map[string]bool{}
	for _, p := range profile2.In {
		in2[p.ID] = true
	}
	if !in2["proj1"] {
		t.Fatal("exclude_project_specific=false must include the project-tagged row")
	}
}

// TestSelectProfileRankingAndBudget isolates each of the six tie-break
// levels, the budget cut, per-category heading cost, and skip-and-continue.
func TestSelectProfileRankingAndBudget(t *testing.T) {
	twoSessions := func(t *testing.T, db *sql.DB, id string) {
		insertEvent(t, db, "create", id, "s1", nowStr(0))
		insertEvent(t, db, "reinforce", id, "s2", nowStr(0))
	}

	t.Run("STATED before DISTILLED", func(t *testing.T) {
		db, cfg := testDB(t)
		insertPrefFull(t, db, "z_stated", "Cat", "same text", "STATED", 0.5, "[]")
		insertPrefFull(t, db, "a_distilled", "Cat", "same text", "DISTILLED", 0.5, "[]")
		twoSessions(t, db, "z_stated")
		twoSessions(t, db, "a_distilled")
		p, err := SelectProfile(db, cfg, 100000)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.In) != 2 || p.In[0].ID != "z_stated" {
			t.Fatalf("STATED must rank before DISTILLED regardless of id: %v", idsOf(p.In))
		}
	})

	t.Run("non-stale-cut before stale-cut", func(t *testing.T) {
		db, cfg := testDB(t)
		insertPrefFull(t, db, "fresh1", "Cat", "text one", "DISTILLED", 0.5, "[]")
		insertPrefFull(t, db, "stale1", "Cat", "text two", "DISTILLED", 0.5, "[]")
		twoSessions(t, db, "fresh1")
		twoSessions(t, db, "stale1")
		insertMembershipEvent(t, db, "profile_cut", "stale1", 2, false)
		p, err := SelectProfile(db, cfg, 100000)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.In) != 2 || p.In[0].ID != "fresh1" {
			t.Fatalf("non-stale-cut must rank before stale-cut: %v", idsOf(p.In))
		}
	})

	t.Run("more distinct sessions ranks first", func(t *testing.T) {
		db, cfg := testDB(t)
		insertPrefFull(t, db, "two_sess", "Cat", "text a", "DISTILLED", 0.5, "[]")
		insertPrefFull(t, db, "three_sess", "Cat", "text b", "DISTILLED", 0.5, "[]")
		twoSessions(t, db, "two_sess")
		insertEvent(t, db, "create", "three_sess", "s1", nowStr(0))
		insertEvent(t, db, "reinforce", "three_sess", "s2", nowStr(0))
		insertEvent(t, db, "reinforce", "three_sess", "s3", nowStr(0))
		p, err := SelectProfile(db, cfg, 100000)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.In) != 2 || p.In[0].ID != "three_sess" {
			t.Fatalf("higher session count must rank first: %v", idsOf(p.In))
		}
	})

	t.Run("higher confidence ranks first", func(t *testing.T) {
		db, cfg := testDB(t)
		insertPrefFull(t, db, "lowc", "Cat", "text a", "DISTILLED", 0.4, "[]")
		insertPrefFull(t, db, "highc", "Cat", "text b", "DISTILLED", 0.8, "[]")
		twoSessions(t, db, "lowc")
		twoSessions(t, db, "highc")
		p, err := SelectProfile(db, cfg, 100000)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.In) != 2 || p.In[0].ID != "highc" {
			t.Fatalf("higher confidence must rank first: %v", idsOf(p.In))
		}
	})

	t.Run("more recently reinforced ranks first", func(t *testing.T) {
		db, cfg := testDB(t)
		insertPrefFull(t, db, "older", "Cat", "text a", "DISTILLED", 0.5, "[]")
		insertPrefFull(t, db, "newer", "Cat", "text b", "DISTILLED", 0.5, "[]")
		twoSessions(t, db, "older")
		twoSessions(t, db, "newer")
		setLastReinforced(t, db, "older", nowStr(-10))
		setLastReinforced(t, db, "newer", nowStr(0))
		p, err := SelectProfile(db, cfg, 100000)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.In) != 2 || p.In[0].ID != "newer" {
			t.Fatalf("more recently reinforced must rank first: %v", idsOf(p.In))
		}
	})

	t.Run("current member wins a tie over a non-member", func(t *testing.T) {
		db, cfg := testDB(t)
		insertPrefFull(t, db, "member1", "Cat", "text a", "DISTILLED", 0.5, "[]")
		insertPrefFull(t, db, "nonmember1", "Cat", "text b", "DISTILLED", 0.5, "[]")
		twoSessions(t, db, "member1")
		twoSessions(t, db, "nonmember1")
		insertMembershipEvent(t, db, "profile_add", "member1", 0, false)
		p, err := SelectProfile(db, cfg, 100000)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.In) != 2 || p.In[0].ID != "member1" {
			t.Fatalf("current member must win a tie over a non-member: %v", idsOf(p.In))
		}
	})

	t.Run("shorter statement ranks first", func(t *testing.T) {
		db, cfg := testDB(t)
		insertPrefFull(t, db, "long1", "Cat", "this is a much longer statement text than the other", "DISTILLED", 0.5, "[]")
		insertPrefFull(t, db, "short1", "Cat", "short one", "DISTILLED", 0.5, "[]")
		twoSessions(t, db, "long1")
		twoSessions(t, db, "short1")
		p, err := SelectProfile(db, cfg, 100000)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.In) != 2 || p.In[0].ID != "short1" {
			t.Fatalf("shorter statement must rank first: %v", idsOf(p.In))
		}
	})

	t.Run("id ascending is the final tie-break", func(t *testing.T) {
		db, cfg := testDB(t)
		insertPrefFull(t, db, "b_id", "Cat", "same text len", "DISTILLED", 0.5, "[]")
		insertPrefFull(t, db, "a_id", "Cat", "same text len", "DISTILLED", 0.5, "[]")
		twoSessions(t, db, "b_id")
		twoSessions(t, db, "a_id")
		p, err := SelectProfile(db, cfg, 100000)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.In) != 2 || p.In[0].ID != "a_id" {
			t.Fatalf("id ascending must be the final tie-break: %v", idsOf(p.In))
		}
	})

	t.Run("budget cut drops the lowest-ranked row", func(t *testing.T) {
		db, cfg := testDB(t)
		insertPrefFull(t, db, "keep1", "Cat", "kept statement", "STATED", 0.9, "[]")
		insertPrefFull(t, db, "drop1", "Cat", "dropped statement", "DISTILLED", 0.3, "[]")
		insertEvent(t, db, "create", "keep1", "s1", nowStr(0))
		twoSessions(t, db, "drop1")
		tightBudget := EstimateTokens(BulletLine(Preference{Statement: "kept statement", Confidence: 0.9})) + 1 + EstimateTokens("## Cat") + 2
		p, err := SelectProfile(db, cfg, tightBudget)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.In) != 1 || p.In[0].ID != "keep1" {
			t.Fatalf("budget must keep only the top-ranked row: %v", idsOf(p.In))
		}
		if reason := p.Excluded["drop1"]; !strings.HasPrefix(reason, "over budget (rank") {
			t.Fatalf("dropped row must carry an over-budget reason, got %q", reason)
		}
		if p.Tokens > tightBudget {
			t.Fatalf("Tokens must never exceed budget: %d > %d", p.Tokens, tightBudget)
		}
	})

	t.Run("heading cost is charged once per category", func(t *testing.T) {
		db, cfg := testDB(t)
		insertPrefFull(t, db, "c1a", "CatX", "first", "STATED", 0.9, "[]")
		insertPrefFull(t, db, "c1b", "CatX", "second", "STATED", 0.8, "[]")
		insertEvent(t, db, "create", "c1a", "s1", nowStr(0))
		insertEvent(t, db, "create", "c1b", "s1", nowStr(0))
		p, err := SelectProfile(db, cfg, 100000)
		if err != nil {
			t.Fatal(err)
		}
		want := EstimateTokens("## CatX") + 2 +
			EstimateTokens(BulletLine(Preference{Statement: "first", Confidence: 0.9})) + 1 +
			EstimateTokens(BulletLine(Preference{Statement: "second", Confidence: 0.8})) + 1
		if p.Tokens != want {
			t.Fatalf("heading cost must be charged exactly once per category: got %d want %d", p.Tokens, want)
		}
	})

	t.Run("skip-and-continue lets a shorter row fill budget a long row left unused", func(t *testing.T) {
		db, cfg := testDB(t)
		insertPrefFull(t, db, "big1", "Cat", strings.Repeat("this is a very long statement that costs a lot of budget ", 10), "STATED", 0.9, "[]")
		insertPrefFull(t, db, "small1", "Cat", "small one", "STATED", 0.5, "[]")
		insertEvent(t, db, "create", "big1", "s1", nowStr(0))
		insertEvent(t, db, "create", "small1", "s1", nowStr(0))
		smallBudget := EstimateTokens(BulletLine(Preference{Statement: "small one", Confidence: 0.5})) + 1 + EstimateTokens("## Cat") + 2
		p, err := SelectProfile(db, cfg, smallBudget)
		if err != nil {
			t.Fatal(err)
		}
		in := map[string]bool{}
		for _, pr := range p.In {
			in[pr.ID] = true
		}
		if in["big1"] {
			t.Fatal("big1 must not fit the tight budget")
		}
		if !in["small1"] {
			t.Fatal("skip-and-continue must let small1 fill the budget big1 left unused")
		}
		if reason := p.Excluded["big1"]; !strings.HasPrefix(reason, "over budget (rank") {
			t.Fatalf("big1 must carry an over-budget reason, got %q", reason)
		}
	})
}

// TestSelectProfileHysteresis covers phase-01's hysteresis table.
func TestSelectProfileHysteresis(t *testing.T) {
	t.Run("a seed:true cut is never stale-cut", func(t *testing.T) {
		db, cfg := testDB(t)
		insertPrefFull(t, db, "seedcut1", "Cat", "seed cut text", "DISTILLED", 0.5, "[]")
		insertEvent(t, db, "create", "seedcut1", "s1", nowStr(0))
		insertEvent(t, db, "reinforce", "seedcut1", "s2", nowStr(0))
		insertMembershipEvent(t, db, "profile_cut", "seedcut1", 2, true)

		insertPrefFull(t, db, "other1", "Cat", "other text", "DISTILLED", 0.5, "[]")
		insertEvent(t, db, "create", "other1", "s1", nowStr(0))
		insertEvent(t, db, "reinforce", "other1", "s2", nowStr(0))

		p, err := SelectProfile(db, cfg, 100000)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.In) != 2 {
			t.Fatalf("a seed cut must compete on evidence like any other row: %v excluded=%v", idsOf(p.In), p.Excluded)
		}
	})

	t.Run("a stale-cut row never evicts a current member", func(t *testing.T) {
		db, cfg := testDB(t)
		insertPrefFull(t, db, "member2", "Cat", "member text", "DISTILLED", 0.4, "[]")
		insertEvent(t, db, "create", "member2", "s1", nowStr(0))
		insertEvent(t, db, "reinforce", "member2", "s2", nowStr(0))
		insertMembershipEvent(t, db, "profile_add", "member2", 0, false)

		insertPrefFull(t, db, "stalecut2", "Cat", "stale cut text", "DISTILLED", 0.9, "[]")
		insertEvent(t, db, "create", "stalecut2", "s1", nowStr(0))
		insertEvent(t, db, "reinforce", "stalecut2", "s2", nowStr(0))
		insertMembershipEvent(t, db, "profile_cut", "stalecut2", 2, false)

		budget := EstimateTokens(BulletLine(Preference{Statement: "member text", Confidence: 0.4})) + 1 + EstimateTokens("## Cat") + 2
		p, err := SelectProfile(db, cfg, budget)
		if err != nil {
			t.Fatal(err)
		}
		in := map[string]bool{}
		for _, pr := range p.In {
			in[pr.ID] = true
		}
		if !in["member2"] {
			t.Fatal("stale-cut rows must rank below every non-stale row of the same tier, so a member is never evicted")
		}
		if in["stalecut2"] {
			t.Fatal("stalecut2 must be excluded under a budget sized for member2 alone")
		}
	})

	t.Run("a cut row that gained a session competes normally", func(t *testing.T) {
		db, cfg := testDB(t)
		insertPrefFull(t, db, "regained1", "Cat", "regained text", "DISTILLED", 0.5, "[]")
		insertEvent(t, db, "create", "regained1", "s1", nowStr(0))
		insertMembershipEvent(t, db, "profile_cut", "regained1", 1, false)
		insertEvent(t, db, "reinforce", "regained1", "s2", nowStr(1))

		p, err := SelectProfile(db, cfg, 100000)
		if err != nil {
			t.Fatal(err)
		}
		in := map[string]bool{}
		for _, pr := range p.In {
			in[pr.ID] = true
		}
		if !in["regained1"] {
			t.Fatal("a cut row that gained a new session since the cut must be eligible again")
		}
	})

	t.Run("leftover budget is filled by stale-cut rows", func(t *testing.T) {
		db, cfg := testDB(t)
		insertPrefFull(t, db, "member3", "Cat", "member text", "STATED", 0.9, "[]")
		insertEvent(t, db, "create", "member3", "s1", nowStr(0))
		insertMembershipEvent(t, db, "profile_add", "member3", 0, false)

		insertPrefFull(t, db, "stalefill1", "Cat", "stale fill", "DISTILLED", 0.5, "[]")
		insertEvent(t, db, "create", "stalefill1", "s1", nowStr(0))
		insertEvent(t, db, "reinforce", "stalefill1", "s2", nowStr(0))
		insertMembershipEvent(t, db, "profile_cut", "stalefill1", 2, false)

		p, err := SelectProfile(db, cfg, 100000)
		if err != nil {
			t.Fatal(err)
		}
		in := map[string]bool{}
		for _, pr := range p.In {
			in[pr.ID] = true
		}
		if !in["member3"] || !in["stalefill1"] {
			t.Fatalf("leftover budget must still admit a stale-cut row: %v excluded=%v", idsOf(p.In), p.Excluded)
		}
	})

	t.Run("equal rank: the member stays", func(t *testing.T) {
		db, cfg := testDB(t)
		insertPrefFull(t, db, "tiemember", "Cat", "same text", "DISTILLED", 0.5, "[]")
		insertEvent(t, db, "create", "tiemember", "s1", nowStr(0))
		insertEvent(t, db, "reinforce", "tiemember", "s2", nowStr(0))
		insertMembershipEvent(t, db, "profile_add", "tiemember", 0, false)

		insertPrefFull(t, db, "tienonmember", "Cat", "same text", "DISTILLED", 0.5, "[]")
		insertEvent(t, db, "create", "tienonmember", "s1", nowStr(0))
		insertEvent(t, db, "reinforce", "tienonmember", "s2", nowStr(0))

		budget := EstimateTokens(BulletLine(Preference{Statement: "same text", Confidence: 0.5})) + 1 + EstimateTokens("## Cat") + 2
		p, err := SelectProfile(db, cfg, budget)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.In) != 1 || p.In[0].ID != "tiemember" {
			t.Fatalf("on an exact tie, the current member must win the budget slot: %v", idsOf(p.In))
		}
	})
}
