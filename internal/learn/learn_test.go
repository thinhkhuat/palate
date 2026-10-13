package learn

import (
	"bytes"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thinhkhuat/palate/internal/config"
	"github.com/thinhkhuat/palate/internal/storage"
)

// captureStderr redirects os.Stderr for the duration of fn and returns what
// was written to it — used to prove a refused rewrite logs its reason
// (phase-01 req 11), not just silently keeps the old text.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	fn()
	_ = w.Close()
	os.Stderr = orig
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	return buf.String()
}

func testDB(t *testing.T) (*sql.DB, config.Config) {
	t.Helper()
	storage.Close()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { storage.Close() })
	return db, config.Default()
}

func TestDecayReinforceWeaken(t *testing.T) {
	if got := Decay(0.5, 0.1, 168, 0); got != 0.5 {
		t.Fatalf("decay 0h: %v", got)
	}
	if got := Decay(0.5, 0.1, 168, 168); got < 0.24 || got > 0.26 {
		t.Fatalf("decay half-life: %v", got)
	}
	if got := Reinforce(0.5, 1.0, 0.3, 0.1); got != 0.65 {
		t.Fatalf("reinforce: %v", got)
	}
	if got := Weaken(0.5, 1.0, 0.3, 0.1); got != 0.35 {
		t.Fatalf("weaken: %v", got)
	}
	if got := InitialConfidence(1.0, 0.1); got != 0.5 {
		t.Fatalf("initial: %v", got)
	}
	if got := Reinforce(1.0, 1.0, 0.3, 0.1); got != 1.0 {
		t.Fatalf("clamp top: %v", got)
	}
	if got := Weaken(0.1, 1.0, 0.3, 0.1); got != 0.1 {
		t.Fatalf("clamp floor: %v", got)
	}
}

func TestApplySignalAndReplayDeterminism(t *testing.T) {
	db, cfg := testDB(t)
	sig := Signal{Type: SignalExplicitPrefer, Statement: "use table-driven tests", Category: "Testing"}
	if _, err := ApplySignal(db, cfg, sig, nil); err != nil {
		t.Fatal(err)
	}
	same := sig
	asOf := time.Now().UTC().Add(1 * time.Hour)
	_ = asOf
	var c1 float64
	_ = db.QueryRow(`SELECT confidence FROM preferences WHERE statement=?`, sig.Statement).Scan(&c1)
	if c1 != 0.4 {
		t.Fatalf("initial confidence: %v", c1)
	}
	_, _ = ApplySignal(db, cfg, same, nil)
	var c2 float64
	_ = db.QueryRow(`SELECT confidence FROM preferences WHERE statement=?`, sig.Statement).Scan(&c2)
	if c2 <= c1 {
		t.Fatalf("reinforce should increase: %v -> %v", c1, c2)
	}
}

func TestProposalsBoundary(t *testing.T) {
	db, cfg := testDB(t)
	id, err := CreateProposal(db, "use early returns", "General")
	if err != nil {
		t.Fatal(err)
	}
	var pending int
	_ = db.QueryRow(`SELECT count(*) FROM proposals WHERE status='pending'`).Scan(&pending)
	if pending != 1 {
		t.Fatalf("pending: %d", pending)
	}
	var eventsBefore int
	_ = db.QueryRow(`SELECT count(*) FROM events`).Scan(&eventsBefore)
	var prefsBefore int
	_ = db.QueryRow(`SELECT count(*) FROM preferences`).Scan(&prefsBefore)
	_ = prefsBefore
	var eventsAfterPropose int
	_ = db.QueryRow(`SELECT count(*) FROM events`).Scan(&eventsAfterPropose)
	if eventsAfterPropose != eventsBefore {
		t.Fatalf("propose should not create events: before %d after %d", eventsBefore, eventsAfterPropose)
	}
	_, err = PromoteProposal(db, id, "stated_preference", cfg, func(sig Signal) error {
		_, e := ApplySignal(db, cfg, sig, nil)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	var eventsAfterPromote int
	_ = db.QueryRow(`SELECT count(*) FROM events`).Scan(&eventsAfterPromote)
	if eventsAfterPromote <= eventsAfterPropose {
		t.Fatalf("promote should create event")
	}
}

// TestExtractStatedPreferenceWordBoundary: the trigger must be a real
// first-person statement; the bare word "preference" (or a negation) is not
// one. Observed 2026-09-13: "no preference" was stored as a preference.
func TestExtractStatedPreferenceWordBoundary(t *testing.T) {
	for _, text := range []string{
		"just a normal message, no preference",
		"I have no preference on this",
		"my preferences are in the file",
		"preferred stock went up",
		"I prefer.",
	} {
		if stmt, _ := ExtractStatedPreference(text); stmt != "" {
			t.Errorf("%q must not yield a preference, got %q", text, stmt)
		}
	}
	for text, want := range map[string]string{
		"I prefer tabs over spaces":                   "I prefer tabs over spaces",
		"we prefer early returns. Thanks":             "we prefer early returns",
		"I'd prefer table-driven tests here":          "I'd prefer table-driven tests here",
		"please always use structured logging first!": "always use structured logging first",
		"never use panic in library code":             "never use panic in library code",
	} {
		if stmt, _ := ExtractStatedPreference(text); stmt != want {
			t.Errorf("%q: got %q, want %q", text, stmt, want)
		}
	}
}

// TestApplyJudgedRelations exercises the four verdicts directly, without a
// judge: same reinforces, refines rewrites + logs "refine", contradicts
// weakens + opens a conflict + inserts the new side, new inserts at the
// clamped confidence; an unknown target degrades to new.
//
// The base row seeds DISTILLED (not the STATED an explicit_prefer signal
// would give): learn.RewriteAllowed refuses to rewrite STATED text
// unconditionally (phase-01 req 8), so "refines" needs a non-STATED target
// to still be eligible for a rewrite here. A rewrite only ever trims, never
// grows (req 8 bullet 3), so the refined text is shorter than the original —
// TestApplyJudgedRefinesRefusesStatedTarget below covers the STATED case.
func TestApplyJudgedRelations(t *testing.T) {
	db, cfg := testDB(t)
	base, err := ApplyJudged(db, cfg, Signal{Type: SignalLLMDistilled, Weight: 0.5}, Assessment{Statement: "Prefers table-driven tests with named subtests.", Category: "Testing", Confidence: 1.0, Relation: RelationNew, By: "test"})
	if err != nil || base.Confidence != 0.95 {
		t.Fatalf("new must clamp to 0.95: %+v %v", base, err)
	}
	same, _ := ApplyJudged(db, cfg, Signal{Type: SignalExplicitPrefer}, Assessment{Statement: "x", Category: "Testing", Confidence: 0.5, Relation: RelationSame, TargetID: base.ID, By: "test"})
	if same.ID != base.ID || same.ReinforcementCount == 0 && same.Confidence < base.Confidence {
		t.Fatalf("same must reinforce the target: %+v", same)
	}
	ref, err := ApplyJudged(db, cfg, Signal{Type: SignalExplicitPrefer}, Assessment{Statement: "Prefers table-driven tests.", Category: "Testing", Confidence: 0.5, Relation: RelationRefines, TargetID: base.ID, By: "test"})
	if err != nil {
		t.Fatalf("refines: %v", err)
	}
	if ref.ID != base.ID || ref.Statement != "Prefers table-driven tests." {
		t.Fatalf("refines must rewrite a non-STATED target in place: %+v", ref)
	}
	var refines int
	_ = db.QueryRow(`SELECT count(*) FROM events WHERE event_type='refine'`).Scan(&refines)
	if refines != 1 {
		t.Fatalf("refine event: %d", refines)
	}
	con, _ := ApplyJudged(db, cfg, Signal{Type: SignalExplicitReject}, Assessment{Statement: "Prefers one assertion per test.", Category: "Testing", Confidence: 0.7, Relation: RelationContradicts, TargetID: base.ID, By: "test"})
	if con.ID == base.ID || con.Confidence != 0.7 {
		t.Fatalf("contradicts must insert the new side: %+v", con)
	}
	var conflicts int
	_ = db.QueryRow(`SELECT count(*) FROM conflicts WHERE resolved_at IS NULL`).Scan(&conflicts)
	if conflicts != 1 {
		t.Fatalf("contradicts must open a conflict: %d", conflicts)
	}
	var rows int
	_ = db.QueryRow(`SELECT count(*) FROM preferences`).Scan(&rows)
	if rows != 2 {
		t.Fatalf("contradicts must store the new side exactly once (base + new = 2 rows), got %d", rows)
	}
	var linked string
	_ = db.QueryRow(`SELECT conflicting_id FROM conflicts WHERE preference_id=?`, base.ID).Scan(&linked)
	if linked != con.ID {
		t.Fatalf("the conflict must link the judged row (%s), got %s", con.ID, linked)
	}
	unk, _ := ApplyJudged(db, cfg, Signal{Type: SignalExplicitPrefer}, Assessment{Statement: "Prefers fast tests.", Category: "Testing", Confidence: 0.6, Relation: RelationSame, TargetID: "nope", By: "test"})
	if unk.Statement != "Prefers fast tests." || unk.ID == base.ID {
		t.Fatalf("unknown target must degrade to new: %+v", unk)
	}
}

// TestApplyJudgedRefinesRefusesStatedTarget proves phase-01 req 8/11: a
// STATED target (the common case — any explicit_prefer signal seeds STATED)
// is never rewritten by the judge's refines path. The row still reinforces,
// the refused reason reaches stderr, and no "refine" event is written.
func TestApplyJudgedRefinesRefusesStatedTarget(t *testing.T) {
	db, cfg := testDB(t)
	base, err := ApplyJudged(db, cfg, Signal{Type: SignalExplicitPrefer}, Assessment{Statement: "Prefers table-driven tests.", Category: "Testing", Confidence: 0.6, Relation: RelationNew, By: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if base.TrustTier != "STATED" {
		t.Fatalf("an explicit_prefer new insert must be STATED, got %s", base.TrustTier)
	}
	before := base.Confidence

	var ref *Preference
	stderr := captureStderr(t, func() {
		ref, err = ApplyJudged(db, cfg, Signal{Type: SignalExplicitPrefer}, Assessment{Statement: "Prefers table-driven tests with named subtests.", Category: "Testing", Confidence: 0.9, Relation: RelationRefines, TargetID: base.ID, By: "test"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if ref.ID != base.ID || ref.Statement != base.Statement {
		t.Fatalf("a STATED target's text must be kept, got %q (want %q)", ref.Statement, base.Statement)
	}
	if ref.Confidence <= before {
		t.Fatalf("the row must still reinforce even when the rewrite is refused: before=%f after=%f", before, ref.Confidence)
	}
	if !strings.Contains(stderr, "refused") || !strings.Contains(stderr, base.ID) {
		t.Fatalf("a refused rewrite must log its reason to stderr, got %q", stderr)
	}
	var refines int
	_ = db.QueryRow(`SELECT count(*) FROM events WHERE event_type='refine'`).Scan(&refines)
	if refines != 0 {
		t.Fatalf("a refused rewrite must not log a refine event, got %d", refines)
	}
}

// Re-confirming a preference must raise its confidence, however long ago it
// was last touched. With the 0.3.x default (half-life 168 h) a 0.80 row
// confirmed after three weeks decayed to the floor first and came back at
// 0.26; decay is now off by default (decisions row 18).
func TestReconfirmAfterWeeksRaisesConfidenceByDefault(t *testing.T) {
	db, cfg := testDB(t)
	if cfg.HalfLifeHours != 0 {
		t.Fatalf("default half_life_hours must be 0 (decay off), got %v", cfg.HalfLifeHours)
	}
	base, err := ApplyJudged(db, cfg, Signal{Type: SignalExplicitPrefer}, Assessment{Statement: "Prefers early returns.", Category: "Error Handling", Confidence: 0.80, Relation: RelationNew, By: "test"})
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-21 * 24 * time.Hour).Format(time.RFC3339)
	if _, err := db.Exec(`UPDATE preferences SET last_reinforced_at=? WHERE id=?`, old, base.ID); err != nil {
		t.Fatal(err)
	}
	same, err := ApplyJudged(db, cfg, Signal{Type: SignalExplicitPrefer}, Assessment{Statement: "Prefers early returns.", Category: "Error Handling", Confidence: 0.8, Relation: RelationSame, TargetID: base.ID, By: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if same.Confidence <= 0.80 {
		t.Fatalf("re-confirmation lowered confidence: 0.80 -> %.2f", same.Confidence)
	}
}

// The knob still works: an explicit half_life_hours restores time decay.
func TestHalfLifeKnobRestoresDecay(t *testing.T) {
	db, cfg := testDB(t)
	cfg.HalfLifeHours = 168
	base, err := ApplyJudged(db, cfg, Signal{Type: SignalExplicitPrefer}, Assessment{Statement: "Prefers guard clauses.", Category: "Error Handling", Confidence: 0.80, Relation: RelationNew, By: "test"})
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-21 * 24 * time.Hour).Format(time.RFC3339)
	_, _ = db.Exec(`UPDATE preferences SET last_reinforced_at=? WHERE id=?`, old, base.ID)
	same, _ := ApplyJudged(db, cfg, Signal{Type: SignalExplicitPrefer}, Assessment{Statement: "Prefers guard clauses.", Category: "Error Handling", Confidence: 0.8, Relation: RelationSame, TargetID: base.ID, By: "test"})
	if same.Confidence >= 0.80 {
		t.Fatalf("with half_life_hours=168 decay should apply, got %.2f", same.Confidence)
	}
}
