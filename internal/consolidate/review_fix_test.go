package consolidate

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/learn"
	"github.com/thinhkhuat/palate/internal/storage"
)

func tierOf(db *sql.DB, id string) string {
	var tier string
	_ = db.QueryRow(`SELECT trust_tier FROM preferences WHERE id=?`, id).Scan(&tier)
	return tier
}

func retireEvents(db *sql.DB, id string) int {
	var n int
	_ = db.QueryRow(`SELECT count(*) FROM events WHERE event_type='consolidate_retire' AND json_extract(payload,'$.id')=?`, id).Scan(&n)
	return n
}

// An old one-session row X (higher confidence) merges with a newer row Y in
// this pass: X survives with two sessions and a fresh last_reinforced_at. The
// stale step used to retire it from its pass-start snapshot, losing both rows.
func TestStaleForgetSkipsMergeSurvivor(t *testing.T) {
	db, cfg := testStore(t)
	statedRows(t, db, 9)
	old := time.Now().UTC().Add(-40 * 24 * time.Hour)
	insertRow(t, db, rowSpec{id: "X", stmt: "Writes tests before the code always", conf: 0.8, created: old, sessions: []string{"sess-a"}})
	insertRow(t, db, rowSpec{id: "Y", stmt: "Test first then code", conf: 0.5, sessions: []string{"sess-b"}})
	gw, _ := newStub(t, func(int, string) stubReply {
		return stubReply{actions: []Action{{Type: "merge", IDs: []string{"X", "Y"}, Statement: "Writes tests first.", Reason: "same"}}}
	})
	rep, err := Run(context.Background(), db, cfg, gw, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Applied != 1 || rep.Retired != 0 {
		t.Fatalf("report %+v, want Applied 1 and Retired 0", rep)
	}
	counts, _ := learn.SessionCounts(db)
	if !isLive(db, "X") || isLive(db, "Y") || counts["X"] != 2 {
		t.Fatalf("live(X)=%v live(Y)=%v sessions(X)=%d, want true, false, 2", isLive(db, "X"), isLive(db, "Y"), counts["X"])
	}
}

// A row that becomes STATED, or gains a second session, while the pass runs
// (another process: `palate restore`, a hook, MCP) must not be retired from
// the stale snapshot taken at the start of the pass.
func TestStaleForgetSkipsConcurrentStated(t *testing.T) {
	db, cfg := testStore(t)
	statedRows(t, db, 9)
	old := time.Now().UTC().Add(-40 * 24 * time.Hour)
	insertRow(t, db, rowSpec{id: "promoted", stmt: "a rare but true preference", created: old})
	insertRow(t, db, rowSpec{id: "repeated", stmt: "another rare preference", created: old})
	insertRow(t, db, rowSpec{id: "stale", stmt: "a genuinely stale remark", created: old})
	candidateRows(t, db, 1)
	gw, _ := newStub(t, func(call int, _ string) stubReply {
		if call == 0 {
			if _, err := learn.Restore(db, "promoted"); err != nil { // the owner: palate restore promoted
				t.Error(err)
			}
			now := time.Now().UTC().Format(time.RFC3339)
			if err := storage.AppendEvent(db, "reinforce", `{"id":"repeated","c":0.5,"signal":"llm_distilled","session":"sess-later"}`); err != nil {
				t.Error(err)
			}
			if _, err := db.Exec(`UPDATE preferences SET last_reinforced_at=? WHERE id='repeated'`, now); err != nil {
				t.Error(err)
			}
		}
		return stubReply{}
	})
	rep, err := Run(context.Background(), db, cfg, gw, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Retired != 1 {
		t.Fatalf("report %+v, want exactly the genuinely stale row retired", rep)
	}
	if !isLive(db, "promoted") || tierOf(db, "promoted") != "STATED" {
		t.Fatalf("a row promoted to STATED mid-pass was retired: live=%v tier=%s", isLive(db, "promoted"), tierOf(db, "promoted"))
	}
	if !isLive(db, "repeated") {
		t.Fatal("a row that gained a second session mid-pass was retired")
	}
	if isLive(db, "stale") {
		t.Fatal("the control row (stale, untouched) must still be retired")
	}
}

// restore of a stale-forgotten row promotes it to STATED, so a second pass
// more than 24 hours later does not forget it again.
func TestRestoredStaleRowNotReforgotten(t *testing.T) {
	db, cfg := testStore(t)
	statedRows(t, db, 9)
	old := time.Now().UTC().Add(-40 * 24 * time.Hour)
	insertRow(t, db, rowSpec{id: "R", stmt: "a rare but true preference", created: old})
	gw, _ := newStub(t, func(int, string) stubReply { return stubReply{} })

	rep1, err := Run(context.Background(), db, cfg, gw, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep1.Retired != 1 || isLive(db, "R") {
		t.Fatalf("setup: pass 1 report %+v live(R)=%v, want R stale-retired", rep1, isLive(db, "R"))
	}
	if found, err := learn.Restore(db, "R"); err != nil || !found {
		t.Fatalf("restore: found=%v err=%v", found, err)
	}
	if !isLive(db, "R") || tierOf(db, "R") != "STATED" {
		t.Fatalf("restoring a stale-forgotten row must reopen it as STATED: live=%v tier=%s", isLive(db, "R"), tierOf(db, "R"))
	}
	// A day and more passes.
	past := time.Now().UTC().Add(-25 * time.Hour).Format(time.RFC3339)
	if _, err := db.Exec(`UPDATE preferences SET last_reinforced_at=? WHERE id='R'`, past); err != nil {
		t.Fatal(err)
	}
	rep2, err := Run(context.Background(), db, cfg, gw, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep2.Retired != 0 || !isLive(db, "R") || retireEvents(db, "R") != 1 {
		t.Fatalf("pass 2 report %+v live(R)=%v retire events=%d, want Retired 0, live, 1 retire event in total", rep2, isLive(db, "R"), retireEvents(db, "R"))
	}
	// A plain restore of a row an ordinary retire closed keeps its tier.
	insertRow(t, db, rowSpec{id: "plain", stmt: "retired by hand", created: old})
	if _, err := db.Exec(`UPDATE preferences SET valid_until=? WHERE id='plain'`, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if found, err := learn.Restore(db, "plain"); err != nil || !found || tierOf(db, "plain") != "DISTILLED" {
		t.Fatalf("a row not retired by the stale step keeps its tier: found=%v err=%v tier=%s", found, err, tierOf(db, "plain"))
	}
}

// retire, project and a confidence-lowering rescore on a STATED row never
// apply in a pass; they are filed as consolidate proposals. Raising a STATED
// row's confidence, and every action on a non-STATED row, still applies.
func TestStatedNonTextActionsBecomeProposals(t *testing.T) {
	db, cfg := testStore(t)
	statedRows(t, db, 20) // p00..p19, STATED at 0.95
	insertRow(t, db, rowSpec{id: "up", stmt: "a stated line that can rise", tier: "STATED", conf: 0.80})
	candidateRows(t, db, 1) // c000, DISTILLED
	gw, _ := newStub(t, func(int, string) stubReply {
		return stubReply{actions: []Action{
			{Type: "retire", ID: "p00", Reason: "one-off"},
			{Type: "project", ID: "p01", Reason: "repo specific"},
			{Type: "rescore", ID: "p02", Confidence: 0.4, Reason: "weak"},
			{Type: "rescore", ID: "up", Confidence: 0.9, Reason: "reinforced"},
			{Type: "retire", ID: "c000", Reason: "one-off"},
		}}
	})
	rep, err := Run(context.Background(), db, cfg, gw, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Actions != 5 || rep.Applied != 2 || rep.Proposed != 3 || rep.Skipped != 0 || rep.Mode != "auto" || rep.Outcome != "applied" {
		t.Fatalf("report %+v, want 5 actions: 2 applied (raise, retire of the candidate), 3 proposed, mode auto", rep)
	}
	if !isLive(db, "p00") || confOf(db, "p02") != 0.95 {
		t.Fatalf("STATED rows must be untouched: live(p00)=%v conf(p02)=%v", isLive(db, "p00"), confOf(db, "p02"))
	}
	var prov string
	_ = db.QueryRow(`SELECT provenance FROM preferences WHERE id='p01'`).Scan(&prov)
	if prov != `["explicit_prefer"]` {
		t.Fatalf("p01 provenance = %s, want it untagged", prov)
	}
	if confOf(db, "up") != 0.9 || isLive(db, "c000") {
		t.Fatalf("raise must apply and the candidate retire must apply: conf(up)=%v live(c000)=%v", confOf(db, "up"), isLive(db, "c000"))
	}
	props, err := learn.ListProposals(db)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, p := range props {
		if p.Source != "consolidate" || p.Status != "pending" {
			t.Fatalf("proposal %+v, want a pending consolidate proposal", p)
		}
		got[p.Content] = true
	}
	for _, want := range []string{
		`{"type":"retire","id":"p00","reason":"one-off"}`,
		`{"type":"project","id":"p01","reason":"repo specific"}`,
		`{"type":"rescore","id":"p02","confidence":0.4,"reason":"weak"}`,
	} {
		if !got[want] {
			t.Fatalf("missing proposal %s in %v", want, got)
		}
	}
	// The owner's approval (palate promote) still applies the stored action.
	if err := ApplyProposal(db, cfg, `{"type":"retire","id":"p00","reason":"one-off"}`); err != nil {
		t.Fatal(err)
	}
	if isLive(db, "p00") {
		t.Fatal("promoting the proposal must retire p00")
	}
}

// Round 2.

// A row the owner makes STATED while the pass runs must not be retired by a
// model action decided from the pass-start snapshot.
func TestStatedRetireRecheckedAtApply(t *testing.T) {
	db, cfg := testStore(t)
	statedRows(t, db, 9)
	insertRow(t, db, rowSpec{id: "R", stmt: "a rare but true preference"})
	gw, _ := newStub(t, func(call int, _ string) stubReply {
		if call == 0 {
			if _, err := learn.Restore(db, "R"); err != nil { // palate restore R, from another process
				t.Error(err)
			}
		}
		return stubReply{actions: []Action{{Type: "retire", ID: "R", Reason: "one-off"}}}
	})
	rep, err := Run(context.Background(), db, cfg, gw, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Applied != 0 || rep.Proposed != 1 {
		t.Fatalf("report %+v, want 0 applied and 1 proposed", rep)
	}
	if !isLive(db, "R") || tierOf(db, "R") != "STATED" {
		t.Fatalf("a row STATED at write time was retired: live=%v tier=%s", isLive(db, "R"), tierOf(db, "R"))
	}
	props, _ := learn.ListProposals(db)
	if len(props) != 1 || props[0].Content != `{"type":"retire","id":"R","reason":"one-off"}` {
		t.Fatalf("proposals = %+v, want the one retire", props)
	}
}

// A merge that names a STATED row may not lower its confidence or re-file it.
func TestMergeCannotLowerStatedConfidence(t *testing.T) {
	setup := func(t *testing.T) (*sql.DB, config.Config) {
		db, cfg := testStore(t)
		statedRows(t, db, 9)
		insertRow(t, db, rowSpec{id: "S", stmt: "Always writes tests before the code", tier: "STATED", conf: 0.95})
		insertRow(t, db, rowSpec{id: "D", stmt: "Writes tests first", conf: 0.5, sessions: []string{"x1"}})
		return db, cfg
	}
	run := func(t *testing.T, db *sql.DB, cfg config.Config, a Action) Report {
		gw, _ := newStub(t, func(int, string) stubReply { return stubReply{actions: []Action{a}} })
		rep, err := Run(context.Background(), db, cfg, gw, RunOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return rep
	}
	catOf := func(db *sql.DB, id string) string {
		var c string
		_ = db.QueryRow(`SELECT category FROM preferences WHERE id=?`, id).Scan(&c)
		return c
	}

	t.Run("a lower confidence and a new category become a proposal", func(t *testing.T) {
		db, cfg := setup(t)
		rep := run(t, db, cfg, Action{Type: "merge", IDs: []string{"S", "D"}, Statement: "whatever", Confidence: 0.2, Category: "Misc", Reason: "same"})
		if rep.Applied != 0 || rep.Proposed != 1 {
			t.Fatalf("report %+v, want 0 applied and 1 proposed", rep)
		}
		if confOf(db, "S") != 0.95 || catOf(db, "S") != "Testing" || !isLive(db, "D") {
			t.Fatalf("nothing may change: conf(S)=%v cat(S)=%s live(D)=%v", confOf(db, "S"), catOf(db, "S"), isLive(db, "D"))
		}
	})
	t.Run("a category change alone becomes a proposal", func(t *testing.T) {
		db, cfg := setup(t)
		rep := run(t, db, cfg, Action{Type: "merge", IDs: []string{"S", "D"}, Statement: "whatever", Category: "Misc", Reason: "same"})
		if rep.Applied != 0 || rep.Proposed != 1 || catOf(db, "S") != "Testing" {
			t.Fatalf("report %+v cat(S)=%s, want a proposal and the category kept", rep, catOf(db, "S"))
		}
	})
	t.Run("a merge that leaves the STATED row as it is still applies", func(t *testing.T) {
		db, cfg := setup(t)
		rep := run(t, db, cfg, Action{Type: "merge", IDs: []string{"S", "D"}, Statement: "whatever", Reason: "same"})
		if rep.Applied != 1 || rep.Proposed != 0 || isLive(db, "D") || confOf(db, "S") != 0.95 || catOf(db, "S") != "Testing" {
			t.Fatalf("report %+v live(D)=%v conf(S)=%v cat(S)=%s, want applied, D merged away, S unchanged", rep, isLive(db, "D"), confOf(db, "S"), catOf(db, "S"))
		}
	})
}

// A later batch of a multi-batch pass may name rows an earlier batch already
// closed; those actions are skipped without writing events.
func TestMultiBatchNoEventsForClosedRows(t *testing.T) {
	db, cfg := testStore(t)
	candidateRows(t, db, 45) // batches c000-c039 and c040-c044
	gw, _ := newStub(t, func(call int, _ string) stubReply {
		if call == 0 {
			return stubReply{actions: []Action{{Type: "retire", ID: "c000", Reason: "one-off"}}}
		}
		return stubReply{actions: []Action{
			{Type: "retire", ID: "c000", Reason: "one-off again"},
			{Type: "rescore", ID: "c000", Confidence: 0.7, Reason: "reinforced"},
			{Type: "retire", ID: "c041", Reason: "one-off"},
		}}
	})
	rep, err := Run(context.Background(), db, cfg, gw, RunOptions{Rebuild: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Batches != 2 || rep.Applied != 2 || rep.Skipped != 2 {
		t.Fatalf("report %+v, want 2 batches, 2 applied (c000, c041), 2 skipped (the closed row)", rep)
	}
	count := func(typ string) int {
		var n int
		_ = db.QueryRow(`SELECT count(*) FROM events WHERE event_type=? AND json_extract(payload,'$.id')='c000'`, typ).Scan(&n)
		return n
	}
	if count("consolidate_retire") != 1 || count("consolidate_rescore") != 0 {
		t.Fatalf("events for c000: retire=%d rescore=%d, want 1 and 0", count("consolidate_retire"), count("consolidate_rescore"))
	}
}
