package consolidate

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/thinhkhuat/palate/internal/learn"
	"github.com/thinhkhuat/palate/internal/render"
)

// A merge overwrites the survivor's text. A stale editor buffer that still
// holds the survivor's old wording must have it listed (and need --yes) just
// like the partner the merge closed, not file it as a new STATED twin.
func TestStaleBufferListsMergeSurvivorOldText(t *testing.T) {
	db, cfg := testStore(t)
	if err := cfg.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	insertRow(t, db, rowSpec{id: "A", stmt: "Prefers tabs for indentation.", conf: 0.8, sessions: []string{"s1", "s2"}})
	insertRow(t, db, rowSpec{id: "B", stmt: "Uses tabs to indent.", conf: 0.7, sessions: []string{"s3", "s4"}})
	if _, err := render.Sync(db, cfg, cfg.TastePath()); err != nil {
		t.Fatal(err)
	}
	buffer, err := os.ReadFile(cfg.TastePath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(buffer), "Prefers tabs for indentation") || !strings.Contains(string(buffer), "Uses tabs to indent") {
		t.Fatalf("setup: both rows must be in TASTE.md:\n%s", buffer)
	}
	gw, _ := newStub(t, func(int, string) stubReply {
		return stubReply{actions: []Action{{Type: "merge", IDs: []string{"A", "B"}, Statement: "Indents with tabs.", Reason: "same"}}}
	})
	rep, err := Run(context.Background(), db, cfg, gw, RunOptions{Rebuild: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Applied != 1 || isLive(db, "B") {
		t.Fatalf("setup: report %+v live(B)=%v, want the merge applied", rep, isLive(db, "B"))
	}
	if _, err := render.Sync(db, cfg, cfg.TastePath()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.TastePath(), append(buffer, []byte("- Prefers delta. Confidence: 0.90\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := render.ReadBack(db, cfg, cfg.TastePath()); err != nil {
		t.Fatal(err)
	}
	if !render.HoldPending(cfg) || !render.HoldAcceptable(cfg) {
		t.Fatalf("setup: pending=%v acceptable=%v, want true and true", render.HoldPending(cfg), render.HoldAcceptable(cfg))
	}
	err = render.AcceptHeldEdits(db, cfg, false)
	if err == nil {
		t.Fatal("a buffer holding a merged row's old wording needs --yes")
	}
	for _, want := range []string{"Uses tabs to indent", "Prefers tabs for indentation", "--yes"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal must list the closed partner and the survivor's old wording, missing %q in %q", want, err.Error())
		}
	}
}

// A promote that lands while a merge is being written (here a trigger fired by
// the survivor's UPDATE stands in for it) must turn the whole merge into a
// proposal: nothing of the merge may stay written.
func TestMergeStatedCheckIsAtomic(t *testing.T) {
	db, cfg := testStore(t)
	statedRows(t, db, 9)
	insertRow(t, db, rowSpec{id: "K", stmt: "Writes tests before the code always", conf: 0.8, sessions: []string{"sess-a"}})
	insertRow(t, db, rowSpec{id: "D", stmt: "Test first then code", conf: 0.5, sessions: []string{"sess-b"}})
	if _, err := db.Exec(`CREATE TRIGGER promote_d BEFORE UPDATE OF statement ON preferences WHEN OLD.id='K'
		BEGIN UPDATE preferences SET trust_tier='STATED' WHERE id='D'; END`); err != nil {
		t.Fatal(err)
	}
	gw, _ := newStub(t, func(int, string) stubReply {
		return stubReply{actions: []Action{{Type: "merge", IDs: []string{"K", "D"}, Statement: "Writes tests first.", Reason: "same"}}}
	})
	rep, err := Run(context.Background(), db, cfg, gw, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Applied != 0 || rep.Proposed != 1 {
		t.Fatalf("report %+v, want 0 applied and 1 proposed", rep)
	}
	var stmt string
	_ = db.QueryRow(`SELECT statement FROM preferences WHERE id='K'`).Scan(&stmt)
	if !isLive(db, "D") || stmt != "Writes tests before the code always" {
		t.Fatalf("a half-written merge: live(D)=%v statement(K)=%q", isLive(db, "D"), stmt)
	}
	if n := rowCount(db, "events WHERE event_type='consolidate_merge'"); n != 0 {
		t.Fatalf("consolidate_merge events = %d, want 0", n)
	}
	props, _ := learn.ListProposals(db)
	if len(props) != 1 {
		t.Fatalf("proposals = %+v, want the one merge", props)
	}
}

// Every action the model returns lands in exactly one bucket: applied,
// proposed, or skipped with a reason.
func TestConsolidateSummaryCountsAddUp(t *testing.T) {
	db, cfg := testStore(t)
	statedRows(t, db, 9)
	candidateRows(t, db, 6) // c000-c005
	insertRow(t, db, rowSpec{id: "sh", stmt: "a twice-seen fresh candidate", sessions: []string{"x1", "x2"}})
	// An identical retire proposal for p01 is already pending.
	if _, err := learn.CreateProposalFrom(db, `{"type":"retire","id":"p01","reason":"one-off"}`, "", proposalSource); err != nil {
		t.Fatal(err)
	}
	gw, _ := newStub(t, func(int, string) stubReply {
		return stubReply{actions: []Action{
			{Type: "retire", ID: "c000", Reason: "one-off"}, // applied
			{Type: "retire", ID: "c001", Reason: "one-off"}, // applied
			{Type: "retire", ID: "nope", Reason: "one-off"}, // skipped: unknown id
			{Type: "retire", ID: "p00", Reason: "one-off"},  // proposed: STATED row
			{Type: "rewrite", ID: "c002", Statement: "candidate statement number 002 with a great many extra words added to it", Reason: "x"}, // skipped: refused
			{Type: "retire", ID: "c000", Reason: "one-off again"},                                                                             // skipped: already closed
			{Type: "retire", ID: "p01", Reason: "one-off"},                                                                                    // skipped: duplicate proposal
			{Type: "retire", ID: "sh", Reason: "one-off"},                                                                                     // skipped: shielded
		}}
	})
	rep, err := Run(context.Background(), db, cfg, gw, RunOptions{Rebuild: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Actions != 8 || rep.Applied != 2 || rep.Proposed != 1 || rep.Skipped != 5 {
		t.Fatalf("report %+v, want 8 actions = 2 applied + 1 proposed + 5 skipped", rep)
	}
	if rep.Applied+rep.Proposed+rep.Skipped != rep.Actions {
		t.Fatalf("buckets %d+%d+%d do not add up to %d actions", rep.Applied, rep.Proposed, rep.Skipped, rep.Actions)
	}
	want := map[string]int{"unknown-id": 1, "refused": 1, "closed": 1, "duplicate-proposal": 1, "shielded": 1}
	sum := 0
	for k, v := range rep.SkipReasons {
		sum += v
		if want[k] != v {
			t.Errorf("skip reason %q = %d, want %d (all: %v)", k, v, want[k], rep.SkipReasons)
		}
	}
	if sum != rep.Skipped || len(rep.SkipReasons) != len(want) {
		t.Fatalf("skip reasons %v must account for all %d skipped actions", rep.SkipReasons, rep.Skipped)
	}
	line := rep.Summary()
	if !strings.Contains(line, "8 actions (2 applied, 1 proposed, 5 skipped") {
		t.Fatalf("summary = %q", line)
	}
	for k, v := range want {
		if !strings.Contains(line, fmt.Sprintf("%d %s", v, k)) {
			t.Fatalf("summary %q must name the reason %q", line, k)
		}
	}
}
